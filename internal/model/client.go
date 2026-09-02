// Package model is the OpenAI-compatible /v1/chat/completions client
// for Simple Harness. It is a leaf package that does one thing: POST a
// chat-completions request to a resolved endpoint and stream the SSE
// response back to a callback. The wire shape matches SCOPE §6
// ("OpenAI-compatible model interface"); the architecture's
// responsibility boundary for this package is in
// docs/ARCHITECTURE.md §"internal/model/" (lines 143-158).
//
// This package does NOT resolve configuration — callers pass an
// Options struct in. It does NOT do retries, retries belong to the
// loop (internal/loop, handoff 009). It does NOT log or surface
// secrets — ModelError carries no request or response body beyond a
// status, a line number, or an upstream message.
//
// Architectural boundary: this is a Simple Harness component. It
// does not import orchestration, harness selection, GPU/VRAM
// allocation, model lifecycle, or Model Allocator policy.
package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Options is the subset of the resolved config the model client
// needs. The caller (internal/loop in handoff 009, or a test)
// supplies this — the client does not call config.Load() itself
// (docs/ARCHITECTURE.md §"internal/model/"(b)).
type Options struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	MaxOutputTokens int
	// ReasoningEffort, when non-empty, is sent as `reasoning_effort`.
	ReasoningEffort string
	// EnableThinking (nil = omitted) and ThinkingBudget (0 = omitted) are
	// sent as `enable_thinking` / `thinking_budget` (DashScope-style
	// hybrid-thinking controls; other providers never see them).
	EnableThinking *bool
	ThinkingBudget int
	RequestTimeout time.Duration
}

// Message is one chat-completions message. Role is one of "system",
// "user", "assistant", "tool" per SCOPE §6; Content is a plain
// string for V1 — no multimodal, no content array.
//
// ToolCalls carries the per-call entries when the assistant emits
// tool calls in a turn; the OpenAI chat-completions spec requires
// the assistant tool_calls message BEFORE the per-call tool
// messages so the server can correlate the tool_call_id fields on
// the follow-up. ToolCallID carries the correlation id on tool
// messages back to the originating assistant tool_call. Both
// fields use `omitempty` so plain {Role, Content} messages
// serialize byte-identically against the pre-amendment-4 wire.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolDefinition is the OpenAI function-calling wire shape for a
// single advertised tool. The harness emits {"type":"function",
// "function":{...}} per the OpenAI chat-completions `tools` spec;
// the Type field lets future handoffs carry other tool kinds
// (e.g., "code_interpreter") without a breaking change.
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function ToolDefinitionFunc `json:"function"`
}

// ToolDefinitionFunc is one tool's name + description + JSON-schema
// parameters. The loop builds these from tools.Tool.Meta() +
// tools.Tool.Schema() — the Description carries the human-readable
// purpose; the Parameters is the JSON-schema rendering of the
// tool's tools.Schema (the JSON-schema-lite shape maps 1:1 onto
// the OpenAI JSON-schema subset: type/required/properties/
// additionalProperties).
type ToolDefinitionFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest is the outgoing chat-completions request body, minus
// the fields the client merges in from Options (model, temperature,
// max_tokens, stream). The loop owns Tools + ToolChoice (Run 023 /
// handoff 073) and populates them from tools.Registry at the call
// site. Tools is the OpenAI function-calling wire shape
// ({"type":"function","function":{"name","description","parameters"}});
// ToolChoice is "auto" when Tools is non-empty and the field is
// omitted on the wire otherwise. The ,omitempty tags keep existing
// serialization tests valid for empty-registry callers.
type ChatRequest struct {
	Messages   []Message        `json:"messages"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice any              `json:"tool_choice,omitempty"`
}

// ToolCallFragment is one tool-call delta as carried in
// StreamEvent.ToolCallDelta. The loop accumulates these across
// multiple StreamEvents until the upstream emits a non-empty
// finish_reason.
type ToolCallFragment struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	ArgsDelta string `json:"args_delta,omitempty"`
}

// ToolCall is the assembled tool call after the loop
// has merged all per-index ToolCallFragment deltas
// emitted by the upstream during a single model turn.
// Index is the position in the upstream tool_calls
// array; ID is the upstream-assigned call identifier
// (used on the tool_call + tool_result events for
// correlation); Name is the tool name; Arguments is
// the JSON-decoded form (strings as Go strings,
// numbers as float64, booleans as bool, arrays as
// []any, objects as map[string]any).
//
// ArgumentsRaw carries the concatenated ArgsDelta
// raw string from accumulation, populated by
// AccumulateToolCallFragment as each fragment arrives
// and parsed once at finish by FinalizeToolCalls.
// The field is exposed for inspection (tests +
// debugging) but elided from the wire via `json:"-"`
// — the assistant-with-tool_calls message marshals
// Arguments as a JSON object, not as a raw string,
// per the OpenAI chat-completions spec. The field
// is empty after FinalizeToolCalls has populated
// Arguments; callers that need the original raw
// string should capture it before finalization.
//
// The GOAL §2 deliverable 3 delta-assembly seam lives
// in this file: the helpers ParseToolCallArgs +
// AccumulateToolCallFragment + FinalizeToolCalls
// below let callers assemble complete ToolCall values
// from stream deltas. The loop's existing private
// accumulateToolCallFragment + parseToolCallArgs
// helpers stay in place until handoff 042 either
// migrates them or folds them away.
type ToolCall struct {
	Index        int            `json:"index"`
	ID           string         `json:"id,omitempty"`
	Type         string         `json:"type,omitempty"`
	Name         string         `json:"name"`
	Arguments    map[string]any `json:"arguments"`
	ArgumentsRaw string         `json:"-"`
}

// ParseToolCallArgs parses the accumulated JSON
// ArgsDelta into a map[string]any. The helper handles
// the common case where the model emits a single
// {"path": "file", "patch": "..."} JSON object. If the
// JSON is malformed (not an object, or unparseable),
// returns a *json.SyntaxError — the loop's caller
// appends the parse error to the message history as
// a tool-result message with status="error" and the
// model gets to retry (SCOPE §31 "untrusted input"
// discipline: structured rejection, never a crash).
//
// An empty argsJSON returns an empty map (not nil)
// and a nil error; the loop treats this as a
// parameterless tool call.
//
// The helper is the model-side counterpart to the
// loop's private parseToolCallArgs (handoff 040).
// handoff 042 will either migrate the loop's caller
// to this exported helper or fold the private
// duplicate away.
func ParseToolCallArgs(argsJSON string) (map[string]any, error) {
	if argsJSON == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AccumulateToolCallFragment merges a ToolCallFragment
// into the per-index accumulator. The first fragment
// for a given Index initializes the ToolCall with
// Name + a fresh Arguments map + a fresh empty
// ArgumentsRaw buffer; subsequent fragments for the
// same Index append ArgsDelta to ArgumentsRaw (raw
// string concatenation, NO per-delta parse).
//
// The OpenAI chat-completions streaming spec emits
// tool_calls[].function.arguments as RAW STRING
// FRAGMENTS — each fragment is a partial slice of
// the final arguments string, not a partial JSON
// object. Only the concatenation is valid JSON.
// The pre-amendment-6 code did json.Unmarshal on
// each fragment and treated each fragment as a
// complete JSON object; for a long argument that
// splits across many fragments (supervisor-measured:
// 21 chunks on a single apply_patch call against
// live MiniMax-M3), the first partial fragment
// returned *json.SyntaxError and the accumulator
// surfaced the error → status:FAILED → exit_code:1.
// The fix (Run 023 amendment 6 / handoff 077)
// concatenates the raw strings here and defers the
// single ParseToolCallArgs call to FinalizeToolCalls
// at finish_reason.
//
// The function returns nil on every non-empty
// ArgsDelta — partial-fragment parsing is no longer
// attempted. Empty ArgsDelta is a no-op (returns nil
// via the early return below). Genuinely-malformed
// assembled arguments are surfaced by
// FinalizeToolCallArgs at finish, not here.
//
// The helper is the model-side counterpart to the
// loop's private accumulateToolCallFragment
// (handoff 040). handoff 042 will either migrate the
// loop's caller to this exported helper or fold the
// private duplicate away.
func AccumulateToolCallFragment(accum map[int]*ToolCall, frag *ToolCallFragment) error {
	if frag == nil {
		return nil
	}
	existing, ok := accum[frag.Index]
	if !ok || existing == nil {
		existing = &ToolCall{
			Index:     frag.Index,
			ID:        frag.ID,
			Name:      frag.Name,
			Arguments: map[string]any{},
		}
		accum[frag.Index] = existing
	}
	if frag.ID != "" {
		existing.ID = frag.ID
	}
	if frag.Name != "" {
		existing.Name = frag.Name
	}
	if frag.ArgsDelta == "" {
		return nil
	}
	existing.ArgumentsRaw += frag.ArgsDelta
	return nil
}

// FinalizeToolCalls parses each accumulated ToolCall's
// ArgumentsRaw into its Arguments map[string]any via
// ParseToolCallArgs. The loop calls this exactly
// ONCE — after the SSE stream reports a non-empty
// finish_reason and BEFORE the assistant-with-tool_calls
// message append at internal/loop/loop.go:784-801 —
// so the per-call parse happens once on the assembled
// string, not per fragment. Pre-amendment-6, the parse
// happened per fragment inside AccumulateToolCallFragment;
// amendment 6 moves the parse to here.
//
// Returns the first error encountered (joined via
// errors.Join for multi-call failures). The loop's
// existing error-propagation pattern surfaces any
// error as status:FAILED + completed(exit_code:1)
// (see internal/loop/loop.go:727 — the same pattern
// applies to any per-call parse failure). Per-call
// inline parsing at the loop's assistant-with-tool_calls
// iteration block is an alternative sanctioned by
// amendment 6; the helper is the cleaner factoring for
// callers that want a single all-or-nothing parse pass.
//
// A nil ToolCall in the accumulator is skipped (the
// accumulator's per-index invariant permits gaps).
// A ToolCall with empty ArgumentsRaw populates
// Arguments with the empty-map sentinel (matches
// ParseToolCallArgs("")'s contract — a parameterless
// tool call).
func FinalizeToolCalls(accum map[int]*ToolCall) error {
	var errs []error
	for _, call := range accum {
		if call == nil {
			continue
		}
		parsed, err := ParseToolCallArgs(call.ArgumentsRaw)
		if err != nil {
			errs = append(errs, fmt.Errorf("model: finalize tool call index %d: %w", call.Index, err))
			continue
		}
		call.Arguments = parsed
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Usage carries the token-count block from upstream, when present
// (typically only on the final event). Zero value otherwise.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CompletionTokensDetails carries the reasoning split some
	// OpenAI-compatible endpoints report (DashScope, OpenAI). Nil when
	// the upstream does not send it.
	CompletionTokensDetails *UsageDetails `json:"completion_tokens_details,omitempty"`
}

// UsageDetails is the completion_tokens_details block.
type UsageDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ReasoningTokens returns the reasoning share of the completion, or 0
// when the upstream did not report one.
func (u *Usage) ReasoningTokens() int {
	if u == nil || u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}

// StreamEvent is one parsed SSE event. Exactly one of Delta,
// ToolCallDelta, FinishReason, or Usage is populated per event;
// multiple may coexist when the upstream combines a delta with a
// non-empty finish_reason in the same payload.
type StreamEvent struct {
	// Delta is the incremental text content
	// (choices[0].delta.content). Empty when the upstream
	// emits only a finish_reason, tool-call delta, or usage.
	Delta string
	// ToolCallDelta is the incremental tool-call fragment
	// when the delta includes a tool_calls array
	// (choices[0].delta.tool_calls). Nil otherwise.
	ToolCallDelta *ToolCallFragment
	// FinishReason is "stop", "length", "tool_calls", etc. when
	// the upstream reports a non-empty finish_reason; empty
	// otherwise.
	FinishReason string
	// Usage is the token-count block when the upstream reports
	// it; nil otherwise.
	Usage *Usage
}

// ErrorKind enumerates the distinct failure modes the model
// client can surface. The loop maps these to SCOPE §28 exit codes
// (ErrHTTP / ErrParse / ErrUpstream -> 3, ErrTimeout -> 6).
type ErrorKind int

const (
	// ErrUnknown is the zero value, never returned.
	ErrUnknown ErrorKind = iota
	// ErrHTTP is HTTP non-2xx; ModelError.StatusCode and
	// ModelError.Body carry the upstream status and body.
	ErrHTTP
	// ErrParse is a malformed SSE payload (JSON unmarshal
	// failure or an unreadable response body). ModelError.Line
	// names the line number; ModelError.Err wraps the cause.
	ErrParse
	// ErrUpstream is a JSON `error` object in the SSE stream —
	// the upstream provider refused the request. ModelError.Code
	// and ModelError.Message carry the upstream fields.
	ErrUpstream
	// ErrTimeout is request timeout or context cancellation
	// during the request. ModelError.Err wraps the cause.
	ErrTimeout
)

// ModelError is the typed error returned by Client methods. Its
// fields are populated according to Kind (see the comment on each
// ErrorKind constant).
type ModelError struct {
	Kind       ErrorKind
	StatusCode int
	Body       string
	Line       int
	Err        error
	Code       string
	Message    string
}

// Error renders a human-readable description of the failure,
// formatted by Kind. It satisfies the error interface.
func (e *ModelError) Error() string {
	if e == nil {
		return "<nil ModelError>"
	}
	switch e.Kind {
	case ErrHTTP:
		return fmt.Sprintf("model: HTTP %d: %s", e.StatusCode, e.Body)
	case ErrParse:
		if e.Line > 0 {
			return fmt.Sprintf("model: parse error on line %d: %v", e.Line, e.Err)
		}
		return fmt.Sprintf("model: parse error: %v", e.Err)
	case ErrUpstream:
		return fmt.Sprintf("model: upstream error %q: %s", e.Code, e.Message)
	case ErrTimeout:
		return fmt.Sprintf("model: timeout: %v", e.Err)
	default:
		return fmt.Sprintf("model: unknown error: %v", e.Err)
	}
}

// Client is the OpenAI-compatible model client. Construct it with
// NewClient and call ChatStream.
type Client struct {
	opts Options
	http *http.Client
}

// NewClient constructs a Client with the given Options. The
// configured RequestTimeout is applied as http.Client.Timeout,
// which covers the entire request including body read — this is
// the documented choice in the handoff (a more sophisticated
// implementation would split connect timeout from per-chunk read
// timeout, but SCOPE §6 only names request_timeout as a single
// field).
func NewClient(opts Options) *Client {
	return &Client{
		opts: opts,
		http: &http.Client{Timeout: opts.RequestTimeout},
	}
}

// ChatStream POSTs req to the configured endpoint and invokes
// onDelta for each parsed SSE event. Returns nil on a clean
// [DONE]; returns a *ModelError on any failure. Honours ctx
// cancellation: a cancelled context produces an ErrTimeout
// (context.Canceled) or ErrHTTP (other network errors) as
// appropriate.
//
// The wire shape: POST <BaseURL>/v1/chat/completions with
// Content-Type: application/json, an optional Authorization: Bearer
// header when APIKey is non-empty, and a JSON body containing
// model, messages, temperature, max_tokens, and stream: true.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onDelta func(StreamEvent) error) error {
	body, err := json.Marshal(struct {
		Model       string        `json:"model"`
		Messages    []wireMessage `json:"messages"`
		Temperature float64       `json:"temperature"`
		MaxTokens   int           `json:"max_tokens"`
		Reasoning   string        `json:"reasoning_effort,omitempty"`
		Thinking    *bool         `json:"enable_thinking,omitempty"`
		Budget      int           `json:"thinking_budget,omitempty"`
		Stream      bool          `json:"stream"`
		// stream_options.include_usage asks the upstream to send the
		// usage block on the final chunk; without it OpenAI-compatible
		// streams carry no token counts at all (2026-09-02).
		StreamOpts streamOptions    `json:"stream_options"`
		Tools      []ToolDefinition `json:"tools,omitempty"`
		ToolChoice any              `json:"tool_choice,omitempty"`
	}{
		Model:       c.opts.Model,
		Messages:    toWireMessages(req.Messages),
		Temperature: c.opts.Temperature,
		MaxTokens:   c.opts.MaxOutputTokens,
		Reasoning:   c.opts.ReasoningEffort,
		Thinking:    c.opts.EnableThinking,
		Budget:      c.opts.ThinkingBudget,
		StreamOpts:  streamOptions{IncludeUsage: true},
		Stream:      true,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	})
	if err != nil {
		return &ModelError{Kind: ErrParse, Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.opts.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return &ModelError{Kind: ErrParse, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.opts.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &ModelError{Kind: ErrTimeout, Err: err}
		}
		return &ModelError{Kind: ErrHTTP, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &ModelError{
			Kind:       ErrHTTP,
			StatusCode: resp.StatusCode,
			Body:       string(bodyBytes),
		}
	}

	return c.parseSSE(ctx, resp.Body, onDelta)
}

// parseSSE reads body as text/event-stream and dispatches each
// parsed payload to onDelta. It stops on the [DONE] sentinel and
// returns nil; parse errors surface as ErrParse; upstream JSON
// error objects surface as ErrUpstream; context cancellation
// between lines surfaces as the ctx error (mapped to ErrTimeout
// by ChatStream's earlier timeout classification if the body read
// hits it). The scanner buffer is 1 MiB per line, matching OpenAI's
// de-facto convention.
func (c *Client) parseSSE(ctx context.Context, body io.Reader, onDelta func(StreamEvent) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return &ModelError{Kind: ErrTimeout, Err: err}
			}
			return &ModelError{Kind: ErrHTTP, Err: err}
		}
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}
		const prefix = "data: "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimPrefix(line, prefix)
		if payload == "[DONE]" {
			return nil
		}

		var raw struct {
			Choices []struct {
				Delta struct {
					Content   string        `json:"content"`
					ToolCalls []toolCallRaw `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return &ModelError{Kind: ErrParse, Line: lineNum, Err: err}
		}
		if raw.Error != nil {
			return &ModelError{
				Kind:    ErrUpstream,
				Code:    raw.Error.Code,
				Message: raw.Error.Message,
			}
		}

		for _, ch := range raw.Choices {
			ev := StreamEvent{FinishReason: ch.FinishReason}
			if ch.Delta.Content != "" {
				ev.Delta = ch.Delta.Content
			}
			if len(ch.Delta.ToolCalls) > 0 {
				tc := ch.Delta.ToolCalls[0]
				ev.ToolCallDelta = &ToolCallFragment{
					Index:     tc.Index,
					ID:        tc.ID,
					Name:      tc.Function.Name,
					ArgsDelta: tc.Function.Arguments,
				}
			}
			if err := onDelta(ev); err != nil {
				return err
			}
		}
		if raw.Usage != nil {
			if err := onDelta(StreamEvent{Usage: raw.Usage}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &ModelError{Kind: ErrTimeout, Err: err}
		}
		return &ModelError{Kind: ErrParse, Line: lineNum, Err: err}
	}
	return nil
}

// toolCallRaw captures the OpenAI wire shape for a single
// tool_calls[] entry before it is collapsed into the public
// ToolCallFragment type. Keeping it private keeps the public
// surface small and the wire-shape decoding self-contained.
type toolCallRaw struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// toolCallWireEntry captures the OpenAI chat-completions wire
// shape for a single tool_calls[] entry that the harness EMITS on
// the assistant message: {id, type:"function", function:{name,
// arguments}}. The Arguments field is the JSON-encoded STRING form
// (the OpenAI spec serializes the JSON-decoded arguments map as a
// string on the wire), so we re-encode at conversion time. This
// shape is private — the public surface keeps model.ToolCall as
// the domain type with the JSON-decoded Arguments map; the wire
// shape is the model-internal marshal concern.
type toolCallWireEntry struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// wireMessage is the OpenAI wire shape for one chat-completions
// message. It mirrors model.Message but uses toolCallWireEntry for
// the per-message tool_calls[] (nested function:{name, arguments})
// instead of the public flat ToolCall shape. Used internally by
// ChatStream's marshal path; the public ChatRequest type continues
// to carry []model.Message so callers (the loop) populate the
// domain ToolCall type and conversion happens at the wire edge.
type wireMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCalls  []toolCallWireEntry `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
}

// toWireMessage converts a domain model.Message into its wire
// representation. Empty ToolCalls slice is elided on the wire
// (omitempty); the Arguments map is re-encoded as a JSON string
// per the OpenAI spec; a nil Arguments map marshals as the literal
// string "null" which the spec accepts for parameterless calls.
func toWireMessage(m Message) wireMessage {
	w := wireMessage{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		w.ToolCalls = make([]toolCallWireEntry, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			entry := toolCallWireEntry{
				ID:   tc.ID,
				Type: tc.Type,
			}
			entry.Function.Name = tc.Name
			if tc.Arguments != nil {
				b, err := json.Marshal(tc.Arguments)
				if err == nil {
					entry.Function.Arguments = string(b)
				}
			}
			w.ToolCalls = append(w.ToolCalls, entry)
		}
	}
	return w
}

// toWireMessages converts a slice of domain messages to the wire
// representation. A convenience for the ChatStream marshal path.
func toWireMessages(msgs []Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toWireMessage(m))
	}
	return out
}

// streamOptions is the OpenAI-compatible stream_options request block.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
