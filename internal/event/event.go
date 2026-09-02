// Package event is the versioned JSONL event/status surface for
// Simple Harness. It owns the wire shape (the V1 protocol: one JSON
// object per line, mandatory protocol_version, event, timestamp,
// session_id), the four event types the loop emits in handoff 010
// (started, assistant_stream, status, completed), and the
// mutex-serialized writes so a streaming callback goroutine cannot
// interleave a partial JSON line with another.
//
// The architecture's responsibility boundary for this package is in
// docs/ARCHITECTURE.md §"internal/event/" (a)/(b)/(c) and the V1
// schema is in §"Schema (V1)". The package does NOT decide which
// events to emit — that is the loop's job (see internal/loop). It
// does NOT own the output-mode split between terminal presentation
// and JSONL on stdout — that is a Run 004+ concern (the `--output
// terminal|jsonl` flag is deferred). In handoff 010 the JSONL always
// writes to a sidecar file the caller supplies; stdout stays clean
// for the streamed assistant text.
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy.
package event

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Event is one JSONL line in the sidecar / stdout-JSONL protocol.
// The base fields are mandatory; event-specific fields are populated
// per the event type. The json tags match ARCHITECTURE.md
// §"Schema (V1)": protocol_version (always "1" in V1), event,
// timestamp (RFC 3339 UTC), session_id. Event-specific fields use
// omitempty so empty values are elided from the wire.
type Event struct {
	ProtocolVersion string `json:"protocol_version"`
	Event           string `json:"event"`
	Timestamp       string `json:"timestamp"`
	SessionID       string `json:"session_id"`

	// Event-specific fields. Populated per the event type; absent
	// (omitempty) when not relevant.
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content,omitempty"`
	Status   string         `json:"status,omitempty"`
	Delta    string         `json:"delta,omitempty"`
	ExitCode int            `json:"exit_code,omitempty"`
	Config   *SessionConfig `json:"config,omitempty"`
	// Usage is the token-count block on "usage" events, emitted once
	// per model request when the upstream reports it (2026-09-02). It
	// is what a throughput measurement needs: completion tokens and the
	// reasoning share per request, not per session.
	Usage *UsageBlock `json:"usage,omitempty"`

	// CallID is the per-call identifier on tool_call and
	// tool_result events. The model emits a unique call_id
	// per tool invocation; the harness echoes the same value
	// on the matching tool_result so an external controller
	// can correlate the two events for a single dispatch
	// round. Populated on tool_call + tool_result events;
	// absent (omitempty) on all other event types.
	CallID string `json:"call_id,omitempty"`
	// Tool is the tool name on tool_call events. Populated
	// on tool_call events only; absent (omitempty) on all
	// other event types. The model-targeted tool name (e.g.
	// "apply_patch", "read_file") matches the
	// tools.Registry name space.
	Tool string `json:"tool,omitempty"`
	// ResultStatus is the tool-result status on tool_result
	// events. One of "ok" or "error"; absent (omitempty) on
	// all other event types. The exact status value matches
	// tools.Result.Status verbatim.
	ResultStatus string `json:"tool_result_status,omitempty"`
}

// SessionConfig is the identity card emitted in the `started` event.
// It carries the active model, endpoint, workspace, and permission
// mode — the visible identity SCOPE §4 requires.
type SessionConfig struct {
	Model      string `json:"model"`
	Endpoint   string `json:"endpoint"`
	Workspace  string `json:"workspace"`
	Permission string `json:"permission"`
}

// Emitter serializes Event values as JSONL to its writer. The
// internal mutex serializes writes so a callback goroutine can call
// Emit concurrently with the loop body without producing interleaved
// partial lines.
//
// One Emitter == one output stream (sidecar file or stdout-JSONL
// pipe). Construct via NewEmitter.
type Emitter struct {
	w         io.Writer
	sessionID string
	mu        sync.Mutex
	enc       *json.Encoder
}

// NewEmitter returns an Emitter that writes to w. sessionID is
// stamped onto every event whose Event.SessionID is empty. The
// underlying json.Encoder appends a newline after each Encode call,
// which gives the JSONL wire shape for free.
func NewEmitter(w io.Writer, sessionID string) *Emitter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Emitter{
		w:         w,
		sessionID: sessionID,
		enc:       enc,
	}
}

// Emit writes ev as one JSON object followed by a newline.
// Concurrent calls are serialized via the internal mutex so two
// goroutines cannot interleave bytes of one event with another.
//
// If ev.ProtocolVersion is empty, it is stamped to "1" (the V1
// protocol). If ev.Timestamp is empty, it is stamped to the current
// time in RFC 3339 UTC (nanosecond precision). If ev.SessionID is
// empty, it is stamped to the sessionID supplied to NewEmitter.
func (e *Emitter) Emit(ev Event) error {
	if ev.ProtocolVersion == "" {
		ev.ProtocolVersion = "1"
	}
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if ev.SessionID == "" {
		ev.SessionID = e.sessionID
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(ev)
}

// Started emits the "started" event with the active session identity
// (model, endpoint, workspace, permission). It is the session's
// identity card; one per session, emitted at the start of each turn
// per ARCHITECTURE.md §"Event types in V1".
func (e *Emitter) Started(cfg SessionConfig) error {
	return e.Emit(Event{
		Event:  "started",
		Config: &cfg,
	})
}

// Status emits a "status" event with the given SCOPE §23 status
// (e.g. "STREAMING", "COMPLETED", "FAILED", "INTERRUPTED"). The
// status string is the SCOPE value verbatim; the loop is the single
// owner of which status to emit when, this package only carries the
// value across the wire.
func (e *Emitter) Status(status string) error {
	return e.Emit(Event{
		Event:  "status",
		Status: status,
	})
}

// AssistantStream emits an "assistant_stream" event with the given
// delta. The delta is the same text that is written to the
// human-facing stdout; this sidecar is the machine-readable mirror
// per SCOPE §22 (the sidecar exists in addition to terminal
// presentation, never as a replacement).
func (e *Emitter) AssistantStream(delta string) error {
	return e.Emit(Event{
		Event: "assistant_stream",
		Role:  "assistant",
		Delta: delta,
	})
}

// Completed emits the "completed" event with the given exit code.
// The exit code follows SCOPE §28 (0 success, 1 generic failure, 2
// config error, 3 model/API failure, 4 permission violation, 5 tool
// failure, 6 interrupted). This is the terminal event for the
// session; one per session, emitted after the final status.
func (e *Emitter) Completed(exitCode int) error {
	return e.Emit(Event{
		Event:    "completed",
		ExitCode: exitCode,
	})
}

// ModelRequest emits a "model_request" event immediately before
// each model-client invocation. The event is the SCOPE §21 / GOAL
// §2-named signal that external controllers use to detect "the
// harness currently has an active model request". No payload beyond
// the base fields (protocol_version, event, timestamp, session_id).
func (e *Emitter) ModelRequest() error {
	return e.Emit(Event{
		Event: "model_request",
	})
}

// UsageBlock is the payload of a "usage" event.
type UsageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

// Usage emits a "usage" event with the token counts of the model
// request that just finished. Emitted only when the upstream reported
// a usage block (stream_options.include_usage), so its absence means
// "not measured", never "zero".
func (e *Emitter) Usage(u UsageBlock) error {
	return e.Emit(Event{
		Event: "usage",
		Usage: &u,
	})
}

// Interrupted emits the "interrupted" event per SCOPE §26's headless
// signal sequence: SIGINT/SIGTERM cancel the in-flight operation,
// the harness emits this event with the session_id, flushes the
// output stream, and exits 6 (SCOPE §28 interrupted). The event
// carries no payload beyond the base fields (protocol_version,
// event, timestamp, session_id) — it is the terminal signal of the
// interruption sequence, distinct from `completed(exit_code: 6)`
// which is the SCOPE §28 model-timeout terminal event.
//
// If sessionID is non-empty, it overrides the emitter's default
// session_id for this event (matching the loop's session identity
// at the moment of interruption). If sessionID is empty, the
// emitter's session_id is used.
func (e *Emitter) Interrupted(sessionID string) error {
	ev := Event{Event: "interrupted"}
	if sessionID != "" {
		ev.SessionID = sessionID
	}
	return e.Emit(ev)
}

// ToolCall emits a "tool_call" event when the model
// emits a tool-call delta the harness has chosen to
// dispatch. callID is the per-call identifier the model
// assigned to this dispatch (used on the matching
// tool_result event for correlation); toolName is the
// tool name from the assembled tools.Call. The event
// carries no payload beyond the base fields +
// call_id + tool. Run-time emissions of this event
// land in handoff 042 (the binding-pin slot); for
// handoff 041 the event TYPE exists beside the V1 six
// (satisfies TG2) but no production code path emits
// it yet.
func (e *Emitter) ToolCall(callID, toolName string) error {
	return e.Emit(Event{
		Event:  "tool_call",
		CallID: callID,
		Tool:   toolName,
	})
}

// ToolResult emits a "tool_result" event after the
// tools.Registry.Dispatch pipeline returns for a call.
// callID is the same per-call identifier the
// corresponding tool_call event carried (so an
// external controller can pair them); status is the
// tools.Result.Status value ("ok" or "error");
// content is the result body for ok results or the
// structured error for error results (the dispatch
// pipeline's structured-error JSON). Run-time
// emissions of this event land in handoff 042; for
// handoff 041 the event TYPE exists beside the V1
// six (satisfies TG2).
func (e *Emitter) ToolResult(callID, status, content string) error {
	ev := Event{
		Event:        "tool_result",
		CallID:       callID,
		ResultStatus: status,
	}
	if content != "" {
		ev.Content = content
	}
	return e.Emit(ev)
}

// SessionID returns the session_id this emitter stamps onto events.
func (e *Emitter) SessionID() string {
	return e.sessionID
}
