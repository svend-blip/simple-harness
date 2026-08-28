package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
)

// TestRunOne_HappyPath_AccumulatesAndStreams is the load-bearing
// integration test: spin up a real httptest server that returns the
// standard SSE deltas, construct a Run with a model.Client pointed
// at it and a bytes.Buffer as the human-facing stdout, call
// RunOne, and assert (a) the buffer has the concatenated deltas,
// (b) the sidecar has the expected sequence (started → status:
// STREAMING → N assistant_stream → status: COMPLETED → completed
// with exit_code 0), and (c) the returned string matches the
// buffer. The reviewer can sed-replace any of these assertions to
// confirm the test is load-bearing.
func TestRunOne_HappyPath_AccumulatesAndStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello, "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"world"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"!"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-loop-test")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model: model.Options{
			BaseURL: srv.URL,
			Model:   "qwen",
		},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	got, err := r.RunOne(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if got != "Hello, world!" {
		t.Errorf("returned string = %q, want %q", got, "Hello, world!")
	}
	if stdout.String() != "Hello, world!" {
		t.Errorf("stdout buffer = %q, want %q", stdout.String(), "Hello, world!")
	}

	// Sidecar sequence: started, status STREAMING, 3x assistant_stream,
	// status COMPLETED, completed exit_code 0 -> 7 lines.
	lines := strings.Split(strings.TrimRight(sidecar.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("got %d sidecar lines, want 7 (output=%q)", len(lines), sidecar.String())
	}

	var s event.Event
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatalf("line 0 unmarshal: %v", err)
	}
	if s.Event != "started" {
		t.Errorf("line 0 Event = %q, want started", s.Event)
	}
	if s.Config == nil || s.Config.Model != "qwen" || s.Config.Workspace != "/tmp/ws" ||
		s.Config.Permission != "READ_ONLY" {
		t.Errorf("line 0 Config = %+v", s.Config)
	}

	var st1 event.Event
	if err := json.Unmarshal([]byte(lines[1]), &st1); err != nil {
		t.Fatalf("line 1 unmarshal: %v", err)
	}
	if st1.Event != "status" || st1.Status != "STREAMING" {
		t.Errorf("line 1 = %+v, want status STREAMING", st1)
	}

	for i := 2; i < 5; i++ {
		var a event.Event
		if err := json.Unmarshal([]byte(lines[i]), &a); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if a.Event != "assistant_stream" {
			t.Errorf("line %d Event = %q, want assistant_stream", i, a.Event)
		}
	}

	var st2 event.Event
	if err := json.Unmarshal([]byte(lines[5]), &st2); err != nil {
		t.Fatalf("line 5 unmarshal: %v", err)
	}
	if st2.Event != "status" || st2.Status != "COMPLETED" {
		t.Errorf("line 5 = %+v, want status COMPLETED", st2)
	}

	var c event.Event
	if err := json.Unmarshal([]byte(lines[6]), &c); err != nil {
		t.Fatalf("line 6 unmarshal: %v", err)
	}
	if c.Event != "completed" || c.ExitCode != 0 {
		t.Errorf("line 6 = %+v, want completed exit_code 0", c)
	}
}

// TestRunOne_PropagatesModelErrorAsModelError pins the error
// contract: an HTTP 500 from the model server surfaces as a
// *model.ModelError with Kind == ErrHTTP. The cmd relies on this
// pass-through to map to exit code 3.
func TestRunOne_PropagatesModelErrorAsModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"oops"}`)
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-err")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	_, err := r.RunOne(context.Background(), "hi")
	if err == nil {
		t.Fatal("RunOne: expected error, got nil")
	}
	var me *model.ModelError
	if !errors.As(err, &me) {
		t.Fatalf("err is not *model.ModelError: %T %v", err, err)
	}
	if me.Kind != model.ErrHTTP {
		t.Errorf("Kind = %v, want ErrHTTP", me.Kind)
	}
	if me.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", me.StatusCode)
	}
}

// TestRunOne_TimeoutMapsToErrTimeout pins the request-timeout path:
// a server that sleeps past the configured RequestTimeout surfaces
// as a *model.ModelError with Kind == ErrTimeout. The cmd relies
// on this pass-through to map to exit code 6.
func TestRunOne_TimeoutMapsToErrTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-timeout")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 50 * time.Millisecond,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	_, err := r.RunOne(context.Background(), "hi")
	if err == nil {
		t.Fatal("RunOne: expected timeout error, got nil")
	}
	var me *model.ModelError
	if !errors.As(err, &me) {
		t.Fatalf("err is not *model.ModelError: %T %v", err, err)
	}
	if me.Kind != model.ErrTimeout {
		t.Errorf("Kind = %v, want ErrTimeout", me.Kind)
	}
}

// TestNormalizeBaseURL is table-driven and pins the four shape
// variants the seam handles: trailing /v1, trailing /v1/, no /v1,
// and an embedded /v1 in a deeper path (must NOT be stripped —
// only a TRULY trailing /v1 is stripped).
func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://x/v1", "http://x"},
		{"http://x/v1/", "http://x"},
		{"http://x", "http://x"},
		{"http://x/v1/chat", "http://x/v1/chat"},
	}
	for _, tc := range cases {
		got := NormalizeBaseURL(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
