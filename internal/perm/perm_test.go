package perm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// fakeTool is a minimal Tool used only to drive the schema-validation
// path in Authorize. Run 014 / Run 015 ship real tools; this stub
// exists only to let Authorize tests express "schema X rejects call Y".
type fakeTool struct {
	name   string
	schema tools.Schema
}

func (f *fakeTool) Meta() tools.ToolMeta { return tools.ToolMeta{Name: f.name} }
func (f *fakeTool) Schema() tools.Schema { return f.schema }
func (f *fakeTool) Execute(_ context.Context, _ tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

// TestAuthorize_PipelineOrdering_SchemaFirst: a call with BOTH a schema
// violation AND a path escape. The schema check fires FIRST; the path
// check never runs.
func TestAuthorize_PipelineOrdering_SchemaFirst(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	// Missing the required "path" field (schema violation) AND the
	// "extra" field is unknown (additional_property violation).
	call := tools.Call{
		Name:      "any",
		Arguments: map[string]any{"extra": "../escape.txt"},
	}

	de := Authorize(context.Background(), call, schema, ws, NewPermissive())
	if de == nil {
		t.Fatalf("Authorize returned nil, want *tools.DecisionError")
	}
	if de.Stage != "schema" {
		t.Fatalf("DecisionError.Stage = %q, want %q (schema check should fire first)",
			de.Stage, "schema")
	}
}

// TestAuthorize_PipelineOrdering_PathSecond: a call with a VALID schema
// but a path escape. The schema check passes; the path check fires.
func TestAuthorize_PipelineOrdering_PathSecond(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	call := tools.Call{
		Name:      "any",
		Arguments: map[string]any{"path": "../escape.txt"},
	}

	de := Authorize(context.Background(), call, schema, ws, NewPermissive())
	if de == nil {
		t.Fatalf("Authorize returned nil, want *tools.DecisionError")
	}
	if de.Stage != "path" {
		t.Fatalf("DecisionError.Stage = %q, want %q (path check should fire after schema)",
			de.Stage, "path")
	}
	if de.Reason != path.ReasonParentTraversal {
		t.Fatalf("DecisionError.Reason = %q, want %q",
			de.Reason, path.ReasonParentTraversal)
	}
}

// TestAuthorize_PipelineOrdering_PolicyThird: a call with both schema
// and path passing. The stub policy is Permissive, so the policy step
// always returns Allowed; Authorize returns nil.
func TestAuthorize_PipelineOrdering_PolicyThird(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	// Use a path that exists inside the workspace so Normalize succeeds
	// (a non-existent path is fine too, but we exercise the happy path
	// here).
	call := tools.Call{
		Name:      "any",
		Arguments: map[string]any{"path": "some-file.txt"},
	}

	de := Authorize(context.Background(), call, schema, ws, NewPermissive())
	if de != nil {
		t.Fatalf("Authorize returned %v, want nil (schema and path both pass; Permissive policy allows)", de)
	}
}

// TestAuthorize_StructuredError: a call with a schema violation. The
// DecisionError carries Call (round-trip), Stage, Reason, and a non-empty
// Error() string (no stack trace, just a one-line message).
func TestAuthorize_StructuredError(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	call := tools.Call{
		Name:      "my_tool",
		Arguments: map[string]any{},
	}

	de := Authorize(context.Background(), call, schema, ws, NewPermissive())
	if de == nil {
		t.Fatalf("Authorize returned nil, want *tools.DecisionError")
	}

	// Call round-trips.
	if !reflect.DeepEqual(de.Call, call) {
		t.Fatalf("DecisionError.Call = %+v, want %+v", de.Call, call)
	}
	if de.Stage != "schema" {
		t.Fatalf("DecisionError.Stage = %q, want %q", de.Stage, "schema")
	}
	if de.Reason == "" {
		t.Fatalf("DecisionError.Reason is empty")
	}
	msg := de.Error()
	if msg == "" {
		t.Fatalf("DecisionError.Error() is empty")
	}
	if strings.Contains(msg, "\n") {
		t.Fatalf("DecisionError.Error() contains a newline: %q", msg)
	}
	if strings.Contains(msg, ".go:") || strings.Contains(msg, "goroutine") {
		t.Fatalf("DecisionError.Error() looks like a stack trace: %q", msg)
	}
}

// TestAuthorize_SchemaPassesWhenSchemaIsEmpty: an empty schema (no
// Required, no Properties) and an empty Arguments map accepts any call.
// tools.Validate returns nil; Authorize returns nil.
func TestAuthorize_SchemaPassesWhenSchemaIsEmpty(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{}
	call := tools.Call{
		Name:      "any_name",
		Arguments: map[string]any{},
	}

	de := Authorize(context.Background(), call, schema, ws, NewPermissive())
	if de != nil {
		t.Fatalf("Authorize(empty schema, empty args) = %v, want nil", de)
	}
}

// TestDecisionError_Error is a focused unit test for the Error() format.
func TestDecisionError_Error(t *testing.T) {
	de := &tools.DecisionError{
		Stage:  "path",
		Reason: "parent_traversal",
		Call:   tools.Call{Name: "read_file"},
	}
	msg := de.Error()
	want := "perm.Authorize rejected call read_file at stage path: parent_traversal"
	if msg != want {
		t.Fatalf("DecisionError.Error() = %q, want %q", msg, want)
	}
}

// TestPolicy_Permissive_AlwaysAllows pins the stub's behavior.
func TestPolicy_Permissive_AlwaysAllows(t *testing.T) {
	ws := tempWorkspace(t)
	d := NewPermissive().Decide(context.Background(),
		tools.Call{Name: "any", Arguments: map[string]any{"path": "/etc/passwd"}}, ws)
	if !d.Allowed {
		t.Fatalf("Permissive.Decide returned Allowed=false, want true (stub allows everything)")
	}
	if d.Reason != "policy-stub" {
		t.Fatalf("Permissive.Decide Reason = %q, want %q", d.Reason, "policy-stub")
	}
}

// tempWorkspace creates a temporary directory and returns a Workspace
// rooted at it. Mirrors internal/path's helper for ergonomics.
func tempWorkspace(t *testing.T) path.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := path.New(dir)
	if err != nil {
		t.Fatalf("path.New(%q): %v", dir, err)
	}
	return ws
}