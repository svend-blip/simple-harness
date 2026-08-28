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

// ChatRequest is the outgoing chat-completions request body, minus
// the fields the client merges in from Options (model, temperature,
// max_tokens, stream). The loop owns tool-call-related fields
// (tools, tool_choice) when handoff 010+ lands them.
type ChatRequest struct {
	Messages []Message `json:"messages"`
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
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Temperature float64   `json:"temperature"`
		MaxTokens   int       `json:"max_tokens"`
		Stream      bool      `json:"stream"`
	}{
		Model:       c.opts.Model,
		Messages:    req.Messages,
		Temperature: c.opts.Temperature,
		MaxTokens:   c.opts.MaxOutputTokens,
		Stream:      true,
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
