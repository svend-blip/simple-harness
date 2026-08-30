package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// recordedSessionRequest captures one request the
// sessionRequiredHTTPServer observed: the JSON-RPC method field and
// the value (or absence) of the Mcp-Session-Id header. The struct is
// test-only (lives in *_test.go) and is NOT referenced from production
// code paths.
type recordedSessionRequest struct {
	Method      string // JSON-RPC method field (e.g., "initialize", "tools/list")
	SessionID   string // value of the Mcp-Session-Id request header ("" if absent)
	HasSession  bool   // true iff the Mcp-Session-Id header was present (non-empty)
	ContentType string // Content-Type header for completeness
}

// sessionRequiredHTTPServer is an in-process httptest server that
// enforces MCP streamable-http session negotiation: every request
// MUST carry the Mcp-Session-Id header EXCEPT the `initialize`
// request (the spec allows `initialize` to be unauthenticated — the
// header is what we're trying to obtain). The server 400s on any
// non-`initialize` request without the header (or with a mismatched
// header); it 200s on `initialize` and assigns a fresh session id
// (a hex-formatted counter) which subsequent requests must echo.
//
// The stub captures the Mcp-Session-Id header on every request in
// the `requests` slice, in arrival order. The `assignedSessionID`
// field holds the session id issued by the most recent `initialize`
// (the test asserts it matches the headers attached by the
// transport's preflight). The `initializeStatus` field controls
// whether the `initialize` handler returns a success (default 200)
// or a failure (any non-2xx — used by the regression check that
// verifies the transport's cached-failure surface).
//
// The stub is test-only (lives in *_test.go). No production code
// path references it. The struct is SEPARATE from stubHTTPServer
// at lines 27-35 — the existing stub is the non-session-required
// baseline (it accepts requests without a session id), and stays
// byte-identical against the Run 020 baseline per amendment 3.
type sessionRequiredHTTPServer struct {
	mu                sync.Mutex
	assignedSessionID string
	initializeStatus  int          // 0 = 200; non-zero overrides the success path
	requests          []recordedSessionRequest
	listings          []ListedTool
	calls             []stubCall
	counter           atomic.Int64
}

// handler implements http.Handler. It dispatches on the JSON-RPC
// method field and enforces the session-required protocol: every
// request except `initialize` MUST carry the Mcp-Session-Id header
// matching the assignedSessionID, or the handler returns 400
// "missing or mismatched Mcp-Session-Id header".
func (s *sessionRequiredHTTPServer) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

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

	sessionHeader := r.Header.Get("Mcp-Session-Id")
	hasSession := sessionHeader != ""

	// Record every request (init, list, call, anything else) for
	// the test's wire-shape assertions.
	s.mu.Lock()
	s.requests = append(s.requests, recordedSessionRequest{
		Method:      req.Method,
		SessionID:   sessionHeader,
		HasSession:  hasSession,
		ContentType: r.Header.Get("Content-Type"),
	})
	assigned := s.assignedSessionID
	initStatus := s.initializeStatus
	listings := append([]ListedTool(nil), s.listings...)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		// The preflight's own `initialize` MUST NOT carry the
		// header (the header is what we're trying to obtain).
		// Assign a fresh session id and return it via the
		// canonical Mcp-Session-Id response header.
		next := s.counter.Add(1)
		sid := fmt.Sprintf("%032x", next)
		s.mu.Lock()
		s.assignedSessionID = sid
		s.mu.Unlock()
		if initStatus != 0 {
			http.Error(w, "initialize forced failure", initStatus)
			return
		}
		w.Header().Set("Mcp-Session-Id", sid)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]interface{}{},
				"serverInfo": map[string]interface{}{
					"name":    "session-required-stub",
					"version": "test",
				},
			},
		})
	default:
		// Any non-`initialize` request MUST carry the
		// Mcp-Session-Id header matching the assigned
		// session id. Anything else (missing, mismatched)
		// returns 400 — the test exercises both shapes.
		if !hasSession || sessionHeader != assigned {
			http.Error(w, "missing or mismatched Mcp-Session-Id header", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "tools/list":
			tools := make([]map[string]interface{}, len(listings))
			for i, lt := range listings {
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
						{"type": "text", "text": "session-stub: " + params.Name},
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
}

// TestMCP_TransportHTTP_SessionRequiredStub is handoff 066's binding
// pin for the streamable-http session-negotiation fix in
// transport_http.go. The pin proves the GENERIC protocol against a
// session-requiring stub server — the Run-019 stubs' blind spot
// (no session negotiation enforcement) must not survive at handoff
// 066 close.
//
// Four sub-assertions:
//
//  1. List succeeds end-to-end against the session-required stub.
//     The transport's `initialize` preflight is invisible to the
//     test; the test inspects the stub's recorded requests to
//     assert the wire shape (entry 0 = initialize, no header;
//     entry 1 = tools/list, header matching assignedSessionID).
//
//  2. Call succeeds with the SAME session id as List — the cached
//     session id is reused across calls (the sync.Once +
//     t.sessionID field work correctly; the stub records the
//     request's Mcp-Session-Id header on entry 2 and the test
//     asserts it matches the header on entry 1).
//
//  3. Sub-call after a pre-flight failure surfaces the error
//     CONSISTENTLY: a fresh stub with initializeStatus=500 returns
//     500 on the initialize; the transport caches the failure in
//     t.sessionErr; a subsequent Call on the SAME transport
//     returns the SAME wrapped error without re-attempting
//     initialize. The sync.Once semantics ensure the failure is
//     reported once, not retried on every call.
//
//  4. Regression check: the existing stubHTTPServer (lines 27-35,
//     byte-identical against the Run 020 baseline) does NOT
//     require Mcp-Session-Id. The transport's preflight succeeds
//     against the non-session-required server (no header returned
//     on initialize), t.sessionID stays empty, and the if-guard
//     in roundtrip skips header attachment on subsequent List +
//     Call requests. The pre-fix wire shape is preserved for
//     non-session-aware servers.
//
// This pin is the binding surface for SCOPE §43 + amendment §44's
// "the integration is proven against it, not modeled on it" +
// amendment 3's "session-REQUIRING stub-server test pin" rule.
func TestMCP_TransportHTTP_SessionRequiredStub(t *testing.T) {
	// ---- (1) List succeeds against session-required stub. ----
	stub := &sessionRequiredHTTPServer{
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
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()

	got, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List(session-required) error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(List) = %d, want 1 (tools: %+v)", len(got), got)
	}
	if got[0].Name != "tool_alpha" {
		t.Fatalf("got[0].Name = %q, want %q", got[0].Name, "tool_alpha")
	}

	stub.mu.Lock()
	assigned := stub.assignedSessionID
	reqs := append([]recordedSessionRequest(nil), stub.requests...)
	stub.mu.Unlock()

	if len(reqs) != 2 {
		t.Fatalf("stub.requests len = %d, want 2 (initialize preflight + tools/list)", len(reqs))
	}
	if reqs[0].Method != "initialize" {
		t.Fatalf("reqs[0].Method = %q, want %q", reqs[0].Method, "initialize")
	}
	if reqs[0].HasSession {
		t.Fatalf("reqs[0].HasSession = true, want false (the initialize preflight MUST NOT carry the session header)")
	}
	if reqs[1].Method != "tools/list" {
		t.Fatalf("reqs[1].Method = %q, want %q", reqs[1].Method, "tools/list")
	}
	if !reqs[1].HasSession {
		t.Fatalf("reqs[1].HasSession = false, want true (tools/list MUST carry the cached session header)")
	}
	if reqs[1].SessionID != assigned {
		t.Fatalf("reqs[1].SessionID = %q, want %q (tools/list header must match the assigned session id from initialize)", reqs[1].SessionID, assigned)
	}
	if assigned == "" {
		t.Fatalf("assigned session id is empty; the stub did not return Mcp-Session-Id on initialize")
	}

	// ---- (2) Call succeeds with the SAME session id as List. ----
	args := map[string]interface{}{"x": float64(1)}
	callOut, err := tr.Call(context.Background(), "tool_alpha", args)
	if err != nil {
		t.Fatalf("Call(session-required) error = %v, want nil", err)
	}
	content, ok := callOut["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("Call result content = %+v, want []interface{} of length 1", callOut["content"])
	}
	first := content[0].(map[string]interface{})
	if first["text"] != "session-stub: tool_alpha" {
		t.Fatalf("Call result content[0].text = %v, want %q", first["text"], "session-stub: tool_alpha")
	}

	stub.mu.Lock()
	reqsAfterCall := append([]recordedSessionRequest(nil), stub.requests...)
	stub.mu.Unlock()
	if len(reqsAfterCall) != 3 {
		t.Fatalf("stub.requests len after Call = %d, want 3 (initialize + tools/list + tools/call)", len(reqsAfterCall))
	}
	if reqsAfterCall[2].Method != "tools/call" {
		t.Fatalf("reqsAfterCall[2].Method = %q, want %q", reqsAfterCall[2].Method, "tools/call")
	}
	if !reqsAfterCall[2].HasSession {
		t.Fatalf("reqsAfterCall[2].HasSession = false, want true (tools/call MUST carry the cached session header)")
	}
	if reqsAfterCall[2].SessionID != reqsAfterCall[1].SessionID {
		t.Fatalf("reqsAfterCall[2].SessionID = %q, want %q (cached session id must be reused across calls)", reqsAfterCall[2].SessionID, reqsAfterCall[1].SessionID)
	}

	// ---- (3) Sub-call after a pre-flight failure surfaces the
	//         error CONSISTENTLY (sync.Once caches the failure). ----
	brokenStub := &sessionRequiredHTTPServer{initializeStatus: http.StatusInternalServerError}
	brokenSrv := httptest.NewServer(http.HandlerFunc(brokenStub.handler))
	defer brokenSrv.Close()

	brokenTr := NewHTTPTransport(brokenSrv.URL)
	defer brokenTr.Close()

	if _, err := brokenTr.List(context.Background()); err == nil {
		t.Fatalf("List(broken initialize) error = nil, want error (500 on initialize must surface)")
	} else if !strings.Contains(err.Error(), "500") {
		t.Fatalf("List(broken initialize) error = %q, want error mentioning 500", err.Error())
	}
	// Subsequent Call on the SAME transport must return the SAME
	// cached error — the sync.Once caches the failure in
	// t.sessionErr; the second call does NOT re-attempt initialize.
	if _, err := brokenTr.Call(context.Background(), "tool_alpha", args); err == nil {
		t.Fatalf("Call(broken preflight, second call) error = nil, want cached error")
	} else if !strings.Contains(err.Error(), "500") {
		t.Fatalf("Call(broken preflight, second call) error = %q, want error mentioning 500 (the cached failure must surface consistently)", err.Error())
	}

	// ---- (4) Regression check: existing stubHTTPServer (non-
	//         session-required) continues to work; the transport
	//         does NOT attach Mcp-Session-Id to its requests. ----
	nonSessionStub := &stubHTTPServer{
		listings: []ListedTool{
			{Name: "tool_alpha", Description: "alpha tool"},
		},
	}
	nonSessionSrv := httptest.NewServer(http.HandlerFunc(nonSessionStub.handler))
	defer nonSessionSrv.Close()

	nonSessionTr := NewHTTPTransport(nonSessionSrv.URL)
	defer nonSessionTr.Close()

	if _, err := nonSessionTr.List(context.Background()); err != nil {
		t.Fatalf("List(non-session-required) error = %v, want nil", err)
	}
	if _, err := nonSessionTr.Call(context.Background(), "tool_alpha", args); err != nil {
		t.Fatalf("Call(non-session-required) error = %v, want nil", err)
	}
	// The transport's session id cache must be empty against the
	// non-session-required server (the stub does not return
	// Mcp-Session-Id on initialize, so the header attachment
	// guard in roundtrip skips the header). The pre-fix wire
	// shape is preserved.
	if nonSessionTr.sessionID != "" {
		t.Fatalf("non-session-required transport sessionID = %q, want empty (regression: non-session-aware servers must NOT cache a session id)", nonSessionTr.sessionID)
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
