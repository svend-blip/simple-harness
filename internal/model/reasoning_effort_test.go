package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func captureBody(t *testing.T, opts Options) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	opts.BaseURL = srv.URL
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}
	c := NewClient(opts)
	if err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(ev StreamEvent) error { return nil }); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	return body
}

// The OpenAI-compatible `reasoning_effort` field is sent only when set.
func TestReasoningEffortSentWhenSet(t *testing.T) {
	body := captureBody(t, Options{Model: "qwen3.8-max", MaxOutputTokens: 1024, ReasoningEffort: "low"})
	if got := body["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got)
	}
}

func TestReasoningEffortOmittedWhenEmpty(t *testing.T) {
	body := captureBody(t, Options{Model: "qwen3.8-max", MaxOutputTokens: 1024})
	if _, present := body["reasoning_effort"]; present {
		t.Fatal("reasoning_effort must be omitted when unset")
	}
}
