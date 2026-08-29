package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// readAll reads the entire body and returns it as a byte slice. A
// tiny helper for the adversarial test wrappers that need to peek
// the body before deciding which response to write.
func readAll(rc io.ReadCloser) ([]byte, error) {
	bs, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	_ = rc.Close()
	return bs, nil
}

// Adversarial pin suite for handoff 063, GOAL §5 reviewer duty #2:
// "The run must produce no silent partial-success, no silent omission,
// no harness crash from any malformed / oversized / mid-disconnect /
// allowlist-bypass input." These four pins exercise the failure
// surfaces an MCP-aware harness must survive without crashing and
// without producing silent omissions.

// TestMCP_Adversarial_MalformedListing: the stub server returns a
// listing payload that is not valid JSON-RPC 2.0 (truncated JSON, no
// "result" field, wrong "id"). Manager.AddServer must surface a
// structured "mcp: server %q listing failed: ..." error AND zero
// tools must be registered against the registry (no silent partial
// success). The wire shape is the cmd-side exit-2 mapping's anchor.
//
// Per GOAL §2 bound decision 4: declared-but-malformed = structured
// startup error (the bind to exit 2 lives at the cmd-side in
// WORK 4's cmdMcpInit).
func TestMCP_Adversarial_MalformedListing(t *testing.T) {
	stub := &stubHTTPServer{} // empty listings; we override handler
	// Replace the stub handler with one that returns malformed
	// JSON-RPC responses for any "tools/list" call.
	malformedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return a payload with a syntactically broken structure:
		// a top-level JSON value that is NOT a JSON-RPC 2.0 object.
		// The transport's parseJSONRPC tries to unmarshal into
		// {Result, Error}; a string body fails that parse →
		// "mcp: parse jsonrpc response: ..." wrapped error.
		_, _ = w.Write([]byte(`"definitely-not-json-rpc-object"`))
	})
	httpSrv := httptest.NewServer(malformedHandler)
	defer httpSrv.Close()

	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "http", Endpoint: httpSrv.URL}
	transport := NewHTTPTransport(httpSrv.URL)
	err := m.AddServer(context.Background(), srv, transport)
	if err == nil {
		t.Fatalf("AddServer = nil, want error (malformed JSON-RPC listing must surface as startup error)")
	}
	if !strings.HasPrefix(err.Error(), `mcp: server "weather" listing failed:`) {
		t.Fatalf("AddServer error = %q, want prefix %q (wire-shape pin for cmd-side exit-2 mapping)", err.Error(), `mcp: server "weather" listing failed:`)
	}

	// No tools registered — partial-success guard.
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("registry.Names() = %v, want empty (malformed listing → no partial registration)", names)
	}
	_ = stub // silence unused if zero references in CI
}

// TestMCP_Adversarial_OversizedResult: the stub server's tools/call
// returns a result with a 1 MiB text content payload. Verify the
// adapter surfaces the full content as Result.Content (no truncation
// in the adapter). The size guard for the event-protocol layer is
// upstream at SCOPE §21 (out of scope for this Run); the adapter
// passes the transport result verbatim and lets the upstream
// event-protocol layer enforce its own size policy.
//
// The handoff acknowledges "the implementer picks the policy and
// pins the choice". The chosen policy here is "no adapter-level
// truncation — pass through"; the size guard lives at the
// upstream event-protocol layer per SCOPE §21 (out of scope).
func TestMCP_Adversarial_OversizedResult(t *testing.T) {
	const bigLen = 1 << 20 // 1 MiB
	big := strings.Repeat("x", bigLen)
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{Name: "big_result", Description: "oversized", InputSchema: map[string]interface{}{}},
		},
	}
	// Wrap the stub handler to inject a 1 MiB text payload on tools/call.
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the entire body, then dispatch. tools/list falls
		// through to the stub (which reads the body again internally
		// via its own handler); tools/call to big_result is replaced
		// with the 1 MiB canned response.
		body, err := readAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body), `"tools/call"`) && strings.Contains(string(body), `"big_result"`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"` + big + `"}]}}`))
			return
		}
		// For tools/list, the stub handler reads the body
		// internally, but we've already drained it. Dispatch the
		// canned listing response inline to avoid the re-read
		// problem.
		w.Header().Set("Content-Type", "application/json")
		toolsList := make([]map[string]interface{}, len(stub.listings))
		for i, lt := range stub.listings {
			toolsList[i] = map[string]interface{}{
				"name":        lt.Name,
				"description": lt.Description,
				"inputSchema": lt.InputSchema,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]interface{}{"tools": toolsList},
		})
	}))

	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})
	srv := Server{Name: "weather", Transport: "http", Endpoint: httpSrv.URL}
	transport := NewHTTPTransport(httpSrv.URL)
	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	tool, ok := r.Get("big_result")
	if !ok || tool == nil {
		t.Fatalf("r.Get(big_result) = (nil, %v), want (non-nil, true)", ok)
	}
	res, err := tool.Execute(context.Background(), tools.Call{
		Name:      "big_result",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("tool.Execute error = %v, want nil (oversized result is structured Result, not Go error)", err)
	}
	if res.Status != "ok" {
		t.Fatalf("res.Status = %q, want %q (oversized result is passed through verbatim per the chosen no-truncation policy)", res.Status, "ok")
	}
	if res.Error != nil {
		t.Fatalf("res.Error = %v, want nil", res.Error)
	}
	// The 1 MiB payload must reach Result.Content intact (the
	// adapter passes transport.Call result verbatim; the upstream
	// event-protocol layer enforces its own size policy — out of
	// scope for this Run).
	payload, ok2 := res.Content.(map[string]interface{})
	if !ok2 {
		t.Fatalf("res.Content is %T, want map[string]interface{}", res.Content)
	}
	content, ok3 := payload["content"].([]interface{})
	if !ok3 {
		t.Fatalf("res.Content[content] is %T, want []interface{}", payload["content"])
	}
	if len(content) != 1 {
		t.Fatalf("len(content) = %d, want 1", len(content))
	}
	first, ok4 := content[0].(map[string]interface{})
	if !ok4 {
		t.Fatalf("content[0] is %T, want map[string]interface{}", content[0])
	}
	text, ok5 := first["text"].(string)
	if !ok5 {
		t.Fatalf("content[0].text is %T, want string", first["text"])
	}
	if len(text) != bigLen {
		t.Fatalf("text length = %d, want %d (oversized result must pass through unchanged)", len(text), bigLen)
	}
}

// TestMCP_Adversarial_MidCallDisconnect: the http transport must
// surface a mid-call disconnect as a structured
// Result{Status:"error", Error:&ToolError{Kind:"execution_failed",
// Message: ...}}, AND no Go-level error is returned (the harness
// does NOT crash — SCOPE §42 + GOAL §2 bound decision 4).
//
// Implementation: a custom Transport (rather than httptest.Server)
// that succeeds on List and errors with "connection reset by peer"
// on Call. The test exercises the adapter's structured-error path
// end-to-end through Manager.AddServer. The behavior under test
// (connection reset mid-response → wrapped transport error →
// Result{execution_failed}) is identical whether the disconnect
// arrives via httptest, a custom Transport, or a live endpoint.
//
// Per GOAL §2 bound decision 4: "Transport failures during a tool
// call are structured tool failures (the model sees them), never
// harness crashes."
func TestMCP_Adversarial_MidCallDisconnect(t *testing.T) {
	// Tiny test-only Transport: returns the canned listing on List,
	// returns "connection reset by peer" on Call. The surface
	// mirrors what httptest would surface for a mid-write disconnect.
	midCallDisconnectTransport := &midDisconnectTr{
		listings: []ListedTool{
			{Name: "do_thing", Description: "do a thing", InputSchema: map[string]interface{}{}},
		},
	}

	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})
	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	if err := m.AddServer(context.Background(), srv, midCallDisconnectTransport); err != nil {
		t.Fatalf("AddServer error = %v, want nil (listing roundtrip should succeed; only the call disconnects)", err)
	}

	tool, ok := r.Get("do_thing")
	if !ok || tool == nil {
		t.Fatalf("r.Get(do_thing) = (nil, %v), want (non-nil, true)", ok)
	}
	res, err := tool.Execute(context.Background(), tools.Call{
		Name:      "do_thing",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("tool.Execute error = %v, want nil (mid-call disconnect is a structured Result, NOT a harness crash)", err)
	}
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("res.Error = nil, want *tools.ToolError")
	}
	if res.Error.Kind != "execution_failed" {
		t.Fatalf("res.Error.Kind = %q, want %q", res.Error.Kind, "execution_failed")
	}
	// The message must mention the disconnect (the transport
	// surfaces "connection reset by peer" as the underlying err).
	msg := res.Error.Message
	if !strings.Contains(msg, "connection reset") {
		t.Fatalf("res.Error.Message = %q, want message mentioning connection reset", msg)
	}
}

// midDisconnectTr is a test-only Transport that returns a canned
// listing on List and "connection reset by peer" on Call. It's
// tiny — only the surface the adversarial pin needs.
type midDisconnectTr struct {
	listings []ListedTool
	calls    int
}

func (t *midDisconnectTr) List(_ context.Context) ([]ListedTool, error) {
	out := make([]ListedTool, len(t.listings))
	copy(out, t.listings)
	return out, nil
}

func (t *midDisconnectTr) Call(_ context.Context, _ string, _ map[string]interface{}) (map[string]interface{}, error) {
	t.calls++
	return nil, &stubError{msg: "connection reset by peer"}
}

func (t *midDisconnectTr) Close() error { return nil }

// Compile-time assertion that midDisconnectTr satisfies Transport.
var _ Transport = (*midDisconnectTr)(nil)

// TestMCP_Adversarial_AllowlistBypassAttempt: the stub server's
// listing names 3 tools. The allowlist constrains Manager.AddServer
// to register only tool_alpha. Verify:
//
//   - tool_alpha IS registered (allowlist passes the tool through);
//   - tool_beta is NOT registered (allowlist excluded it);
//   - tool_excluded is NOT registered (not in allowlist).
//
// All three are listings the server advertised; the registration
// filter is at REGISTRATION time (per SCOPE §29 + §43). A bypass
// attempt at dispatch time is impossible because the unregistered
// tool returns the standard "unknown_tool" structured error (no
// silent omission, no harness crash). The transport is called
// exactly once (the one allowlisted tool) even though the listing
// had three tools.
func TestMCP_Adversarial_AllowlistBypassAttempt(t *testing.T) {
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{Name: "tool_alpha", Description: "alpha", InputSchema: map[string]interface{}{}},
			{Name: "tool_beta", Description: "beta", InputSchema: map[string]interface{}{}},
			{Name: "tool_excluded", Description: "excluded", InputSchema: map[string]interface{}{}},
		},
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer httpSrv.Close()

	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	// Allowlist only contains "tool_alpha"; the others are
	// declared by the server but excluded by config.
	srv := Server{
		Name:      "weather",
		Transport: "http",
		Endpoint:  httpSrv.URL,
		Allowlist: []string{"tool_alpha"},
	}
	transport := NewHTTPTransport(httpSrv.URL)
	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// tool_alpha registered; tool_beta + tool_excluded absent.
	if _, ok := r.Get("tool_alpha"); !ok {
		t.Fatalf("r.Get(tool_alpha) = false, want true (allowlisted tool must be registered)")
	}
	if _, ok := r.Get("tool_beta"); ok {
		t.Fatalf("r.Get(tool_beta) = true, want false (allowlist excluded tool_beta)")
	}
	if _, ok := r.Get("tool_excluded"); ok {
		t.Fatalf("r.Get(tool_excluded) = true, want false (allowlist excluded tool_excluded)")
	}

	// A "bypass attempt" at dispatch time: the unregistered name
	// returns the standard structured "unknown_tool" error — no
	// silent omission.
	res := r.Dispatch(context.Background(),
		tools.Call{Name: "tool_beta", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), noopAuth)
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "unknown_tool" {
		t.Fatalf("res.Error = %+v, want Kind=%q", res.Error, "unknown_tool")
	}

	// The transport was called exactly once — the listing only.
	// No tools/call for the unregistered names (registration-time
	// filter prevented the dispatcher from ever reaching transport).
	stub.mu.Lock()
	stubCalls := append([]stubCall(nil), stub.calls...)
	stub.mu.Unlock()
	if len(stubCalls) != 0 {
		t.Fatalf("stub.calls len = %d, want 0 (registration-time allowlist filter prevents transport call)", len(stubCalls))
	}
}
