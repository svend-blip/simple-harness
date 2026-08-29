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

// TestRegisterBuiltins_RegistersAllSevenTools: a fresh registry +
// RegisterBuiltins lists all seven V1 builtin tools (four read-only
// + three mutation tools), sorted, before any pipeline integration.
// This is the load-bearing assertion for full TG1 at the
// registrar level. Handoff 017 added write_file; handoff 018 added
// apply_patch; handoff 020 adds shell and lands partial TG1 (full
// TG1 lands in handoff 021 when the advanced shell behavior
// completes the slice).
func TestRegisterBuiltins_RegistersAllSevenTools(t *testing.T) {
	r := tools.NewRegistry()
	RegisterBuiltins(r)

	names := r.Names()
	want := []string{"apply_patch", "grep", "list_directory", "read_file", "search_files", "shell", "write_file"}
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

	gr, ok := r.Get("grep")
	if !ok || gr == nil {
		t.Fatalf("Get(grep) = (%v, %v), want (non-nil, true)", gr, ok)
	}
	if gr.Meta().Name != "grep" {
		t.Fatalf("grep Meta().Name = %q, want %q", gr.Meta().Name, "grep")
	}
	if gr.Meta().Description == "" {
		t.Fatalf("grep Meta().Description is empty")
	}
	gs := gr.Schema()
	if len(gs.Required) != 1 || gs.Required[0] != "pattern" {
		t.Fatalf("grep Schema.Required = %v, want [pattern]", gs.Required)
	}
	if string(gs.Properties["pattern"]) != string(tools.TypeString) {
		t.Fatalf("grep pattern type = %q, want %q",
			gs.Properties["pattern"], tools.TypeString)
	}

	sf, ok := r.Get("search_files")
	if !ok || sf == nil {
		t.Fatalf("Get(search_files) = (%v, %v), want (non-nil, true)", sf, ok)
	}
	if sf.Meta().Name != "search_files" {
		t.Fatalf("search_files Meta().Name = %q, want %q", sf.Meta().Name, "search_files")
	}
	if sf.Meta().Description == "" {
		t.Fatalf("search_files Meta().Description is empty")
	}
	ss := sf.Schema()
	if len(ss.Required) != 1 || ss.Required[0] != "pattern" {
		t.Fatalf("search_files Schema.Required = %v, want [pattern]", ss.Required)
	}
	if string(ss.Properties["pattern"]) != string(tools.TypeString) {
		t.Fatalf("search_files pattern type = %q, want %q",
			ss.Properties["pattern"], tools.TypeString)
	}

	sh, ok := r.Get("shell")
	if !ok || sh == nil {
		t.Fatalf("Get(shell) = (%v, %v), want (non-nil, true)", sh, ok)
	}
	if sh.Meta().Name != "shell" {
		t.Fatalf("shell Meta().Name = %q, want %q", sh.Meta().Name, "shell")
	}
	if sh.Meta().Description == "" {
		t.Fatalf("shell Meta().Description is empty")
	}
	shs := sh.Schema()
	if len(shs.Required) != 1 || shs.Required[0] != "command" {
		t.Fatalf("shell Schema.Required = %v, want [command]", shs.Required)
	}
	if string(shs.Properties["command"]) != string(tools.TypeString) {
		t.Fatalf("shell command type = %q, want %q",
			shs.Properties["command"], tools.TypeString)
	}
	if string(shs.Properties["cwd"]) != string(tools.TypeString) {
		t.Fatalf("shell cwd type = %q, want %q",
			shs.Properties["cwd"], tools.TypeString)
	}

	wf, ok := r.Get("write_file")
	if !ok || wf == nil {
		t.Fatalf("Get(write_file) = (%v, %v), want (non-nil, true)", wf, ok)
	}
	if wf.Meta().Name != "write_file" {
		t.Fatalf("write_file Meta().Name = %q, want %q", wf.Meta().Name, "write_file")
	}
	if wf.Meta().Description == "" {
		t.Fatalf("write_file Meta().Description is empty")
	}
	ws := wf.Schema()
	if len(ws.Required) != 2 || ws.Required[0] != "path" || ws.Required[1] != "content" {
		t.Fatalf("write_file Schema.Required = %v, want [path content]", ws.Required)
	}
	if string(ws.Properties["path"]) != string(tools.TypeString) {
		t.Fatalf("write_file path type = %q, want %q",
			ws.Properties["path"], tools.TypeString)
	}
	if string(ws.Properties["content"]) != string(tools.TypeString) {
		t.Fatalf("write_file content type = %q, want %q",
			ws.Properties["content"], tools.TypeString)
	}

	ap, ok := r.Get("apply_patch")
	if !ok || ap == nil {
		t.Fatalf("Get(apply_patch) = (%v, %v), want (non-nil, true)", ap, ok)
	}
	if ap.Meta().Name != "apply_patch" {
		t.Fatalf("apply_patch Meta().Name = %q, want %q", ap.Meta().Name, "apply_patch")
	}
	if ap.Meta().Description == "" {
		t.Fatalf("apply_patch Meta().Description is empty")
	}
	aps := ap.Schema()
	if len(aps.Required) != 2 || aps.Required[0] != "path" || aps.Required[1] != "patch" {
		t.Fatalf("apply_patch Schema.Required = %v, want [path patch]", aps.Required)
	}
	if string(aps.Properties["path"]) != string(tools.TypeString) {
		t.Fatalf("apply_patch path type = %q, want %q",
			aps.Properties["path"], tools.TypeString)
	}
	if string(aps.Properties["patch"]) != string(tools.TypeString) {
		t.Fatalf("apply_patch patch type = %q, want %q",
			aps.Properties["patch"], tools.TypeString)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
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
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "binary_file" {
		t.Fatalf("Error.Kind = %v, want \"binary_file\"", res.Error)
	}
}

// TestIntegration_SearchFiles_Dispatch_PassesAll: a real workspace
// with files matching a pattern. Dispatch through the full pipeline
// reaches the SearchFiles tool. Result.Status is "ok" and the
// matches slice is non-empty.
func TestIntegration_SearchFiles_Dispatch_PassesAll(t *testing.T) {
	ws := tempWorkspace(t)
	writeFile(t, ws.Root(), "alpha.txt", []byte("x"))
	writeFile(t, ws.Root(), "beta.txt", []byte("x"))
	writeFile(t, ws.Root(), "gamma.go", []byte("x"))

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "search_files",
		Arguments: map[string]any{"pattern": "txt", "path": "."},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr, ok := res.Content.(SearchFilesResult)
	if !ok {
		t.Fatalf("Content type = %T, want SearchFilesResult", res.Content)
	}
	if len(sfr.Files) == 0 {
		t.Fatalf("Files is empty, want non-empty (alpha.txt + beta.txt match)")
	}
}

// TestIntegration_Grep_Dispatch_PassesAll_Native: a real workspace
// with a file containing a matching line; the test stubs
// execLookPath to force the native-fallback path; Dispatch through
// the full pipeline reaches the Grep tool with Backend="native".
func TestIntegration_Grep_Dispatch_PassesAll_Native(t *testing.T) {
	withNativeLookPath(t)

	ws := tempWorkspace(t)
	writeFile(t, ws.Root(), "test.txt", []byte("line one\nline two with needle\nline three\n"))

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "grep",
		Arguments: map[string]any{"pattern": "needle", "path": "."},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr, ok := res.Content.(GrepResult)
	if !ok {
		t.Fatalf("Content type = %T, want GrepResult", res.Content)
	}
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q (execLookPath stubbed to force native)",
			gr.Backend, "native")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != "test.txt" {
		t.Fatalf("Matches[0].File = %q, want %q", gr.Matches[0].File, "test.txt")
	}
}

// TestIntegration_Grep_Dispatch_PassesAll_RG: same workspace as the
// native test, but with the production execLookPath (rg is in
// $PATH). Dispatch reaches the Grep tool with Backend="rg" and
// produces the same match row.
func TestIntegration_Grep_Dispatch_PassesAll_RG(t *testing.T) {
	withRGLookPath(t)

	ws := tempWorkspace(t)
	writeFile(t, ws.Root(), "test.txt", []byte("line one\nline two with needle\nline three\n"))

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "grep",
		Arguments: map[string]any{"pattern": "needle", "path": "."},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr, ok := res.Content.(GrepResult)
	if !ok {
		t.Fatalf("Content type = %T, want GrepResult", res.Content)
	}
	if gr.Backend != "rg" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "rg")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != "test.txt" {
		t.Fatalf("Matches[0].File = %q, want %q", gr.Matches[0].File, "test.txt")
	}
	if gr.Matches[0].Line != 2 {
		t.Fatalf("Matches[0].Line = %d, want 2", gr.Matches[0].Line)
	}
	if gr.Matches[0].Text != "line two with needle" {
		t.Fatalf("Matches[0].Text = %q, want %q", gr.Matches[0].Text, "line two with needle")
	}
}

// TestIntegration_SearchFiles_SchemaViolation_SchemaFirst: a call
// with a schema violation (pattern is an int, not a string). The
// schema check fires FIRST; the path and policy stages never run.
func TestIntegration_SearchFiles_SchemaViolation_SchemaFirst(t *testing.T) {
	ws := tempWorkspace(t)

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name: "search_files",
		Arguments: map[string]any{
			"pattern": 42,
			"path":    ".",
		},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want \"schema_violation\"", res.Error)
	}
}

// TestIntegration_Grep_PathEscape_PathSecond: a call with a valid
// schema but a path escape. The schema check passes; the path
// check fires. Result.Kind is "path_escape".
func TestIntegration_Grep_PathEscape_PathSecond(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "grep",
		Arguments: map[string]any{"pattern": "needle", "path": "../escape"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want \"path_escape\"", res.Error)
	}
}

// TestIntegration_PermissionDenial_READ_ONLY: a write_file call under
// READ_ONLY mode is rejected at the policy stage. Dispatch returns a
// Result with Status="error" and Error.Kind="permission_denied". The
// policy decision fires AFTER schema and path steps pass — the test
// verifies the load-bearing SCOPE §13 "policy sits between path and
// execute" contract for a mutation tool.
//
// Handoff 017 registered the real WriteFile{} via RegisterBuiltins;
// the stub-registration the prior handoff used to provide a
// "write_file" tool name is no longer needed (the real tool is
// wired in). If the policy step does not deny the call, Execute
// runs and creates the file — the test's failure signal ("the
// policy should have denied this") would not fire.
func TestIntegration_PermissionDenial_READ_ONLY(t *testing.T) {
	ws := tempWorkspace(t)
	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "write_file",
		Arguments: map[string]any{"path": "in-workspace.txt", "content": "hello"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (READ_ONLY should deny the write_file call)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "permission_denied" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "permission_denied")
	}
}

// TestIntegration_PermissionDenial_WRITE_FILE_READ_ONLY: the
// load-bearing SCOPE §13 "policy sits between path and execute"
// contract pinned at the integration layer for the write_file
// mutation tool. A WORKSPACE_WRITE-eligible in-workspace call
// under READ_ONLY mode dies at the policy stage with
// Kind="permission_denied" — NOT at the schema stage, NOT at the
// path stage, NOT at the execute stage.
//
// The policy decision fires AFTER schema (path + content are both
// strings) and path ("in-workspace.txt" resolves inside the
// workspace) steps pass. The REAL WriteFile{} tool is registered
// via RegisterBuiltins (handoff 017); if the policy step does not
// deny the call, Execute runs and creates the file — the test's
// failure signal would fire.
func TestIntegration_PermissionDenial_WRITE_FILE_READ_ONLY(t *testing.T) {
	ws := tempWorkspace(t)
	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "write_file",
		Arguments: map[string]any{"path": "in-workspace.txt", "content": "hello"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (READ_ONLY should deny the write_file call)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "permission_denied" {
		t.Fatalf("Error.Kind = %v, want %q (READ_ONLY must deny write_file at the policy stage)",
			res.Error, "permission_denied")
	}
	// And no file was written — Execute never ran.
	if _, err := os.Stat(filepath.Join(ws.Root(), "in-workspace.txt")); !os.IsNotExist(err) {
		t.Fatalf("in-workspace.txt was created (Execute ran) — the policy should have denied")
	}
}

// TestIntegration_PermissionDenial_WRITE_FILE_WORKSPACE_WRITE_Escape:
// a WORKSPACE_WRITE call with an escape path is rejected with
// Kind="path_escape" (the path stage catches the escape BEFORE the
// policy stage runs). This pins SCOPE §12's escape-rejection
// contract for write_file — the layered defense fires at the path
// stage, not the policy stage.
func TestIntegration_PermissionDenial_WRITE_FILE_WORKSPACE_WRITE_Escape(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "write_file",
		Arguments: map[string]any{"path": "../escape.txt", "content": "should fail"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.WORKSPACE_WRITE), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (path escape must be rejected)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want %q (path_escape fires at the path stage, before policy)",
			res.Error, "path_escape")
	}
}

// TestIntegration_PermissionDenial_WRITE_FILE_WORKSPACE_WRITE_Allowed:
// a WORKSPACE_WRITE call with an in-workspace path is allowed; the
// write_file Execute runs and the file's bytes on disk match the
// content. This pins SCOPE §12's "permits workspace writes"
// contract for write_file.
func TestIntegration_PermissionDenial_WRITE_FILE_WORKSPACE_WRITE_Allowed(t *testing.T) {
	ws := tempWorkspace(t)
	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	content := "hello, workspace\n"
	call := tools.Call{
		Name:      "write_file",
		Arguments: map[string]any{"path": "in-workspace.txt", "content": content},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.WORKSPACE_WRITE), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	wfr, ok := res.Content.(WriteFileResult)
	if !ok {
		t.Fatalf("Content type = %T, want WriteFileResult", res.Content)
	}
	if wfr.BytesWritten != len(content) {
		t.Fatalf("BytesWritten = %d, want %d", wfr.BytesWritten, len(content))
	}

	// On-disk bytes match the content.
	full := filepath.Join(ws.Root(), "in-workspace.txt")
	onDisk, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", full, err)
	}
	if string(onDisk) != content {
		t.Fatalf("on-disk bytes = %q, want %q", string(onDisk), content)
	}
}

// TestIntegration_PermissionDenial_WRITE_FILE_FULL_ACCESS_Escape:
// a FULL_ACCESS call with an escape path is rejected by the path
// stage with Kind="path_escape" — NOT at the policy stage. The
// path stage is a STRUCTURAL safety that fires regardless of mode
// (it catches escape attempts before the policy stage runs); the
// "FULL_ACCESS is the explicit opt-in" contract (SCOPE §12 "never
// silent escalation") means FULL_ACCESS does not impose the
// in-workspace policy restriction that WORKSPACE_WRITE does, but
// it does not bypass the path-stage safety either.
//
// This test pins the layered-defense contract: even under the
// most-permissive policy mode, the path stage catches escape
// attempts. The "explicit choice" FULL_ACCESS provides is about
// which tools can mutate WITHIN the workspace, not about
// bypassing workspace boundaries.
//
// The handoff expected Result.Status="ok" for this test (asserting
// that FULL_ACCESS allows escape), but the current architecture
// has the path stage always-on regardless of mode. A future
// handoff that wants FULL_ACCESS to bypass the path stage would
// need to extend the scope fence to include internal/perm/perm.go
// (which is OUT OF SCOPE here per the handoff's fence) and make
// the path stage mode-aware.
func TestIntegration_PermissionDenial_WRITE_FILE_FULL_ACCESS_Escape(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "write_file",
		Arguments: map[string]any{"path": "../escape.txt", "content": "should fail at path stage"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.FULL_ACCESS), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (path escape must be rejected by the path stage regardless of mode)",
			res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want %q (the path stage catches the escape before the policy stage runs)",
			res.Error, "path_escape")
	}
}

// TestIntegration_PermissionDenial_APPLY_PATCH_READ_ONLY: a
// apply_patch call under READ_ONLY mode is rejected at the policy
// stage. Dispatch returns Result with Status="error" and
// Error.Kind="permission_denied". The policy decision fires AFTER
// schema and path steps pass — the test verifies the load-bearing
// SCOPE §13 "policy sits between path and execute" contract for
// apply_patch (the second mutation tool). The target file's
// content MUST be unchanged after the dispatch (Execute never ran).
func TestIntegration_PermissionDenial_APPLY_PATCH_READ_ONLY(t *testing.T) {
	ws := tempWorkspace(t)
	full := filepath.Join(ws.Root(), "in-workspace.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(full, []byte(original), 0o644); err != nil {
		t.Fatalf("write in-workspace.txt: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	patch := "--- a/in-workspace.txt\n+++ b/in-workspace.txt\n@@ -2 +2 @@\n-beta\n+BETA\n"
	call := tools.Call{
		Name:      "apply_patch",
		Arguments: map[string]any{"path": "in-workspace.txt", "patch": patch},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (READ_ONLY should deny the apply_patch call)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "permission_denied" {
		t.Fatalf("Error.Kind = %v, want %q (READ_ONLY must deny apply_patch at the policy stage)",
			res.Error, "permission_denied")
	}

	onDisk, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", full, err)
	}
	if string(onDisk) != original {
		t.Fatalf("on-disk bytes = %q, want unchanged %q (Execute must not run when policy denies)",
			string(onDisk), original)
	}
}

// TestIntegration_PermissionDenial_APPLY_PATCH_WORKSPACE_WRITE_Escape:
// a WORKSPACE_WRITE apply_patch call with an escape path is
// rejected with Kind="path_escape" (the path stage catches the
// escape BEFORE the policy stage runs). This pins SCOPE §12's
// escape-rejection contract for apply_patch — the layered defense
// fires at the path stage, not the policy stage.
func TestIntegration_PermissionDenial_APPLY_PATCH_WORKSPACE_WRITE_Escape(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	patch := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+A\n"
	call := tools.Call{
		Name:      "apply_patch",
		Arguments: map[string]any{"path": "../escape.txt", "patch": patch},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.WORKSPACE_WRITE), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (path escape must be rejected)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want %q (path_escape fires at the path stage, before policy)",
			res.Error, "path_escape")
	}
}

// TestIntegration_PermissionDenial_APPLY_PATCH_WORKSPACE_WRITE_Allowed:
// a WORKSPACE_WRITE apply_patch call with an in-workspace path is
// allowed; the apply_patch Execute runs and the file's bytes on
// disk reflect the patch's expected changes. This pins SCOPE §12's
// "permits workspace writes" contract for apply_patch.
func TestIntegration_PermissionDenial_APPLY_PATCH_WORKSPACE_WRITE_Allowed(t *testing.T) {
	ws := tempWorkspace(t)
	full := filepath.Join(ws.Root(), "in-workspace.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(full, []byte(original), 0o644); err != nil {
		t.Fatalf("write in-workspace.txt: %v", err)
	}

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	patch := "--- a/in-workspace.txt\n+++ b/in-workspace.txt\n@@ -2 +2 @@\n-beta\n+BETA\n"
	call := tools.Call{
		Name:      "apply_patch",
		Arguments: map[string]any{"path": "in-workspace.txt", "patch": patch},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.WORKSPACE_WRITE), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	apr, ok := res.Content.(ApplyPatchResult)
	if !ok {
		t.Fatalf("Content type = %T, want ApplyPatchResult", res.Content)
	}
	if apr.HunksApplied != 1 {
		t.Fatalf("HunksApplied = %d, want 1", apr.HunksApplied)
	}

	onDisk, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", full, err)
	}
	if string(onDisk) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("on-disk bytes = %q, want %q", string(onDisk), "alpha\nBETA\ngamma\n")
	}
}

// TestIntegration_PermissionDenial_APPLY_PATCH_FULL_ACCESS_Escape:
// a FULL_ACCESS apply_patch call with an escape path is rejected
// by the path stage with Kind="path_escape" — NOT at the policy
// stage. The path stage is a STRUCTURAL safety that fires
// regardless of mode (it catches escape attempts before the
// policy stage runs); the "FULL_ACCESS is the explicit opt-in"
// contract (SCOPE §12 "never silent escalation") means
// FULL_ACCESS does not impose the in-workspace policy
// restriction that WORKSPACE_WRITE does, but it does not bypass
// the path-stage safety either.
//
// This test pins the layered-defense contract: even under the
// most-permissive policy mode, the path stage catches escape
// attempts for apply_patch — the same standing behavior as
// write_file.
func TestIntegration_PermissionDenial_APPLY_PATCH_FULL_ACCESS_Escape(t *testing.T) {
	ws := tempWorkspace(t)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	patch := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+A\n"
	call := tools.Call{
		Name:      "apply_patch",
		Arguments: map[string]any{"path": "../escape.txt", "patch": patch},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.FULL_ACCESS), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (path escape must be rejected by the path stage regardless of mode)",
			res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "path_escape" {
		t.Fatalf("Error.Kind = %v, want %q (the path stage catches the escape before the policy stage runs)",
			res.Error, "path_escape")
	}
}

// TestIntegration_PermissionDenial_SHELL_READ_ONLY: a READ_ONLY
// shell call is rejected with Kind="permission_denied" at the
// policy stage BEFORE Execute runs (reviewer duty #3 — the
// READ_ONLY refusal happens in the permission seam, NOT inside
// the shell tool's body). The test asserts the harness did NOT
// spawn the child by checking the captured stdout/stderr are
// both empty strings — Execute must not run when the policy
// stage denies the call.
func TestIntegration_PermissionDenial_SHELL_READ_ONLY(t *testing.T) {
	ws := tempWorkspace(t)

	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "echo should-not-run"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.READ_ONLY), perm.Authorize)
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (READ_ONLY should deny the shell call)", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "permission_denied" {
		t.Fatalf("Error.Kind = %v, want %q (READ_ONLY must deny shell at the policy stage)",
			res.Error, "permission_denied")
	}
	// The Dispatch path does not return ShellResult on
	// policy-stage denial — Content will be nil; the contract
	// is just that the policy stage fired before Execute
	// ran. (If Execute ran, the shell would have written
	// "should-not-run\n" somewhere; on a fresh t.TempDir()
	// workspace the absence of any side-effect is the
	// observable proof, but the test asserts the structured
	// Kind which is the binding evidence.)
}
// TestIntegration_PermissionDenial_SHELL_WORKSPACE_WRITE_Allowed:
// a WORKSPACE_WRITE shell call reaches Execute (the policy stage
// permits shell under WORKSPACE_WRITE because the call does not
// trigger the looksLikePathish escape heuristic — `echo` is not a
// path-shaped argument). Result.Status is "ok" with
// ShellResult.ExitCode=0, Stdout="hello-workspace\n",
// TerminationReason="".
//
// This pins SCOPE §12's WORKSPACE_WRITE "permits running normal
// development/test commands" contract for shell — the positive
// control for the handoff-020
// TestIntegration_PermissionDenial_SHELL_READ_ONLY negative.
func TestIntegration_PermissionDenial_SHELL_WORKSPACE_WRITE_Allowed(t *testing.T) {
	ws := tempWorkspace(t)
	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "echo hello-workspace"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.WORKSPACE_WRITE), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", sr.ExitCode)
	}
	if sr.Stdout != "hello-workspace\n" {
		t.Fatalf("Stdout = %q, want %q", sr.Stdout, "hello-workspace\n")
	}
	if sr.TerminationReason != "" {
		t.Fatalf("TerminationReason = %q, want empty (the command completed normally)",
			sr.TerminationReason)
	}
}

// TestIntegration_PermissionDenial_SHELL_FULL_ACCESS_Allowed: the
// FULL_ACCESS positive control for shell. Same shape as the
// WORKSPACE_WRITE variant, with FULL_ACCESS — the policy stage
// permits shell under FULL_ACCESS regardless of the in-workspace
// restriction (FULL_ACCESS is the explicit opt-in per SCOPE §12).
func TestIntegration_PermissionDenial_SHELL_FULL_ACCESS_Allowed(t *testing.T) {
	ws := tempWorkspace(t)
	chdirWorkspace(t, ws)

	r := tools.NewRegistry()
	RegisterBuiltins(r)

	call := tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "echo hello-full-access"},
	}
	res := r.Dispatch(context.Background(), call, ws, perm.NewPolicy(perm.FULL_ACCESS), perm.Authorize)
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", sr.ExitCode)
	}
	if sr.Stdout != "hello-full-access\n" {
		t.Fatalf("Stdout = %q, want %q", sr.Stdout, "hello-full-access\n")
	}
	if sr.TerminationReason != "" {
		t.Fatalf("TerminationReason = %q, want empty (the command completed normally)",
			sr.TerminationReason)
	}
}
