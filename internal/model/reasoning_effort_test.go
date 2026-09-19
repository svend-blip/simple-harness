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

func TestThinkingControlsSentOnlyWhenSet(t *testing.T) {
	off := false
	body := captureBody(t, Options{Model: "qwen3.8-max", MaxOutputTokens: 1024, EnableThinking: &off, ThinkingBudget: 512})
	if body["enable_thinking"] != false || body["thinking_budget"] != float64(512) {
		t.Fatalf("thinking fields = %v / %v", body["enable_thinking"], body["thinking_budget"])
	}
	body = captureBody(t, Options{Model: "qwen3.8-max", MaxOutputTokens: 1024})
	if _, ok := body["enable_thinking"]; ok {
		t.Fatal("enable_thinking must be omitted when unset")
	}
	if _, ok := body["thinking_budget"]; ok {
		t.Fatal("thinking_budget must be omitted when unset")
	}
}

// A request may carry its own output cap: a compaction summary has a
// size it is meant to stay under, and the configured max_tokens (8192 by
// default) let one run to 1 424 tokens and 35 s. The smaller of the two
// goes on the wire; a request without one changes nothing.
func TestARequestsOwnOutputCapIsSentWhenItIsTheSmaller(t *testing.T) {
	send := func(opts Options, req ChatRequest) float64 {
		t.Helper()
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		defer srv.Close()
		opts.BaseURL, opts.RequestTimeout = srv.URL, 5*time.Second
		req.Messages = []Message{{Role: "user", Content: "hi"}}
		if err := NewClient(opts).ChatStream(context.Background(), req,
			func(StreamEvent) error { return nil }); err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		if _, leaked := body["MaxTokens"]; leaked {
			t.Errorf("the request's own field leaked onto the wire: %v", body)
		}
		got, _ := body["max_tokens"].(float64)
		return got
	}
	for _, tc := range []struct {
		name            string
		configured, req int
		want            float64
	}{
		{"no cap of its own", 8192, 0, 8192},
		{"its own is smaller", 8192, 1024, 1024},
		{"its own is larger", 512, 1024, 512},
		{"nothing configured", 0, 1024, 1024},
	} {
		if got := send(Options{Model: "m", MaxOutputTokens: tc.configured}, ChatRequest{MaxTokens: tc.req}); got != tc.want {
			t.Errorf("%s: max_tokens = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A request may set its own reasoning effort. Measured on Ollama with
// qwen3.6-27b: a 57-word summary took 28.3 s and 2 058 output tokens
// with the model's default reasoning, and 1.4 s and 71 tokens with
// reasoning_effort "none". A compaction is such a request; the working
// turns keep whatever the client was configured with.
func TestARequestsOwnReasoningEffortOverridesTheConfigured(t *testing.T) {
	send := func(configured, own string) any {
		t.Helper()
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		defer srv.Close()
		c := NewClient(Options{BaseURL: srv.URL, Model: "m", ReasoningEffort: configured, RequestTimeout: 5 * time.Second})
		if err := c.ChatStream(context.Background(), ChatRequest{
			Messages: []Message{{Role: "user", Content: "hi"}}, ReasoningEffort: own,
		}, func(StreamEvent) error { return nil }); err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		return body["reasoning_effort"]
	}
	if got := send("high", "none"); got != "none" {
		t.Errorf("own effort: reasoning_effort = %v, want none", got)
	}
	if got := send("high", ""); got != "high" {
		t.Errorf("no own effort: reasoning_effort = %v, want the configured high", got)
	}
	if got := send("", ""); got != nil {
		t.Errorf("neither: reasoning_effort = %v, want it absent", got)
	}
}
