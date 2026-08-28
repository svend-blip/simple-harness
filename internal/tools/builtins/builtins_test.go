package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestRegisterBuiltins_RegistersBothTools: a fresh registry +
// RegisterBuiltins lists both tools, sorted, before any pipeline
// integration.
func TestRegisterBuiltins_RegistersBothTools(t *testing.T) {
	r := tools.NewRegistry()
	RegisterBuiltins(r)

	names := r.Names()
	want := []string{"list_directory", "read_file"}
	if len(names) != len(want) {
		t.Fatalf("len(names) = %d, want %d (got %v)", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestRegisterBuiltins_MetaAndSchema: each registered tool exposes
// a non-empty Meta() Name and a Schema() with the documented
// Required fields. The schemas are the contract; the foundation's
// validator pins the wire shape end-to-end.
func TestRegisterBuiltins_MetaAndSchema(t *testing.T) {
	r := tools.NewRegistry()
	RegisterBuiltins(r)

	rf, ok := r.Get("read_file")
	if !ok || rf == nil {
		t.Fatalf("Get(read_file) = (%v, %v), want (non-nil, true)", rf, ok)
	}
	if rf.Meta().Name != "read_file" {
		t.Fatalf("read_file Meta().Name = %q, want %q", rf.Meta().Name, "read_file")
	}
	if rf.Meta().Description == "" {
		t.Fatalf("read_file Meta().Description is empty")
	}
	s := rf.Schema()
	if len(s.Required) != 1 || s.Required[0] != "path" {
		t.Fatalf("read_file Schema.Required = %v, want [path]", s.Required)
	}
	if string(s.Properties["path"]) != string(tools.TypeString) {
		t.Fatalf("read_file path type = %q, want %q",
			s.Properties["path"], tools.TypeString)
	}

	ld, ok := r.Get("list_directory")
	if !ok || ld == nil {
		t.Fatalf("Get(list_directory) = (%v, %v), want (non-nil, true)", ld, ok)
	}
	if ld.Meta().Name != "list_directory" {
		t.Fatalf("list_directory Meta().Name = %q, want %q", ld.Meta().Name, "list_directory")
	}
	if ld.Meta().Description == "" {
		t.Fatalf("list_directory Meta().Description is empty")
	}
	s = ld.Schema()
	if len(s.Required) != 1 || s.Required[0] != "path" {
		t.Fatalf("list_directory Schema.Required = %v, want [path]", s.Required)
	}
	if string(s.Properties["path"]) != string(tools.TypeString) {
		t.Fatalf("list_directory path type = %q, want %q",
			s.Properties["path"], tools.TypeString)
	}
}

// tempWorkspace constructs a path.Workspace rooted at a fresh
// t.TempDir(). Mirrors the helper in internal/tools/dispatch_test.go
// without importing it (cross-package test helpers are not shared in
// this codebase).
func tempWorkspace(t *testing.T) path.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := path.New(dir)
	if err != nil {
		t.Fatalf("path.New(%q): %v", dir, err)
	}
	return ws
}

// chdirWorkspace changes the process's working directory to ws.Root()
// for the duration of the test. The original cwd is restored on
// cleanup. This mirrors t.Chdir (added in Go 1.24) for projects that
// pin go.mod to an older minor version.
//
// The integration tests rely on the cwd = workspace assumption
// because the foundation's path stage validates but does not rewrite
// path arguments; the tool's os.Open on a relative path resolves
// against cwd, which matches the production assumption that
// simple-harness is launched from the project directory.
func chdirWorkspace(t *testing.T, ws path.Workspace) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws.Root()); err != nil {
		t.Fatalf("chdir %s: %v", ws.Root(), err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("chdir restore %s: %v", orig, err)
		}
	})
}

// TestReadFile_Integration_Dispatch_PassesAll: a real workspace
// with one 3-line file. The integration test calls
// reg.Dispatch(ctx, call, ws, pol, perm.Authorize) directly — the
// pipeline (schema → path → policy → execute) runs end-to-end and
// the tool's Execute is reached. Result.Status is "ok" with
// size_bytes matching the file's length.
//
// The test t.Chdir's into the workspace root before Dispatch so
// the tool's os.Open on the relative path "test.txt" resolves to
// the workspace (matching the production assumption that cwd =
// workspace when simple-harness is launched from the project dir).
// t.Chdir restores the original cwd on cleanup.
func TestReadFile_Integration_Dispatch_PassesAll(t *testing.T) {
	ws := tempWorkspace(t)
	full := filepath.Join(ws.Root(), "test.txt")
	if err := os.WriteFile(full, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write test.txt: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": "test.txt"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr, ok := res.Content.(ReadFileResult)
	if !ok {
		t.Fatalf("Content type = %T, want ReadFileResult", res.Content)
	}
	if rfr.SizeBytes != int64(len("a\nb\nc\n")) {
		t.Fatalf("SizeBytes = %d, want %d", rfr.SizeBytes, len("a\nb\nc\n"))
	}
	if rfr.Content != "a\nb\nc" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "a\nb\nc")
	}
}

// TestListDirectory_Integration_Dispatch_PassesAll: a real
// workspace with one file. Dispatch through the full pipeline
// reaches the tool with a non-empty entries slice.
//
// t.Chdir to the workspace root so the relative path "." resolves
// to the workspace (matching the production assumption).
func TestListDirectory_Integration_Dispatch_PassesAll(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws.Root(), "only.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write only.txt: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "list_directory",
		Arguments: map[string]any{"path": "."},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	m, ok := res.Content.(map[string]any)
	if !ok {
		t.Fatalf("Content type = %T, want map[string]any", res.Content)
	}
	entries, ok := m["entries"].([]ListDirectoryEntry)
	if !ok {
		t.Fatalf("entries type = %T, want []ListDirectoryEntry (entries=%v)",
			m["entries"], m["entries"])
	}
	if len(entries) == 0 {
		t.Fatalf("entries is empty, want non-empty")
	}
}

// TestIntegration_SchemaViolation_SchemaFirst: a call with a schema
// violation (start_line is a string, not an int). The schema check
// fires FIRST — the path and policy stages never run. The load-
// bearing assertion is the pipeline order, not the specific
// schema error. (t.Chdir keeps the path-resolution honest for the
// "happy-path" branch of the test.)
func TestIntegration_SchemaViolation_SchemaFirst(t *testing.T) {
	ws := tempWorkspace(t)
	full := filepath.Join(ws.Root(), "test.txt")
	if err := os.WriteFile(full, []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write test.txt: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name: "read_file",
		Arguments: map[string]any{
			"path":       "test.txt",
			"start_line": "not-an-int",
		},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want \"schema_violation\"", res.Error)
	}
}

// TestIntegration_PathEscape_PathSecond: a call with a VALID schema
// but a path escape. The schema check passes; the path check
// fires. Result.Kind is "path_escape" (Dispatch maps the
// pipeline's "path" stage to the external "path_escape" Kind).
func TestIntegration_PathEscape_PathSecond(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": "../escape.txt"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want \"path_escape\"", res.Error)
	}
}

// TestIntegration_UnknownTool_Rejects: a call to a tool that was
// not registered. Dispatch's first step ("Get the tool by name")
// fails. Result.Kind is "unknown_tool".
//
// This is the Run-003-correct load-bearing assertion at this
// layer (the equivalent internal/perm test was substituted during
// handoff 013; the equivalent behavior is pinned here for the new
// tools).
func TestIntegration_UnknownTool_Rejects(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "no_such_tool",
		Arguments: map[string]any{},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "unknown_tool" {
		t.Fatalf("Error.Kind = %v, want \"unknown_tool\"", res.Error)
	}
	if !strings.Contains(res.Error.Message, "no_such_tool") {
		t.Fatalf("Error.Message = %q, want it to name the missing tool",
			res.Error.Message)
	}
}

// TestIntegration_BinaryRejection_ViaDispatch: a workspace with a
// binary file (NUL in the first 8 KiB). Dispatch runs the full
// pipeline; the schema and path stages pass; the tool's binary
// probe fires. Result.Kind is "binary_file".
//
// t.Chdir to the workspace root so the relative path "binary.bin"
// resolves to the binary file we created.
func TestIntegration_BinaryRejection_ViaDispatch(t *testing.T) {
	ws := tempWorkspace(t)
	full := filepath.Join(ws.Root(), "binary.bin")
	if err := os.WriteFile(full, []byte("hello\x00world\n"), 0o644); err != nil {
		t.Fatalf("write binary.bin: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "read_file",
		Arguments: map[string]any{"path": "binary.bin"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPermissive(), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "binary_file" {
		t.Fatalf("Error.Kind = %v, want \"binary_file\"", res.Error)
	}
}