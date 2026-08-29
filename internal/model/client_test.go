package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// TestRequestShape pins the outgoing wire shape against a real
// httptest.Server: URL path, Content-Type, Authorization, body
// model/messages/temperature/max_tokens/stream=true. The reviewer
// (per GOAL §5 #2) can mutate the captured wire shape in a
// temporary test variant and confirm the assertion fires — this is
// the "auditable from the source" surface for the request shape.
func TestRequestShape(t *testing.T) {
	var gotReq struct {
		URL     string
		Headers http.Header
		Body    struct {
			Model       string    `json:"model"`
			Messages    []Message `json:"messages"`
			Temperature float64   `json:"temperature"`
			MaxTokens   int       `json:"max_tokens"`
			Stream      bool      `json:"stream"`
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq.URL = r.URL.Path
		gotReq.Headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotReq.Body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(Options{
		BaseURL:         srv.URL,
		Model:           "qwen",
		APIKey:          "sk-test",
		Temperature:     0.2,
		MaxOutputTokens: 8192,
		RequestTimeout:  5 * time.Second,
	})
	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(ev StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if gotReq.URL != "/v1/chat/completions" {
		t.Errorf("URL path = %q, want /v1/chat/completions", gotReq.URL)
	}
	if got := gotReq.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := gotReq.Headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
	if !gotReq.Body.Stream {
		t.Error("stream: true missing from body")
	}
	if gotReq.Body.Model != "qwen" {
		t.Errorf("model = %q, want qwen", gotReq.Body.Model)
	}
	if len(gotReq.Body.Messages) != 1 || gotReq.Body.Messages[0].Content != "hi" {
		t.Errorf("messages not echoed: %+v", gotReq.Body.Messages)
	}
}

// TestAPIKeyBearerHeader pins that an empty APIKey produces no
// Authorization header. TestRequestShape covers the APIKey-set
// case.
func TestAPIKeyBearerHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen"})
	_ = c.ChatStream(context.Background(), ChatRequest{}, func(StreamEvent) error { return nil })
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}

// TestStreamParsing_TextDeltas — three text deltas then [DONE].
func TestStreamParsing_TextDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello, "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"world"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"!"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	var got []string
	err := c.ChatStream(context.Background(), ChatRequest{}, func(ev StreamEvent) error {
		if ev.Delta != "" {
			got = append(got, ev.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	want := []string{"Hello, ", "world", "!"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deltas = %q, want %q", got, want)
	}
}

// TestStreamParsing_FinishReason — final event carries finish_reason.
func TestStreamParsing_FinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	var finish string
	_ = c.ChatStream(context.Background(), ChatRequest{}, func(ev StreamEvent) error {
		if ev.FinishReason != "" {
			finish = ev.FinishReason
		}
		return nil
	})
	if finish != "stop" {
		t.Errorf("finish_reason = %q, want stop", finish)
	}
}

// TestStreamParsing_ToolCallDelta — a delta with a partial tool_call.
func TestStreamParsing_ToolCallDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":\"/tmp/x"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	var sawTool bool
	_ = c.ChatStream(context.Background(), ChatRequest{}, func(ev StreamEvent) error {
		if ev.ToolCallDelta != nil {
			sawTool = true
		}
		return nil
	})
	if !sawTool {
		t.Error("no tool_call delta seen")
	}
}

// TestStreamParsing_UpstreamError — server emits a JSON error event.
func TestStreamParsing_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"error":{"code":"context_length_exceeded","message":"too long"}}`+"\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	err := c.ChatStream(context.Background(), ChatRequest{}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var me *ModelError
	if !errors.As(err, &me) {
		t.Fatalf("err is not *ModelError: %T %v", err, err)
	}
	if me.Kind != ErrUpstream {
		t.Errorf("Kind = %v, want ErrUpstream", me.Kind)
	}
	if me.Code != "context_length_exceeded" {
		t.Errorf("Code = %q, want context_length_exceeded", me.Code)
	}
}

// TestStreamParsing_MalformedJSON — server emits unparseable data.
func TestStreamParsing_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: not-json\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	err := c.ChatStream(context.Background(), ChatRequest{}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var me *ModelError
	if !errors.As(err, &me) || me.Kind != ErrParse {
		t.Errorf("Kind = %v, want ErrParse (err=%v)", me, err)
	}
}

// TestHTTPError — server returns 500.
func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"oops"}`)
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	err := c.ChatStream(context.Background(), ChatRequest{}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var me *ModelError
	if !errors.As(err, &me) || me.Kind != ErrHTTP {
		t.Errorf("Kind = %v, want ErrHTTP (err=%v)", me, err)
	}
	if me.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", me.StatusCode)
	}
}

// TestRequestTimeout — server sleeps past the configured timeout.
func TestRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := NewClient(Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 50 * time.Millisecond})
	err := c.ChatStream(context.Background(), ChatRequest{}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var me *ModelError
	if !errors.As(err, &me) || me.Kind != ErrTimeout {
		t.Errorf("Kind = %v, want ErrTimeout (err=%v)", me, err)
	}
}

// TestParseToolCallArgs_HappyPath pins the SCOPE §31
// structured-rejection discipline (Run 017 / handoff 041):
// a complete JSON object parses into a map[string]any
// with the expected keys. The binding pin is the
// delta-assembly seam's GOAL §2 deliverable 3 contract.
func TestParseToolCallArgs_HappyPath(t *testing.T) {
	argsJSON := `{"path":"/tmp/x","patch":"@@ -1 +1 @@\n-old\n+new\n"}`
	got, err := ParseToolCallArgs(argsJSON)
	if err != nil {
		t.Fatalf("ParseToolCallArgs: %v", err)
	}
	if got["path"] != "/tmp/x" {
		t.Errorf("args[path] = %v, want /tmp/x", got["path"])
	}
	if got["patch"] == nil {
		t.Errorf("args[patch] missing")
	}
}

// TestParseToolCallArgs_MalformedJSON_ReturnsSyntaxError pins
// the SCOPE §31 contract: a truncated JSON object surfaces
// as a *json.SyntaxError (the caller appends a tool-result
// message with status="error" so the model gets to retry).
func TestParseToolCallArgs_MalformedJSON_ReturnsSyntaxError(t *testing.T) {
	_, err := ParseToolCallArgs(`{"path":"/tmp/x"`) // truncated
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		t.Errorf("err is not *json.SyntaxError: %T %v", err, err)
	}
}

// TestAccumulateToolCallFragment_MergesArgs exercises the
// per-fragment merge end-to-end: the first fragment
// initializes the ToolCall with Name + partial args; the
// second fragment merges additional args into the same
// ToolCall. The resulting Arguments map has both keys.
func TestAccumulateToolCallFragment_MergesArgs(t *testing.T) {
	accum := map[int]*ToolCall{}
	// Fragment 1: first half — name + partial path.
	if err := AccumulateToolCallFragment(accum, &ToolCallFragment{
		Index:     0,
		ID:        "call_1",
		Name:      "apply_patch",
		ArgsDelta: `{"path":"/tmp/x"}`,
	}); err != nil {
		t.Fatalf("first fragment: %v", err)
	}
	// Fragment 2: closing half — patch only.
	if err := AccumulateToolCallFragment(accum, &ToolCallFragment{
		Index:     0,
		ArgsDelta: `{"patch":"@@ -1 +1 @@\n-old\n+new\n"}`,
	}); err != nil {
		t.Fatalf("second fragment: %v", err)
	}
	call, ok := accum[0]
	if !ok {
		t.Fatal("no tool call at index 0")
	}
	if call.Name != "apply_patch" {
		t.Errorf("Name = %q, want apply_patch", call.Name)
	}
	if call.Arguments["path"] != "/tmp/x" {
		t.Errorf("Arguments[path] = %v, want /tmp/x", call.Arguments["path"])
	}
	if call.Arguments["patch"] == nil {
		t.Errorf("Arguments[patch] missing")
	}
}
