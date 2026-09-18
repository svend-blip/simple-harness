package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/svend-blip/simple-harness/internal/path"
)

// stubPolicy is a Policy implementation used by the dispatch tests.
// It always returns Decision{Allowed: true, Reason: "test-stub"}. The
// real Permissive lives in internal/perm; defining a local stub here
// breaks the would-be test-time import cycle (perm imports tools, so
// tools' tests cannot import perm without cycling).
type stubPolicy struct{}

func (stubPolicy) Decide(_ context.Context, _ Call, _ Workspace) Decision {
	return Decision{Allowed: true, Reason: "test-stub"}
}

// allowAllAuthorize is an AuthorizeFunc that always returns nil (i.e.,
// it acts like the stub policy allowing everything). Used to exercise
// the Dispatch path-Execute path in isolation.
func allowAllAuthorize(_ context.Context, _ Call, _ Schema, _ Workspace, _ Policy) *DecisionError {
	return nil
}

// rejectAllAuthorize rejects every call at the schema stage. Used to
// exercise Dispatch's short-circuit-at-schema behavior.
func rejectAllAuthorize(_ context.Context, call Call, _ Schema, _ Workspace, _ Policy) *DecisionError {
	return &DecisionError{Stage: "schema", Reason: "missing_field", Call: call}
}

// recordingTool is a Tool that records the calls it receives in
// Execute. Used to verify the pipeline's short-circuit behavior (a
// rejected call never reaches Execute).
type recordingTool struct {
	meta   ToolMeta
	schema Schema
	calls  *[]Call
}

func (r *recordingTool) Meta() ToolMeta { return r.meta }
func (r *recordingTool) Schema() Schema { return r.schema }
func (r *recordingTool) Execute(_ context.Context, call Call) (Result, error) {
	*r.calls = append(*r.calls, call)
	return Result{Status: "ok", Content: "ok"}, nil
}

// TestRegistry_UnknownToolRejection: Dispatch with call.Name="no_such_tool"
// returns Result{Status:"error", Error.Kind:"unknown_tool"}.
func TestRegistry_UnknownToolRejection(t *testing.T) {
	r := NewRegistry()
	ws := tempPathWorkspace(t)
	res := r.Dispatch(context.Background(),
		Call{Name: "no_such_tool", Arguments: map[string]any{}},
		ws, stubPolicy{}, allowAllAuthorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "unknown_tool" {
		t.Fatalf("Error.Kind = %v, want \"unknown_tool\"", res.Error)
	}
}

// TestRegistry_DispatchShortCircuits_AtSchema: a call with a schema
// violation. The schema check fires; Execute is never called.
func TestRegistry_DispatchShortCircuits_AtSchema(t *testing.T) {
	rec := &[]Call{}
	rt := &recordingTool{
		meta:   ToolMeta{Name: "echo"},
		schema: Schema{Required: []string{"required_field"}},
		calls:  rec,
	}
	r := NewRegistry()
	r.Register(rt)
	ws := tempPathWorkspace(t)

	res := r.Dispatch(context.Background(),
		Call{Name: "echo", Arguments: map[string]any{}},
		ws, stubPolicy{}, rejectAllAuthorize)

	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want \"schema_violation\"", res.Error)
	}
	if len(*rec) != 0 {
		t.Fatalf("Execute was called %d time(s), want 0 (schema check should short-circuit)", len(*rec))
	}
}

// TestRegistry_DispatchShortCircuits_AtPath: a call with valid schema
// but a path escape (via a path-rejecting AuthorizeFunc). The path
// check fires; Execute is never called.
func TestRegistry_DispatchShortCircuits_AtPath(t *testing.T) {
	rec := &[]Call{}
	rt := &recordingTool{
		meta:   ToolMeta{Name: "echo"},
		schema: Schema{},
		calls:  rec,
	}
	r := NewRegistry()
	r.Register(rt)
	ws := tempPathWorkspace(t)

	rejectAtPath := func(_ context.Context, call Call, _ Schema, _ Workspace, _ Policy) *DecisionError {
		return &DecisionError{Stage: "path", Reason: "parent_traversal", Call: call}
	}

	res := r.Dispatch(context.Background(),
		Call{Name: "echo", Arguments: map[string]any{"path": "../escape.txt"}},
		ws, stubPolicy{}, rejectAtPath)

	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want \"path_escape\"", res.Error)
	}
	if len(*rec) != 0 {
		t.Fatalf("Execute was called %d time(s), want 0 (path check should short-circuit)", len(*rec))
	}
}

// TestRegistry_Dispatch_PassesAll: a valid call with the stub policy
// and an allow-all AuthorizeFunc. Execute is called once; Result.Status
// is "ok".
func TestRegistry_Dispatch_PassesAll(t *testing.T) {
	rec := &[]Call{}
	rt := &recordingTool{
		meta:   ToolMeta{Name: "echo"},
		schema: Schema{},
		calls:  rec,
	}
	r := NewRegistry()
	r.Register(rt)
	ws := tempPathWorkspace(t)

	res := r.Dispatch(context.Background(),
		Call{Name: "echo", Arguments: map[string]any{"x": "y"}},
		ws, stubPolicy{}, allowAllAuthorize)

	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (res=%+v)", res.Status, "ok", res)
	}
	if res.Error != nil {
		t.Fatalf("Error = %v, want nil", res.Error)
	}
	if len(*rec) != 1 {
		t.Fatalf("Execute was called %d time(s), want 1", len(*rec))
	}
	if !reflect.DeepEqual((*rec)[0].Arguments, map[string]any{"x": "y"}) {
		t.Fatalf("recorded call Arguments = %v, want %v", (*rec)[0].Arguments, map[string]any{"x": "y"})
	}
}

// TestRegistry_Dispatch_ExecutionError: Execute returns a non-nil error.
// Dispatch wraps it in ToolError{Kind:"execution_failed"}.
func TestRegistry_Dispatch_ExecutionError(t *testing.T) {
	et := &errorTool{name: "boom", err: errors.New("disk on fire")}
	r := NewRegistry()
	r.Register(et)
	ws := tempPathWorkspace(t)

	res := r.Dispatch(context.Background(),
		Call{Name: "boom", Arguments: map[string]any{}},
		ws, stubPolicy{}, allowAllAuthorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "execution_failed" {
		t.Fatalf("Error.Kind = %v, want \"execution_failed\"", res.Error)
	}
}

// errorTool returns a structured error from Execute so Dispatch can
// wrap it.
type errorTool struct {
	name string
	err  error
}

func (e *errorTool) Meta() ToolMeta { return ToolMeta{Name: e.name} }
func (e *errorTool) Schema() Schema { return Schema{} }
func (e *errorTool) Execute(_ context.Context, _ Call) (Result, error) {
	return Result{}, e.err
}

// tempPathWorkspace creates a temporary directory and returns a
// path.Workspace rooted at it.
func tempPathWorkspace(t *testing.T) path.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := path.New(dir)
	if err != nil {
		t.Fatalf("path.New(%q): %v", dir, err)
	}
	return ws
}

// TestRegistry_Dispatch_DoesNotMutateTheCallersArguments — the
// authorize stage rewrites path arguments to their normalized form
// for the tool. That rewrite must land on a copy: the caller's map is
// also the model's own tool_calls record in the conversation, and the
// model must see what it sent, not what the harness resolved it to.
func TestRegistry_Dispatch_DoesNotMutateTheCallersArguments(t *testing.T) {
	var calls []Call
	reg := NewRegistry()
	reg.Register(&recordingTool{
		meta:   ToolMeta{Name: "t", Description: "d"},
		schema: Schema{Properties: map[string]PropertyType{"path": TypeString}},
		calls:  &calls,
	})
	ws, err := path.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rewriting := func(_ context.Context, call Call, _ Schema, ws Workspace, _ Policy) *DecisionError {
		call.Arguments["path"] = ws.Root() + "/rewritten"
		return nil
	}
	args := map[string]any{"path": "original"}
	res := reg.Dispatch(context.Background(), Call{Name: "t", Arguments: args}, ws, stubPolicy{}, rewriting)
	if res.Status != "ok" {
		t.Fatalf("Dispatch: %+v", res)
	}
	if args["path"] != "original" {
		t.Errorf("caller's arguments were mutated: %v", args)
	}
	if len(calls) != 1 || calls[0].Arguments["path"] != ws.Root()+"/rewritten" {
		t.Errorf("tool saw %v, want the rewritten path", calls)
	}
}

// TestRegistry_Dispatch_CarriesTheWorkspaceInContext — tools that
// need the workspace root (the shell's default cwd, the skill tools'
// workspace search root) used os.Getwd(), which is only the
// workspace when the harness happens to run from it.
func TestRegistry_Dispatch_CarriesTheWorkspaceInContext(t *testing.T) {
	var seen []string
	reg := NewRegistry()
	reg.Register(&ctxTool{seen: &seen})
	ws, err := path.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg.Dispatch(context.Background(), Call{Name: "c", Arguments: map[string]any{}}, ws, stubPolicy{}, allowAllAuthorize)
	if len(seen) != 1 || seen[0] != ws.Root() {
		t.Errorf("tool saw workspace %v, want %q", seen, ws.Root())
	}
	if _, ok := WorkspaceFromContext(context.Background()); ok {
		t.Error("a bare context must not report a workspace")
	}
}

type ctxTool struct{ seen *[]string }

func (c *ctxTool) Meta() ToolMeta { return ToolMeta{Name: "c", Description: "d"} }
func (c *ctxTool) Schema() Schema { return Schema{} }
func (c *ctxTool) Execute(ctx context.Context, _ Call) (Result, error) {
	ws, _ := WorkspaceFromContext(ctx)
	*c.seen = append(*c.seen, ws.Root())
	return Result{Status: "ok"}, nil
}
