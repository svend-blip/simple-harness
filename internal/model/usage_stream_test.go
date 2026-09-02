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

// stream_options.include_usage is always requested (2026-09-02), and a
// usage chunk with completion_tokens_details reaches onDelta with the
// reasoning split readable through Usage.ReasoningTokens().
func TestStreamRequestsUsageAndParsesReasoningSplit(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":68,"completion_tokens":91,"total_tokens":159,"completion_tokens_details":{"reasoning_tokens":80}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen3.8-max", MaxOutputTokens: 512, RequestTimeout: 5 * time.Second})
	var got *Usage
	err := c.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, func(ev StreamEvent) error {
		if ev.Usage != nil {
			got = ev.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	so, ok := body["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options.include_usage missing from request body: %v", body["stream_options"])
	}
	if got == nil {
		t.Fatal("usage chunk not delivered")
	}
	if got.CompletionTokens != 91 || got.ReasoningTokens() != 80 {
		t.Fatalf("usage = %+v (reasoning %d), want completion 91 / reasoning 80", got, got.ReasoningTokens())
	}
	var none *Usage
	if none.ReasoningTokens() != 0 {
		t.Fatal("nil usage must read as 0 reasoning tokens")
	}
}
