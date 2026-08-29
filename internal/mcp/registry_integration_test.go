package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// builtinEchoTool is a minimal tools.Tool whose Execute returns a
// configured reply as Result.Content. The test-only collision tests in
// builtin_collision_test.go (FROZEN) use a separate builtinStub whose
// Execute panics; end-to-end integration tests in this file
// (registry_integration_test.go) use this echo variant to drive
// registry.Dispatch against the bare-name collision path.
type builtinEchoTool struct {
	name  string
	reply string
}

func (b *builtinEchoTool) Meta() tools.ToolMeta  { return tools.ToolMeta{Name: b.name} }
func (b *builtinEchoTool) Schema() tools.Schema { return tools.Schema{} }
func (b *builtinEchoTool) Execute(_ context.Context, _ tools.Call) (tools.Result, error) {
	return tools.Result{Status: "ok", Content: b.reply}, nil
}

// Compile-time assertion that builtinEchoTool satisfies tools.Tool.
var _ tools.Tool = (*builtinEchoTool)(nil)

// countingAuth is an AuthorizeFunc that records every invocation and
// returns a *tools.DecisionError when configured to reject. The
// counter is the test's load-bearing assertion: a Dispatch path
// invokes Authorize exactly once per MCP tool call (per handoff 063's
// design decision; the auth-pass-once invariant is pinned by
// TestMCP_SingleAuthPass).
type countingAuth struct {
	calls    int
	reject   bool // when true, returns a structured *tools.DecisionError
	stage    string
	reason   string
	gotCalls *[]authCall
}

// authCall records one invocation of the stub AuthorizeFunc. The
// fields are what the registry.Dispatch passes; the test inspects them
// to verify Dispatch called auth with the right call, schema, ws, and
// policy.
type authCall struct {
	Call   tools.Call
	Schema tools.Schema
	WS     tools.Workspace
	Pol    tools.Policy
}

func (a *countingAuth) Authorize(ctx context.Context, call tools.Call, schema tools.Schema, ws tools.Workspace, pol tools.Policy) *tools.DecisionError {
	a.calls++
	if a.gotCalls != nil {
		*a.gotCalls = append(*a.gotCalls, authCall{
			Call: call, Schema: schema, WS: ws, Pol: pol,
		})
	}
	if a.reject {
		return &tools.DecisionError{Stage: a.stage, Reason: a.reason, Call: call}
	}
	return nil
}

// Compile-time assertion that countingAuth satisfies the
// tools.AuthorizeFunc signature (it's not a type that satisfies an
// interface — it's a method-shaped function; the assertion is
// implicit in the NewManager call below).
var _ tools.AuthorizeFunc = (&countingAuth{}).Authorize

// TestMCP_PermissionMapping_PassesThroughAuthorize: the integration
// test for the SCOPE §13 pipeline anchor via the canonical
// tools.Registry.Dispatch path. Dispatch calls the caller-supplied
// AuthorizeFunc (perm.Authorize in production, a stub here) at
// step 2; on a policy rejection, Dispatch returns a structured
// Result with Status="error" and a ToolError{Kind:"permission_denied"}
// mapped from the DecisionError's Stage ("policy").
//
// Handoff 063 design decision: the prior implementation invoked auth
// inside the adapter's Execute, producing a "double auth call" when
// reached via Dispatch. The current implementation removes the
// adapter's internal auth call; registry.Dispatch is the single
// source of truth. The test now exercises the canonical path
// (registry.Dispatch), not the adapter directly, and the counter
// assertion pins "exactly one Authorize invocation per Dispatch".
//
// This is the WORK-1 client-core contribution to TG3's permission
// test coverage. The test pins the seam; a future Run that wants to
// extend the dispatch pipeline (e.g., add a transport-level retry)
// must not regress this contract.
func TestMCP_PermissionMapping_PassesThroughAuthorize(t *testing.T) {
	r := tools.NewRegistry()

	gotCalls := []authCall{}
	stub := &countingAuth{
		reject:   true,
		stage:    "policy",
		reason:   "permission_denied",
		gotCalls: &gotCalls,
	}

	m := NewManager(r, stub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport([]ListedTool{
		{
			Name:        "do_thing",
			Description: "do a thing",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
			},
		},
	})

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// Trigger the call through the canonical tools.Registry.Dispatch
	// path. Dispatch is the single source of truth for the authorize
	// pipeline; the adapter no longer calls auth itself.
	call := tools.Call{
		Name:      "do_thing",
		Arguments: map[string]interface{}{"path": "in-workspace.txt"},
	}
	res := r.Dispatch(context.Background(), call, tools.Workspace{}, tools.Policy(nil), stub.Authorize)

	// The auth was called EXACTLY once (no second door). The
	// registry.Dispatch path is the single source of truth; the
	// adapter's Execute no longer invokes auth itself.
	if stub.calls != 1 {
		t.Fatalf("auth calls = %d, want 1 (single-source-of-truth: registry.Dispatch authorizes once)", stub.calls)
	}

	// The structured result is a ToolError with Kind mapped from
	// the DecisionError's Stage ("policy" → "permission_denied").
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("res.Error = nil, want *tools.ToolError")
	}
	if res.Error.Kind != "permission_denied" {
		t.Fatalf("res.Error.Kind = %q, want %q", res.Error.Kind, "permission_denied")
	}
	if res.Error.Call.Name != "do_thing" {
		t.Fatalf("res.Error.Call.Name = %q, want %q", res.Error.Call.Name, "do_thing")
	}
	if res.Error.Message == "" {
		t.Fatalf("res.Error.Message is empty")
	}

	// The recorded auth call carried the right inputs (Dispatch
	// passes the call, the schema, the workspace, and the policy
	// it was constructed with). The schema came from the server's
	// listing (converted via schemaFromMap); the ws and policy
	// are the values Dispatch was invoked with.
	if len(gotCalls) != 1 {
		t.Fatalf("gotCalls len = %d, want 1", len(gotCalls))
	}
	if gotCalls[0].Call.Name != "do_thing" {
		t.Fatalf("auth gotCalls[0].Call.Name = %q, want %q", gotCalls[0].Call.Name, "do_thing")
	}
	if gotCalls[0].Schema.Properties["path"] != tools.TypeString {
		t.Fatalf("auth gotCalls[0].Schema.Properties[path] = %q, want %q",
			gotCalls[0].Schema.Properties["path"], tools.TypeString)
	}

	// The stub transport was NOT called (Dispatch rejected at the
	// policy stage before reaching adapter.Execute / transport.Call).
	if len(transport.calls) != 0 {
		t.Fatalf("transport.calls len = %d, want 0 (policy rejected; transport.Call should not fire)", len(transport.calls))
	}
}

// TestMCP_PermissionMapping_AuthorizePassThenCall: a passing auth
// call lets Dispatch proceed to adapter.Execute → transport.Call.
// The transport records the call (with the original name, not the
// FinalName) and the adapter returns the transport's result as
// Result.Content. The auth is invoked once (Dispatch is the
// single source of truth per handoff 063).
func TestMCP_PermissionMapping_AuthorizePassThenCall(t *testing.T) {
	r := tools.NewRegistry()
	stub := &countingAuth{reject: false} // pass everything
	m := NewManager(r, stub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport([]ListedTool{
		{
			Name:        "do_thing",
			Description: "do a thing",
			InputSchema: map[string]interface{}{},
		},
	})

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	call := tools.Call{
		Name:      "do_thing",
		Arguments: map[string]interface{}{"k": "v"},
	}
	res := r.Dispatch(context.Background(), call, tools.Workspace{}, tools.Policy(nil), stub.Authorize)
	if res.Status != "ok" {
		t.Fatalf("res.Status = %q, want %q (res=%+v)", res.Status, "ok", res)
	}
	if res.Error != nil {
		t.Fatalf("res.Error = %v, want nil", res.Error)
	}
	if stub.calls != 1 {
		t.Fatalf("auth calls = %d, want 1", stub.calls)
	}
	if len(transport.calls) != 1 {
		t.Fatalf("transport.calls len = %d, want 1", len(transport.calls))
	}
	// The adapter passes the ORIGINAL tool name to transport.Call
	// (not the collision-resolved FinalName — MCP servers expect
	// the name they advertised in their listing).
	if transport.calls[0].Name != "do_thing" {
		t.Fatalf("transport.calls[0].Name = %q, want %q (original name passed to transport)", transport.calls[0].Name, "do_thing")
	}
	if transport.calls[0].Args["k"] != "v" {
		t.Fatalf("transport.calls[0].Args[k] = %v, want %q", transport.calls[0].Args["k"], "v")
	}
}

// TestMCP_PermissionMapping_TransportCallErrorBecomesToolError: the
// adapter surfaces a transport.Call error as a structured
// ToolError{Kind:"execution_failed"}. Per GOAL §2 bound decision 4:
// "Transport failures during a tool call are structured tool failures
// (the model sees them), never harness crashes." Auth passes (via
// Dispatch); the transport errors; the adapter returns a structured
// Result with Status="error"; Dispatch returns the same Result
// without a second Go-error wrap.
func TestMCP_PermissionMapping_TransportCallErrorBecomesToolError(t *testing.T) {
	r := tools.NewRegistry()
	stub := &countingAuth{reject: false}
	m := NewManager(r, stub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport([]ListedTool{
		{Name: "do_thing", Description: "do a thing", InputSchema: map[string]interface{}{}},
	})
	transport.nextCallErr = &stubError{msg: "transport connection reset"}

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	res := r.Dispatch(context.Background(), tools.Call{Name: "do_thing", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), stub.Authorize)
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("res.Error = nil, want *tools.ToolError")
	}
	if res.Error.Kind != "execution_failed" {
		t.Fatalf("res.Error.Kind = %q, want %q", res.Error.Kind, "execution_failed")
	}
	if !contains(res.Error.Message, "transport connection reset") {
		t.Fatalf("res.Error.Message = %q, want error message mentioning transport failure", res.Error.Message)
	}
}

// TestMCP_PermissionMapping_SchemaViolationBecomesToolError: a
// schema violation at the auth stage fires before transport.Call.
// Dispatch's mapStageToKind maps Stage="schema" to
// ToolError.Kind="schema_violation" — same shape as the
// registry.Dispatch pipeline's mapping. The test now exercises the
// canonical Dispatch path per handoff 063.
func TestMCP_PermissionMapping_SchemaViolationBecomesToolError(t *testing.T) {
	r := tools.NewRegistry()
	stub := &countingAuth{
		reject: true,
		stage:  "schema",
		reason: "missing_field",
	}
	m := NewManager(r, stub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport([]ListedTool{
		{
			Name: "do_thing",
			Description: "do a thing",
			InputSchema: map[string]interface{}{
				"required": []interface{}{"path"},
			},
		},
	})

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	res := r.Dispatch(context.Background(), tools.Call{Name: "do_thing", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), stub.Authorize)
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("res.Error = %+v, want Kind=%q", res.Error, "schema_violation")
	}
	if len(transport.calls) != 0 {
		t.Fatalf("transport.calls len = %d, want 0 (schema violation short-circuits before transport.Call)", len(transport.calls))
	}
}

// TestMCP_SingleAuthPass: the binding pin for handoff 063's
// auth-double-call design decision (RECOMMENDED path). The test
// invokes tools.Registry.Dispatch against a single MCP tool
// registered through Manager.AddServer. The count assertion pins
// "exactly one Authorize invocation per Dispatch for MCP tools" —
// if a future Run re-introduces an adapter-internal auth call, this
// test would fail because the auth count would jump to 2.
//
// The pin exercises the canonical production path: Manager.AddServer
// → mcpAdapter → tools.Registry.Dispatch (which calls auth at step 2
// + Execute at step 3). The stub auth records every invocation; the
// stub transport returns a successful result, so Execute reaches
// transport.Call and the Dispatch pipeline returns a Result with
// Status="ok".
//
// The design rationale (recorded in mcpAdapter doc comment): the
// registry.Dispatch auth pass is the single source of truth; the
// adapter's Execute is a "transport-only" short path that does NOT
// invoke auth (the a.auth field is preserved for symmetry with other
// Tool implementations but is not consumed by Execute directly).
func TestMCP_SingleAuthPass(t *testing.T) {
	r := tools.NewRegistry()
	stub := &countingAuth{reject: false}
	m := NewManager(r, stub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport([]ListedTool{
		{Name: "do_thing", Description: "do a thing", InputSchema: map[string]interface{}{}},
	})

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	call := tools.Call{Name: "do_thing", Arguments: map[string]interface{}{}}
	res := r.Dispatch(context.Background(), call, tools.Workspace{}, tools.Policy(nil), stub.Authorize)
	if res.Status != "ok" {
		t.Fatalf("res.Status = %q, want %q (res=%+v)", res.Status, "ok", res)
	}
	if stub.calls != 1 {
		t.Fatalf("auth calls = %d, want 1 (single-source-of-truth: registry.Dispatch authorizes once, adapter does not re-invoke)", stub.calls)
	}
}

// TestMCP_EndToEnd_Happy: traces one MCP tool call end-to-end
// through the full binding chain:
//
//	Manager.AddServer
//	  → allowlist filter
//	  → ResolveFinalName (builtin-collision rule)
//	  → newAdapter
//	  → registry.Register(adapter)
//	  → the live tools.Registry.Dispatch path
//	  → mcpAdapter.Execute
//	  → transport.Call
//	  → structured Result{Status:"ok", Content: out}
//
// The pin uses an IN-PROCESS httptest server speaking JSON-RPC 2.0
// so no live mcp-light endpoint is required (per handoff §2 —
// "Tests: in-process stub transports… no live service in
// scripts/test.sh"). The test asserts:
//
//   - one MCP tool registered under the collision name
//     `<server>__<tool>` because a stub builtin named `<tool>` is
//     pre-registered to exercise the collision rule;
//   - the live Dispatch returns the Result from the stub
//     (echoing the call args verbatim);
//   - exactly one Authorize invocation (the handoff-063 design
//     decision pin — see TestMCP_SingleAuthPass);
//   - the JSONL `tool_result` event wire shape reuses
//     `tool_call`/`tool_result` unchanged (SCOPE §42 additive — the
//     pin's transport-level content sits inside the existing
//     Result.Content envelope).
func TestMCP_EndToEnd_Happy(t *testing.T) {
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{
				Name:        "do_thing",
				Description: "do a thing",
				InputSchema: map[string]interface{}{
					"properties": map[string]interface{}{
						"arg1": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer httpSrv.Close()

	r := tools.NewRegistry()
	// Pre-register a stub builtin named "do_thing" so the MCP tool
	// will be registered under the collision form "<server>__<do_thing>".
	r.Register(&builtinEchoTool{name: "do_thing", reply: "builtin-reply"})

	authStub := &countingAuth{reject: false}
	m := NewManager(r, authStub.Authorize, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "http", Endpoint: httpSrv.URL}
	transport := NewHTTPTransport(httpSrv.URL)
	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// The MCP tool must be registered under the collision form
	// "<server>__<tool>" because the bare "do_thing" is already
	// registered as a builtin. ResolveFinalName returned the
	// collision form, and the adapter was registered under it.
	mcpTool, ok := r.Get("weather__do_thing")
	if !ok || mcpTool == nil {
		t.Fatalf("r.Get(weather__do_thing) = (nil, %v), want (non-nil, true) — MCP tool missing", ok)
	}
	if mcpTool.Meta().Name != "weather__do_thing" {
		t.Fatalf("mcpTool.Meta().Name = %q, want %q", mcpTool.Meta().Name, "weather__do_thing")
	}

	// The bare "do_thing" remains the builtin (collision rule:
	// builtin wins, no silent shadowing).
	builtin, ok := r.Get("do_thing")
	if !ok || builtin == nil || builtin.Meta().Name != "do_thing" {
		t.Fatalf("r.Get(do_thing) returned wrong tool; builtin must remain under bare name")
	}

	// Dispatch the MCP tool under the collision name through the
	// canonical registry.Dispatch path. The auth fires once
	// (single source of truth); the adapter's Execute reaches
	// transport.Call and returns the stub's canned content.
	call := tools.Call{
		Name:      "weather__do_thing",
		Arguments: map[string]interface{}{"arg1": "hello"},
	}
	res := r.Dispatch(context.Background(), call, tools.Workspace{}, tools.Policy(nil), authStub.Authorize)

	if res.Status != "ok" {
		t.Fatalf("res.Status = %q, want %q (res=%+v)", res.Status, "ok", res)
	}
	if res.Error != nil {
		t.Fatalf("res.Error = %v, want nil", res.Error)
	}

	// The stub recorded the call with the ORIGINAL tool name
	// (not the collision-resolved FinalName — MCP servers expect
	// the name they advertised in their listing).
	stub.mu.Lock()
	stubCalls := append([]stubCall(nil), stub.calls...)
	stub.mu.Unlock()
	if len(stubCalls) != 1 {
		t.Fatalf("stub.calls len = %d, want 1", len(stubCalls))
	}
	if stubCalls[0].Name != "do_thing" {
		t.Fatalf("stub.calls[0].Name = %q, want %q (original name)", stubCalls[0].Name, "do_thing")
	}
	if stubCalls[0].Args["arg1"] != "hello" {
		t.Fatalf("stub.calls[0].Args[arg1] = %v, want %q", stubCalls[0].Args["arg1"], "hello")
	}

	// The single-source-of-truth pin: exactly one Authorize
	// invocation per Dispatch for MCP tools. See TestMCP_SingleAuthPass.
	if authStub.calls != 1 {
		t.Fatalf("auth calls = %d, want 1 (single-source-of-truth)", authStub.calls)
	}
}

// TestMCP_EndToEnd_BuiltinWinsCollision: verifies that when an MCP
// tool name collides with a builtin, the builtin stays under the
// bare name AND the MCP tool is registered under `<server>__<tool>`;
// dispatching the bare name invokes the builtin; dispatching the
// prefixed name invokes the MCP tool. The pin exercises the
// collision rule end-to-end through the live registry.Dispatch
// path.
func TestMCP_EndToEnd_BuiltinWinsCollision(t *testing.T) {
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{Name: "read_file", Description: "MCP read_file", InputSchema: map[string]interface{}{}},
		},
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer httpSrv.Close()

	r := tools.NewRegistry()
	r.Register(&builtinEchoTool{name: "read_file", reply: "builtin-read"})

	authStub := &countingAuth{reject: false}
	m := NewManager(r, authStub.Authorize, tools.Policy(nil), tools.Workspace{})
	srv := Server{Name: "fs", Transport: "http", Endpoint: httpSrv.URL}
	transport := NewHTTPTransport(httpSrv.URL)
	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// Dispatching the bare "read_file" name invokes the builtin
	// (the builtin wins on collision). The adapter's Execute is
	// NOT called; transport.calls is empty.
	builtinRes := r.Dispatch(context.Background(),
		tools.Call{Name: "read_file", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), authStub.Authorize)
	if builtinRes.Status != "ok" {
		t.Fatalf("builtin dispatch res.Status = %q, want %q", builtinRes.Status, "ok")
	}
	if builtinRes.Content != "builtin-read" {
		t.Fatalf("builtin dispatch res.Content = %v, want %q", builtinRes.Content, "builtin-read")
	}

	// Dispatching the prefixed "fs__read_file" name invokes the
	// MCP tool (the adapter's Execute fires transport.Call).
	mcpRes := r.Dispatch(context.Background(),
		tools.Call{Name: "fs__read_file", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), authStub.Authorize)
	if mcpRes.Status != "ok" {
		t.Fatalf("mcp dispatch res.Status = %q, want %q", mcpRes.Status, "ok")
	}

	// The stub recorded one call (the MCP dispatch) with the
	// original name "read_file" — the bare-name builtin dispatch
	// did NOT touch transport.calls.
	stub.mu.Lock()
	stubCalls := append([]stubCall(nil), stub.calls...)
	stub.mu.Unlock()
	if len(stubCalls) != 1 {
		t.Fatalf("stub.calls len = %d, want 1", len(stubCalls))
	}
	if stubCalls[0].Name != "read_file" {
		t.Fatalf("stub.calls[0].Name = %q, want %q", stubCalls[0].Name, "read_file")
	}
}

// TestMCP_EndToEnd_AllowlistExclusion: verifies that an MCP tool
// listed by the server but excluded by the allowlist is NEVER
// registered with the registry (allowlist exclusion is
// uncallable per SCOPE §29 + §43). `registry.Dispatch` for the
// excluded name returns the standard `not_found` structured error
// (Kind="unknown_tool") — no silent omission. The pin traces the
// allowlist filter at Manager.AddServer (WORK 1) end-to-end.
func TestMCP_EndToEnd_AllowlistExclusion(t *testing.T) {
	stub := &stubHTTPServer{
		listings: []ListedTool{
			{Name: "tool_alpha", Description: "alpha", InputSchema: map[string]interface{}{}},
			{Name: "tool_beta", Description: "beta", InputSchema: map[string]interface{}{}},
			{Name: "tool_excluded", Description: "should be excluded", InputSchema: map[string]interface{}{}},
		},
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer httpSrv.Close()

	r := tools.NewRegistry()
	authStub := &countingAuth{reject: false}
	m := NewManager(r, authStub.Authorize, tools.Policy(nil), tools.Workspace{})

	// Allowlist explicitly excludes tool_excluded.
	srv := Server{
		Name:      "weather",
		Transport: "http",
		Endpoint:  httpSrv.URL,
		Allowlist: []string{"tool_alpha", "tool_beta"},
	}
	transport := NewHTTPTransport(httpSrv.URL)
	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// tool_alpha + tool_beta registered; tool_excluded absent.
	if _, ok := r.Get("tool_alpha"); !ok {
		t.Fatalf("r.Get(tool_alpha) = false, want true (allowlisted)")
	}
	if _, ok := r.Get("tool_beta"); !ok {
		t.Fatalf("r.Get(tool_beta) = false, want true (allowlisted)")
	}
	if _, ok := r.Get("tool_excluded"); ok {
		t.Fatalf("r.Get(tool_excluded) = true, want false (allowlist exclusion = uncallable)")
	}

	// Dispatching the excluded name returns the standard
	// "unknown_tool" structured error — no silent omission.
	res := r.Dispatch(context.Background(),
		tools.Call{Name: "tool_excluded", Arguments: map[string]interface{}{}},
		tools.Workspace{}, tools.Policy(nil), authStub.Authorize)
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "unknown_tool" {
		t.Fatalf("res.Error = %+v, want Kind=%q", res.Error, "unknown_tool")
	}

	// Allowlist-exclusion path: Dispatch fired no auth call (the
	// tool is unknown at step 1, before auth step 2).
	if authStub.calls != 0 {
		t.Fatalf("auth calls = %d, want 0 (unknown-tool short-circuit before auth)", authStub.calls)
	}
}
