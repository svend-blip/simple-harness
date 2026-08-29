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

	de := Authorize(context.Background(), call, schema, ws, NewPolicy(READ_ONLY))
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

	de := Authorize(context.Background(), call, schema, ws, NewPolicy(READ_ONLY))
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
// and path passing. The READ_ONLY policy allows read-only tools
// ("any" is not on the mutation list), so the policy step returns
// Allowed; Authorize returns nil.
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

	de := Authorize(context.Background(), call, schema, ws, NewPolicy(READ_ONLY))
	if de != nil {
		t.Fatalf("Authorize returned %v, want nil (schema and path both pass; READ_ONLY allows non-mutation tools)", de)
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

	de := Authorize(context.Background(), call, schema, ws, NewPolicy(READ_ONLY))
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

	de := Authorize(context.Background(), call, schema, ws, NewPolicy(READ_ONLY))
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

// TestAuthorize_DefaultModeIsReadOnly pins the SCOPE §12 "never
// silent escalation" rule: the zero-value Policy is READ_ONLY (the
// harness never silently escalates). For every tool on the mutation
// list, the zero-value policy denies the call.
//
// The zero-value Policy is the natural default — when an empty
// struct is passed where Mode has not been set, the Mode field is 0
// which is the READ_ONLY constant. This test demonstrates the
// default-deny semantics for mutation under the uninitialized state.
func TestAuthorize_DefaultModeIsReadOnly(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	var pol Policy // zero value: Mode == 0 == READ_ONLY

	for toolName := range mutationTools {
		call := tools.Call{
			Name:      toolName,
			Arguments: map[string]any{"path": "in-workspace.txt"},
		}
		de := Authorize(context.Background(), call, schema, ws, pol)
		if de == nil {
			t.Fatalf("Authorize(%s, zero-policy) returned nil, want *DecisionError (zero policy = READ_ONLY denies mutation)",
				toolName)
		}
		if de.Stage != "policy" {
			t.Fatalf("Authorize(%s, zero-policy) Stage = %q, want %q (zero-policy should fire at policy stage)",
				toolName, de.Stage, "policy")
		}
	}
}

// TestAuthorize_PolicyWORKSPACE_WRITE_AllowsInWorkspace confirms the
// WORKSPACE_WRITE mode lets a mutation tool reach Execute when the
// path is inside the workspace.
func TestAuthorize_PolicyWORKSPACE_WRITE_AllowsInWorkspace(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	for toolName := range mutationTools {
		call := tools.Call{
			Name:      toolName,
			Arguments: map[string]any{"path": "in-workspace.txt"},
		}
		de := Authorize(context.Background(), call, schema, ws, NewPolicy(WORKSPACE_WRITE))
		if de != nil {
			t.Fatalf("Authorize(%s, WS_WRITE, in-ws) = %v, want nil", toolName, de)
		}
	}
}

// TestAuthorize_PolicyWORKSPACE_WRITE_RejectsEscape_CONFIRMS_PATH_LAYER:
// the WORKSPACE_WRITE mode's escape detection is a SECOND line of
// defense. The primary escape detection is the path-normalization
// step in Authorize itself (step 2), which catches every form the
// normalizer recognizes (parent_traversal, absolute_path, symlink_
// escape). The policy step's escape detection matters only for paths
// the path step has already approved — which is rare in practice
// (today there's no such case) — but the contract is the
// TestPolicy_WORKSPACE_WRITE_RejectsEscape test in policy_test.go
// that exercises Policy.Decide directly.
//
// This test documents the layered behavior: a call whose path
// escapes the workspace is rejected at the PATH stage by the
// normalizer, NOT at the policy stage. The policy stage's escape
// detection is enforced when the workspace normalizer was bypassed
// (e.g. an injected Authorize without the path step) — a failure
// mode the test fixtures do not exercise.
func TestAuthorize_PolicyWORKSPACE_WRITE_RejectsEscape_CONFIRMS_PATH_LAYER(t *testing.T) {
	ws := tempWorkspace(t)
	schema := tools.Schema{
		Required:   []string{"path"},
		Properties: map[string]tools.PropertyType{"path": tools.TypeString},
	}
	for toolName := range mutationTools {
		call := tools.Call{
			Name:      toolName,
			Arguments: map[string]any{"path": "../escape"},
		}
		de := Authorize(context.Background(), call, schema, ws, NewPolicy(WORKSPACE_WRITE))
		if de == nil {
			t.Fatalf("Authorize(%s, WS_WRITE, escape) = nil, want *DecisionError", toolName)
		}
		// The path stage catches the escape FIRST (it's part of
		// SCOPE §13's "schema → path → policy" pipeline order).
		// The policy stage never runs in this scenario — the
		// layered defense means the policy stage's escape
		// detection is exercised via the direct Policy.Decide test
		// in policy_test.go, not through Authorize.
		if de.Stage != "path" {
			t.Fatalf("Authorize(%s, WS_WRITE, escape) Stage = %q, want %q (path stage catches escape first)",
				toolName, de.Stage, "path")
		}
		if de.Reason != path.ReasonParentTraversal {
			t.Fatalf("Authorize(%s, WS_WRITE, escape) Reason = %q, want %q",
				toolName, de.Reason, path.ReasonParentTraversal)
		}
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