package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubHTTPServer is an in-process httptest server that speaks
// JSON-RPC 2.0 over HTTP. The handler dispatches on `method`:
//
//   - "tools/list" → returns a canned {"tools":[...]} response.
//   - "tools/call" → records the call (name + args) and returns a
//     canned {"content":[...]} response, or a JSON-RPC error when
//     the request URL has ?fail=1.
//
// The stub captures the last request body + headers so the tests
// can assert the wire shape. Captures are guarded by a mutex.
//
// The stub is test-only (lives in *_test.go). No production code
// path references it.
type stubHTTPServer struct {
	mu       sync.Mutex
	lastBody string
	lastCT   string
	lastAcc  string
	listings []ListedTool
	calls    []stubCall
	failNext bool
}

// handler implements http.Handler. It reads the JSON-RPC request,
// dispatches on the `method` field, and writes a response. The
// `failNext` flag forces a JSON-RPC error response (the
// TestMCP_TransportHTTP_StubCall error-mode case).
func (s *stubHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.lastBody = string(body)
	s.lastCT = r.Header.Get("Content-Type")
	s.lastAcc = r.Header.Get("Accept")
	fail := s.failNext
	s.failNext = false
	s.mu.Unlock()

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if fail {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "method disabled by stub",
			},
		})
		return
	}

	switch req.Method {
	case "tools/list":
		tools := make([]map[string]interface{}, len(s.listings))
		for i, lt := range s.listings {
			tools[i] = map[string]interface{}{
				"name":        lt.Name,
				"description": lt.Description,
				"inputSchema": lt.InputSchema,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]interface{}{"tools": tools},
		})
	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		s.mu.Lock()
		s.calls = append(s.calls, stubCall{Name: params.Name, Args: params.Arguments})
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": "stub: " + params.Name},
				},
				"echoed_args": params.Arguments,
			},
		})
	default:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "method not found: " + req.Method,
			},
		})
	}
}

// TestMCP_TransportHTTP_StubListing: the http transport's List
// roundtrips a JSON-RPC tools/list request against an in-process
// httptest server and decodes the result.tools array. Asserts:
//
//   - the listing roundtrip returns the 2 seeded tools with the
//     seeded names + descriptions + inputSchema;
//   - the JSON-RPC request body has the correct shape
//     (jsonrpc: "2.0", id: 1, method: "tools/list");
//   - the request headers carry the spec-mandated Content-Type +
//     Accept values (streamable-http baseline).
//
// This is WORK 2's transport-level contribution to TG3 (binding
// ≥ 4 TestMCP_ pins per the GOAL §2 bound decision 7 bar).
func TestMCP_TransportHTTP_StubListing(t *testing.T) {
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{
				Name:        "tool_alpha",
				Description: "alpha tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string"},
					},
				},
			},
			{
				Name:        "tool_beta",
				Description: "beta tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"count": map[string]interface{}{"type": "integer"},
					},
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()

	got, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2 (tools: %+v)", len(got), got)
	}
	if got[0].Name != "tool_alpha" || got[0].Description != "alpha tool" {
		t.Fatalf("got[0] = %+v, want Name=tool_alpha Description=alpha tool", got[0])
	}
	if got[1].Name != "tool_beta" || got[1].Description != "beta tool" {
		t.Fatalf("got[1] = %+v, want Name=tool_beta Description=beta tool", got[1])
	}
	if _, ok := got[0].InputSchema["properties"]; !ok {
		t.Fatalf("got[0].InputSchema missing properties; got %+v", got[0].InputSchema)
	}

	// Inspect the captured request body for the JSON-RPC shape.
	stub.mu.Lock()
	body := stub.lastBody
	ct := stub.lastCT
	acc := stub.lastAcc
	stub.mu.Unlock()

	var req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal last body %q: %v", body, err)
	}
	if req.JSONRPC != "2.0" {
		t.Fatalf("req.jsonrpc = %q, want %q", req.JSONRPC, "2.0")
	}
	if req.ID != 1 {
		t.Fatalf("req.id = %d, want 1", req.ID)
	}
	if req.Method != "tools/list" {
		t.Fatalf("req.method = %q, want %q", req.Method, "tools/list")
	}

	// Headers: streamable-http baseline per the spec.
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json...", ct)
	}
	if !strings.Contains(acc, "application/json") {
		t.Fatalf("Accept = %q, want to contain application/json", acc)
	}
	if !strings.Contains(acc, "text/event-stream") {
		t.Fatalf("Accept = %q, want to contain text/event-stream (streamable-http baseline)", acc)
	}
}

// TestMCP_TransportHTTP_StubCall: the http transport's Call
// roundtrips a JSON-RPC tools/call request against an in-process
// httptest server. Three sub-assertions:
//
//   - happy path: the call roundtrip returns the canned content
//     from the stub; the JSON-RPC request body carries the tool
//     name + args verbatim;
//   - JSON-RPC error path: when the stub server returns a JSON-RPC
//     error object, Call surfaces it as a wrapped error
//     (per GOAL §2 bound decision 4 — transport failures are
//     structured tool failures, never harness crashes).
func TestMCP_TransportHTTP_StubCall(t *testing.T) {
	stub := &stubHTTPServer{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()

	// Happy-path call.
	args := map[string]interface{}{"x": float64(1), "name": "alpha"}
	got, err := tr.Call(context.Background(), "tool_alpha", args)
	if err != nil {
		t.Fatalf("Call(happy) error = %v, want nil", err)
	}
	content, ok := got["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("Call result content = %+v, want []interface{} of length 1", got["content"])
	}
	first, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Call result content[0] = %T, want map[string]interface{}", content[0])
	}
	if first["text"] != "stub: tool_alpha" {
		t.Fatalf("Call result content[0].text = %v, want %q", first["text"], "stub: tool_alpha")
	}
	echoed, ok := got["echoed_args"].(map[string]interface{})
	if !ok {
		t.Fatalf("Call result echoed_args = %T, want map[string]interface{}", got["echoed_args"])
	}
	if echoed["name"] != "alpha" {
		t.Fatalf("Call result echoed_args[name] = %v, want %q", echoed["name"], "alpha")
	}

	// Inspect the captured request body for the JSON-RPC shape.
	stub.mu.Lock()
	body := stub.lastBody
	calls := append([]stubCall(nil), stub.calls...)
	stub.mu.Unlock()

	var req struct {
		JSONRPC string                 `json:"jsonrpc"`
		ID      int                    `json:"id"`
		Method  string                 `json:"method"`
		Params  map[string]interface{} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal last body %q: %v", body, err)
	}
	if req.JSONRPC != "2.0" {
		t.Fatalf("req.jsonrpc = %q, want %q", req.JSONRPC, "2.0")
	}
	if req.Method != "tools/call" {
		t.Fatalf("req.method = %q, want %q", req.Method, "tools/call")
	}
	if req.Params["name"] != "tool_alpha" {
		t.Fatalf("req.params.name = %v, want %q", req.Params["name"], "tool_alpha")
	}
	if req.Params["arguments"].(map[string]interface{})["name"] != "alpha" {
		t.Fatalf("req.params.arguments.name = %v, want %q",
			req.Params["arguments"].(map[string]interface{})["name"], "alpha")
	}

	if len(calls) != 1 {
		t.Fatalf("stub.calls len = %d, want 1", len(calls))
	}
	if calls[0].Name != "tool_alpha" {
		t.Fatalf("stub.calls[0].Name = %q, want %q", calls[0].Name, "tool_alpha")
	}

	// JSON-RPC error path: arm the stub to fail the next call,
	// verify Call surfaces the wrapped error.
	stub.mu.Lock()
	stub.failNext = true
	stub.mu.Unlock()
	if _, err := tr.Call(context.Background(), "tool_alpha", map[string]interface{}{}); err == nil {
		t.Fatalf("Call(fail) error = nil, want error (JSON-RPC error object)")
	} else if !strings.Contains(err.Error(), "jsonrpc error") {
		t.Fatalf("Call(fail) error = %q, want error mentioning jsonrpc error", err.Error())
	}
}
