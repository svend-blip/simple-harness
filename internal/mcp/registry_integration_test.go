package mcp

import (
	"context"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// countingAuth is an AuthorizeFunc that records every invocation and
// returns a *tools.DecisionError when configured to reject. The
// counter is the test's load-bearing assertion: the test calls the
// adapter's Execute exactly once and verifies the counter is 1.
type countingAuth struct {
	calls    int
	reject   bool // when true, returns a structured *tools.DecisionError
	stage    string
	reason   string
	gotCalls *[]authCall
}

// authCall records one invocation of the stub AuthorizeFunc. The
// fields are what the adapter passes; the test inspects them to
// verify the adapter called auth with the right call, schema, ws,
// and policy.
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
// test for the SCOPE §13 pipeline anchor. The adapter's Execute
// MUST call the caller-supplied AuthorizeFunc (perm.Authorize in
// production, a stub here) before dispatching the tool call — and
// MUST return a structured error when auth rejects. The counter
// assertion pins "exactly once" — there is no second door around
// perm.Policy in the MCP client.
//
// This is the WORK-1 client-core contribution to TG3's permission
// test coverage. The test pins the seam; a future Run that wants to
// extend the adapter's Execute (e.g., add a transport-level retry)
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

	// Pull the registered adapter from the registry.
	adapter, ok := r.Get("do_thing")
	if !ok || adapter == nil {
		t.Fatalf("r.Get(do_thing) = (nil, %v), want (non-nil, true) — adapter not registered", ok)
	}

	// Trigger a tool call directly on the adapter (NOT through
	// registry.Dispatch — the test isolates the adapter's own
	// Execute behavior from the registry's pipeline). The stub
	// auth rejects the call with a structured *tools.DecisionError.
	call := tools.Call{
		Name:      "do_thing",
		Arguments: map[string]interface{}{"path": "in-workspace.txt"},
	}
	res, err := adapter.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("adapter.Execute error = %v, want nil (auth-rejected calls return a structured Result, not a Go error)", err)
	}

	// The auth was called EXACTLY once (no second door). This is
	// the load-bearing assertion for "no second door around
	// perm.Policy" — if a future Run adds a parallel permission
	// check inside the adapter's Execute, this assertion will fire.
	if stub.calls != 1 {
		t.Fatalf("auth calls = %d, want 1 (no second door around perm.Policy)", stub.calls)
	}

	// The structured result is a ToolError with Kind mapped from
	// the DecisionError's Stage ("policy" → "permission_denied").
	// The mapping matches tools.mapStageToKind so the model's view
	// is indistinguishable from a builtin-tool failure.
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

	// The recorded auth call carried the right inputs (the
	// adapter passes the call, the schema, the workspace, and the
	// policy it was constructed with). The schema came from the
	// server's listing (converted via schemaFromMap); the ws and
	// policy are the Manager's construction-time values.
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

	// The stub transport was NOT called (the call was rejected at
	// the policy stage before reaching transport.Call). This pins
	// the auth → transport ordering.
	if len(transport.calls) != 0 {
		t.Fatalf("transport.calls len = %d, want 0 (auth rejected the call; transport.Call should not fire)", len(transport.calls))
	}
}

// TestMCP_PermissionMapping_AuthorizePassThenCall: a passing auth
// call lets the adapter proceed to transport.Call. The transport
// records the call (with the original name, not the FinalName) and
// the adapter returns the transport's result as Result.Content.
// Together with the auth-reject test above, this pins the full
// happy-path pipeline end-to-end inside the adapter.
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

	adapter, ok := r.Get("do_thing")
	if !ok || adapter == nil {
		t.Fatalf("r.Get(do_thing) = (nil, %v), want (non-nil, true)", ok)
	}

	call := tools.Call{
		Name:      "do_thing",
		Arguments: map[string]interface{}{"k": "v"},
	}
	res, err := adapter.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("adapter.Execute error = %v, want nil", err)
	}
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
// (the model sees them), never harness crashes." The auth passes; the
// transport errors; the adapter returns a structured Result with
// Status="error" (NOT a Go error from Execute).
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

	adapter, ok := r.Get("do_thing")
	if !ok || adapter == nil {
		t.Fatalf("r.Get(do_thing) = (nil, %v), want (non-nil, true)", ok)
	}

	res, err := adapter.Execute(context.Background(), tools.Call{Name: "do_thing", Arguments: map[string]interface{}{}})
	if err != nil {
		t.Fatalf("adapter.Execute error = %v, want nil (transport errors are surfaced as structured Result, not Go error)", err)
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
	if !contains(res.Error.Message, "transport connection reset") {
		t.Fatalf("res.Error.Message = %q, want error message mentioning transport failure", res.Error.Message)
	}
}

// TestMCP_PermissionMapping_SchemaViolationBecomesToolError: a
// schema violation at the auth stage fires before transport.Call.
// The adapter maps the DecisionError's Stage="schema" to
// ToolError.Kind="schema_violation" via stageToKind — same shape as
// the registry.Dispatch pipeline's mapping.
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

	adapter, ok := r.Get("do_thing")
	if !ok || adapter == nil {
		t.Fatalf("r.Get(do_thing) = (nil, %v), want (non-nil, true)", ok)
	}

	res, err := adapter.Execute(context.Background(), tools.Call{Name: "do_thing", Arguments: map[string]interface{}{}})
	if err != nil {
		t.Fatalf("adapter.Execute error = %v, want nil", err)
	}
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