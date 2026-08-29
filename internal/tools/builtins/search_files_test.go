package builtins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// searchFilesContent pulls the structured SearchFilesResult out of
// a successful Result.Content. Tests call this to assert the wire
// shape without re-implementing the type assertion in every test.
func searchFilesContent(t *testing.T, res tools.Result) SearchFilesResult {
	t.Helper()
	sfr, ok := res.Content.(SearchFilesResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want SearchFilesResult (content=%v)",
			res.Content, res.Content)
	}
	return sfr
}

// TestSearchFiles_HappyPath_Substring: a workspace with 5 files
// (3 .txt + 1 .go + 1 .md); pattern="txt" returns the 3 .txt files,
// sorted alphabetically.
func TestSearchFiles_HappyPath_Substring(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"alpha.txt":   "x",
		"beta.txt":    "x",
		"gamma.go":    "x",
		"delta.txt":   "x",
		"epsilon.md":  "x",
	}
	for name := range files {
		writeFile(t, dir, name, []byte(files[name]))
	}

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "txt",
		"path":    dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr := searchFilesContent(t, res)
	want := []string{"alpha.txt", "beta.txt", "delta.txt"}
	if len(sfr.Files) != len(want) {
		t.Fatalf("len(Files) = %d, want %d (files=%v)", len(sfr.Files), len(want), sfr.Files)
	}
	for i := range want {
		if sfr.Files[i] != want[i] {
			t.Fatalf("Files[%d] = %q, want %q (full=%v)", i, sfr.Files[i], want[i], sfr.Files)
		}
	}
	if sfr.Pattern != "txt" {
		t.Fatalf("Pattern = %q, want %q", sfr.Pattern, "txt")
	}
	if sfr.Path != dir {
		t.Fatalf("Path = %q, want %q", sfr.Path, dir)
	}
}

// TestSearchFiles_NoMatches: a workspace whose files contain no
// substring match. Result.Status is "ok" and Files is a non-nil
// empty slice (not nil; JSON encodes `[]` not `null`).
func TestSearchFiles_NoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "alpha.go", []byte("package x"))
	writeFile(t, dir, "beta.go", []byte("package x"))

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "nope",
		"path":    dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr := searchFilesContent(t, res)
	if sfr.Files == nil {
		t.Fatalf("Files is nil, want empty slice (no-match seam choice)")
	}
	if len(sfr.Files) != 0 {
		t.Fatalf("len(Files) = %d, want 0 (files=%v)", len(sfr.Files), sfr.Files)
	}
}

// TestSearchFiles_NestedSubdirectory: a workspace with files in a
// subdirectory. The walk is recursive; paths are RELATIVE TO THE
// CALL'S path argument (so a file at "subdir/inner.txt" is returned
// as "subdir/inner.txt" when path="." and as "inner.txt" when
// path="subdir").
func TestSearchFiles_NestedSubdirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}
	writeFile(t, dir, "top.txt", []byte("x"))
	writeFile(t, subdir, "inner.txt", []byte("x"))

	// Case 1: path = "." (workspace root) — matches are
	// "subdir/inner.txt" and "top.txt".
	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "txt",
		"path":    dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute (root): %v", err)
	}
	sfr := searchFilesContent(t, res)
	wantRoot := []string{"subdir/inner.txt", "top.txt"}
	if len(sfr.Files) != len(wantRoot) {
		t.Fatalf("len(Files) = %d, want %d (files=%v)", len(sfr.Files), len(wantRoot), sfr.Files)
	}
	for i := range wantRoot {
		if sfr.Files[i] != wantRoot[i] {
			t.Fatalf("Files[%d] = %q, want %q (full=%v)", i, sfr.Files[i], wantRoot[i], sfr.Files)
		}
	}

	// Case 2: path = "subdir" — only inner.txt matches, returned
	// as "inner.txt" (relative to subdir, not to dir).
	call2 := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "txt",
		"path":    subdir,
	}}
	res2, err := sf.Execute(context.Background(), call2)
	if err != nil {
		t.Fatalf("Execute (subdir): %v", err)
	}
	sfr2 := searchFilesContent(t, res2)
	if len(sfr2.Files) != 1 || sfr2.Files[0] != "inner.txt" {
		t.Fatalf("Files = %v, want [inner.txt]", sfr2.Files)
	}
}

// TestSearchFiles_DotGitSkipped: a workspace with a .git directory
// containing "secret.txt". The pattern matches "secret" but the
// .git directory is excluded by the walk; "secret.txt" is NOT in
// the result.
func TestSearchFiles_DotGitSkipped(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	writeFile(t, gitDir, "secret.txt", []byte("do not leak"))
	// Also a top-level match so the result is non-empty (proves
	// the walk ran past .git).
	writeFile(t, dir, "visible_secret.txt", []byte("x"))

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "secret",
		"path":    dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr := searchFilesContent(t, res)
	if len(sfr.Files) != 1 || sfr.Files[0] != "visible_secret.txt" {
		t.Fatalf("Files = %v, want [visible_secret.txt] (the .git/secret.txt must be excluded)", sfr.Files)
	}
}

// TestSearchFiles_HiddenFileIncluded: a workspace with a dotfile
// outside .git (".hidden"). The pattern matches "hidden"; the file
// IS in the result (dotfiles are included; only the .git directory
// is special).
func TestSearchFiles_HiddenFileIncluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".hidden", []byte("x"))
	writeFile(t, dir, "visible.txt", []byte("x"))

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "hidden",
		"path":    dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr := searchFilesContent(t, res)
	if len(sfr.Files) != 1 || sfr.Files[0] != ".hidden" {
		t.Fatalf("Files = %v, want [.hidden] (dotfiles outside .git are included)", sfr.Files)
	}
}

// TestSearchFiles_MaxResultsTruncates: a workspace with 5 matching
// files; max_results=2 truncates the result to 2 entries (the first
// 2 in alphabetical order).
func TestSearchFiles_MaxResultsTruncates(t *testing.T) {
	dir := t.TempDir()
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	for _, n := range names {
		writeFile(t, dir, n, []byte("x"))
	}

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern":     "txt",
		"path":        dir,
		"max_results": 2,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sfr := searchFilesContent(t, res)
	if len(sfr.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2 (max_results=2 truncates; got %v)", len(sfr.Files), sfr.Files)
	}
	if sfr.Files[0] != "a.txt" || sfr.Files[1] != "b.txt" {
		t.Fatalf("Files = %v, want [a.txt b.txt] (first 2 alphabetically)", sfr.Files)
	}
}

// TestSearchFiles_NotADirectory: a call whose path is a file
// (not a directory) returns Kind="not_a_directory".
func TestSearchFiles_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.txt")
	writeFile(t, dir, "file.txt", []byte("x"))

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "txt",
		"path":    filePath,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "not_a_directory" {
		t.Fatalf("Error.Kind = %v, want \"not_a_directory\"", res.Error)
	}
}

// TestSearchFiles_NotFound: a call whose path does not exist
// returns Kind="not_found".
func TestSearchFiles_NotFound(t *testing.T) {
	dir := t.TempDir()

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "txt",
		"path":    filepath.Join(dir, "no-such-dir"),
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "not_found" {
		t.Fatalf("Error.Kind = %v, want \"not_found\"", res.Error)
	}
}

// TestSearchFiles_MissingPattern: a call without the required
// pattern argument returns a structured error (Kind="not_found"
// per the seam choice — the missing pattern is treated like a
// missing path: "what do you want me to search for? not found.").
// The schema validator catches this in the dispatch path; the
// defensive guard keeps direct-Execute callers honest.
func TestSearchFiles_MissingPattern(t *testing.T) {
	dir := t.TempDir()

	sf := SearchFiles{}
	call := tools.Call{Name: "search_files", Arguments: map[string]any{
		"path": dir,
	}}
	res, err := sf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("Error = nil, want structured error")
	}
}