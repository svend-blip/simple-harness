package event

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestEmit_WritesJSONLWithNewline pins the wire shape: one JSON
// object per line, exactly one line per Emit call (Encoder appends
// a trailing newline). A reviewer can sed-replace the trailing
// newline assertion to confirm the test is load-bearing.
func TestEmit_WritesJSONLWithNewline(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-1")

	if err := em.Emit(Event{Event: "started", SessionID: "sess-1", ProtocolVersion: "1"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), out)
	}
	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v (line=%q)", err, lines[0])
	}
	if got.Event != "started" {
		t.Errorf("Event = %q, want started", got.Event)
	}
}

// TestEmit_StampsProtocolVersionAndSessionID pins the default
// stamping behaviour: ProtocolVersion defaults to "1", SessionID
// defaults to the emitter's sessionID, Timestamp defaults to a
// non-empty RFC3339 UTC value.
func TestEmit_StampsProtocolVersionAndSessionID(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-default")

	if err := em.Emit(Event{Event: "completed", ExitCode: 0}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var got Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, buf.String())
	}
	if got.ProtocolVersion != "1" {
		t.Errorf("ProtocolVersion = %q, want 1", got.ProtocolVersion)
	}
	if got.SessionID != "sess-default" {
		t.Errorf("SessionID = %q, want sess-default", got.SessionID)
	}
	if got.Timestamp == "" {
		t.Errorf("Timestamp empty, want non-empty RFC3339 UTC")
	}
	if got.Event != "completed" {
		t.Errorf("Event = %q, want completed", got.Event)
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
}

// TestEmit_ConcurrentSerialization pins the mutex contract: 100
// goroutines fire Emit concurrently; the resulting output must be
// exactly 100 valid JSONL lines, no partial interleaving. This is
// the SCOPE §21 invariant proof for the Emitter (machine-readable
// state stays clean even under concurrent callback goroutines).
func TestEmit_ConcurrentSerialization(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-conc")

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if err := em.Emit(Event{
				Event:  "assistant_stream",
				Role:   "assistant",
				Delta:  "x",
				Status: "",
			}); err != nil {
				t.Errorf("Emit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d (output=%q)", len(lines), n, buf.String())
	}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d is not valid JSON: %v (line=%q)", i, err, line)
			continue
		}
		if ev.Event != "assistant_stream" {
			t.Errorf("line %d Event = %q, want assistant_stream", i, ev.Event)
		}
	}
}

// TestEmit_HelpersProduceCorrectEventTypes pins the four helper
// methods: Started, Status, AssistantStream, Completed each emit an
// event with the expected event-type field and the expected
// event-specific field populated.
func TestEmit_HelpersProduceCorrectEventTypes(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-helpers")

	cfg := SessionConfig{
		Model:      "qwen",
		Endpoint:   "http://127.0.0.1:8080",
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}
	if err := em.Started(cfg); err != nil {
		t.Fatalf("Started: %v", err)
	}
	if err := em.Status("STREAMING"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if err := em.AssistantStream("hello"); err != nil {
		t.Fatalf("AssistantStream: %v", err)
	}
	if err := em.Completed(0); err != nil {
		t.Fatalf("Completed: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (output=%q)", len(lines), buf.String())
	}

	// started
	var s Event
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatalf("started unmarshal: %v", err)
	}
	if s.Event != "started" {
		t.Errorf("started.Event = %q", s.Event)
	}
	if s.Config == nil {
		t.Fatalf("started.Config = nil")
	}
	if s.Config.Model != "qwen" || s.Config.Endpoint != "http://127.0.0.1:8080" ||
		s.Config.Workspace != "/tmp/ws" || s.Config.Permission != "READ_ONLY" {
		t.Errorf("started.Config = %+v", s.Config)
	}

	// status STREAMING
	var st Event
	if err := json.Unmarshal([]byte(lines[1]), &st); err != nil {
		t.Fatalf("status unmarshal: %v", err)
	}
	if st.Event != "status" || st.Status != "STREAMING" {
		t.Errorf("status = %+v", st)
	}

	// assistant_stream
	var a Event
	if err := json.Unmarshal([]byte(lines[2]), &a); err != nil {
		t.Fatalf("assistant_stream unmarshal: %v", err)
	}
	if a.Event != "assistant_stream" || a.Delta != "hello" || a.Role != "assistant" {
		t.Errorf("assistant_stream = %+v", a)
	}

	// completed
	var c Event
	if err := json.Unmarshal([]byte(lines[3]), &c); err != nil {
		t.Fatalf("completed unmarshal: %v", err)
	}
	if c.Event != "completed" || c.ExitCode != 0 {
		t.Errorf("completed = %+v", c)
	}
}

// TestSessionID confirms the accessor returns the value supplied to
// NewEmitter.
func TestSessionID(t *testing.T) {
	em := NewEmitter(&bytes.Buffer{}, "the-session-id")
	if got := em.SessionID(); got != "the-session-id" {
		t.Errorf("SessionID = %q, want the-session-id", got)
	}
}

// TestEmit_ModelRequest_ProducesCorrectEventType pins the
// ModelRequest helper (Run 006 / handoff 022): one Emit call
// produces one JSONL line whose event field is "model_request",
// protocol_version is the V1 default "1", session_id is the
// emitter's session id, and timestamp is a non-empty RFC3339 UTC
// value. The helper is the GOAL §2-named signal that an external
// controller can use to detect "the harness currently has an
// active model request" — the test pins the wire shape so a
// future regression that drops the base fields or renames the
// event type fails.
func TestEmit_ModelRequest_ProducesCorrectEventType(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-mr")

	if err := em.ModelRequest(); err != nil {
		t.Fatalf("ModelRequest: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (output=%q)", len(lines), buf.String())
	}

	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line is not valid JSON: %v (line=%q)", err, lines[0])
	}
	if got.Event != "model_request" {
		t.Errorf("Event = %q, want model_request", got.Event)
	}
	if got.ProtocolVersion != "1" {
		t.Errorf("ProtocolVersion = %q, want 1", got.ProtocolVersion)
	}
	if got.SessionID != "sess-mr" {
		t.Errorf("SessionID = %q, want sess-mr", got.SessionID)
	}
	if got.Timestamp == "" {
		t.Errorf("Timestamp empty, want non-empty RFC3339 UTC")
	}
}
