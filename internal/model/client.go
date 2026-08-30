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
	RequestTimeout  time.Duration
}

// Message is one chat-completions message. Role is one of "system",
// "user", "assistant", "tool" per SCOPE §6; Content is a plain
// string for V1 — no multimodal, no content array.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
// The GOAL §2 deliverable 3 delta-assembly seam lives
// in this file: the helpers ParseToolCallArgs +
// AccumulateToolCallFragment below let callers
// assemble complete ToolCall values from stream
// deltas. The loop's existing private
// accumulateToolCallFragment + parseToolCallArgs
// helpers stay in place until handoff 042 either
// migrates them or folds them away.
type ToolCall struct {
	Index     int            `json:"index"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
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
// Name + a fresh Arguments map; subsequent fragments
// for the same Index merge ArgsDelta into the
// Arguments map by re-parsing the accumulated buffer
// (the model emits ArgsDelta as a partial JSON object
// that becomes a complete object once the upstream
// emits the closing brace; we re-parse the buffer
// each step so partial state survives across
// fragments).
//
// Returns an error if the accumulated JSON is
// malformed. The caller (the loop's onDelta handler)
// treats the error as a per-delta parse failure: the
// SCOPE §31 "untrusted input" discipline says
// structured rejection is the harness's contract with
// the model, not a hard failure; the loop continues
// and the model gets to retry on the next turn.
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
	var merged map[string]any
	if err := json.Unmarshal([]byte(frag.ArgsDelta), &merged); err != nil {
		return fmt.Errorf("model: parse tool-call args delta: %w", err)
	}
	for k, v := range merged {
		existing.Arguments[k] = v
	}
	return nil
}

// Usage carries the token-count block from upstream, when present
// (typically only on the final event). Zero value otherwise.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
		Model       string           `json:"model"`
		Messages    []Message        `json:"messages"`
		Temperature float64          `json:"temperature"`
		MaxTokens   int              `json:"max_tokens"`
		Stream      bool             `json:"stream"`
		Tools       []ToolDefinition `json:"tools,omitempty"`
		ToolChoice  any              `json:"tool_choice,omitempty"`
	}{
		Model:       c.opts.Model,
		Messages:    req.Messages,
		Temperature: c.opts.Temperature,
		MaxTokens:   c.opts.MaxOutputTokens,
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
