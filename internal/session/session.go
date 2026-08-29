// Package session writes the persistent record of a harness run
// (or interactive exchange) under a state directory:
//
//	<state-dir>/<session-id>/session.json   identity + config snapshot + final status/exit
//	<state-dir>/<session-id>/messages.jsonl per-message log (one JSON object per line)
//	<state-dir>/<session-id>/events.jsonl   canonical session record (the Run-002 sidecar)
//
// Run 008 introduces this package. The Writer type is the cmd-side
// seam — every run-mode (headless or interactive) call opens a
// Writer, accumulates messages via AppendMessage, and closes via
// Write on exit (success / interrupted / failure). The events.jsonl
// file is owned by the existing event.Emitter; the Writer does NOT
// write events.jsonl. The Writer writes session.json + messages.jsonl.
//
// The package has no third-party dependencies (Go stdlib only).
package session

import "time"

// Status is the canonical final status of a session. The mapping
// matches the event.Emitter's status event payloads:
//   "completed" — success path (loop's RunOne returned nil error)
//   "interrupted" — SIGINT/SIGTERM-triggered cancellation (Run 007)
//   "failed" — non-cancellation error path (*model.ModelError or generic)
type Status string

const (
	StatusCompleted   Status = "completed"
	StatusInterrupted Status = "interrupted"
	StatusFailed      Status = "failed"
)

// Config is the resolved-config snapshot persisted to session.json.
// The field set is the binding contract for SCOPE §17's "config
// snapshot, resolved permission"; fields are the lowercase/uppercase
// wire-form versions, matching the existing config.Load() JSON shape.
type Config struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Workspace  string `json:"workspace"`
	Permission string `json:"permission"`
	OutputMode string `json:"output_mode,omitempty"`
}

// Session is the in-memory representation of session.json. It is
// written exactly once at session end (via Writer.Write); partial
// session.json files are not written (the file is atomically replaced
// via os.WriteFile on close).
type Session struct {
	SessionID  string    `json:"session_id"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Status     Status    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	Config     Config    `json:"config"`
	EventsPath string    `json:"events_path"` // relative to the session dir, "events.jsonl"
}

// Message is one entry in messages.jsonl. The Role field is one of
// "user" / "assistant" / "tool"; Content is the message body (for
// tool messages, the JSON-encoded tool call / tool result); the
// Timestamp is set to the message-emission time.
//
// Run 008 ships a minimal schema — the messages.jsonl is the
// SCOPE §17 "execution history" record. Semantic memory is
// explicitly NOT in scope (per GOAL §5 reviewer duty #3 "no
// semantic-memory creep"). A future Run (e.g. Run 010 Context
// observability) may extend the schema.
type Message struct {
	Timestamp time.Time `json:"timestamp"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
}
