package builtins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// listDirectoryContent pulls the structured list_directory content
// out of a successful Result.Content. The tool returns a
// map[string]any with entries and path; we assert both shapes here.
func listDirectoryContent(t *testing.T, res tools.Result) (entries []ListDirectoryEntry, path string) {
	t.Helper()
	m, ok := res.Content.(map[string]any)
	if !ok {
		t.Fatalf("Result.Content type = %T, want map[string]any (content=%v)",
			res.Content, res.Content)
	}
	rawEntries, ok := m["entries"].([]ListDirectoryEntry)
	if !ok {
		t.Fatalf("entries type = %T, want []ListDirectoryEntry (entries=%v)",
			m["entries"], m["entries"])
	}
	p, ok := m["path"].(string)
	if !ok {
		t.Fatalf("path type = %T, want string (path=%v)", m["path"], m["path"])
	}
	return rawEntries, p
}

// TestListDirectory_HappyPath: a directory with three entries — a
// 5-byte file, a subdirectory, and a hidden dotfile. The listing is
// sorted by name (".hidden" before "a.txt" before "sub"). The file
// has Type="file" and SizeBytes=5; the subdirectory has Type="dir"
// with SizeBytes omitted via the omitempty tag.
func TestListDirectory_HappyPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte{}, 0o644); err != nil {
		t.Fatalf("write .hidden: %v", err)
	}

	ld := ListDirectory{}
	call := tools.Call{Name: "list_directory", Arguments: map[string]any{
		"path": dir,
	}}
	res, err := ld.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	entries, gotPath := listDirectoryContent(t, res)
	if gotPath != dir {
		t.Fatalf("path = %q, want %q", gotPath, dir)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (got %+v)", len(entries), entries)
	}
	// Sorted order: ".hidden" < "a.txt" < "sub".
	want := []ListDirectoryEntry{
		{Name: ".hidden", Type: "file", SizeBytes: 0},
		{Name: "a.txt", Type: "file", SizeBytes: 5},
		{Name: "sub", Type: "dir"},
	}
	for i := range want {
		if entries[i].Name != want[i].Name {
			t.Fatalf("entries[%d].Name = %q, want %q", i, entries[i].Name, want[i].Name)
		}
		if entries[i].Type != want[i].Type {
			t.Fatalf("entries[%d].Type = %q, want %q", i, entries[i].Type, want[i].Type)
		}
		if entries[i].Type == "file" && entries[i].SizeBytes != want[i].SizeBytes {
			t.Fatalf("entries[%d].SizeBytes = %d, want %d",
				i, entries[i].SizeBytes, want[i].SizeBytes)
		}
		if entries[i].Type == "dir" && entries[i].SizeBytes != 0 {
			t.Fatalf("entries[%d] (dir) SizeBytes = %d, want 0 (omitempty)",
				i, entries[i].SizeBytes)
		}
	}
}

// TestListDirectory_Empty: an empty directory returns an empty
// (non-nil) entries slice and the input path.
func TestListDirectory_Empty(t *testing.T) {
	dir := t.TempDir()

	ld := ListDirectory{}
	call := tools.Call{Name: "list_directory", Arguments: map[string]any{
		"path": dir,
	}}
	res, err := ld.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	entries, gotPath := listDirectoryContent(t, res)
	if entries == nil {
		t.Fatalf("entries is nil, want empty non-nil slice")
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0 (got %+v)", len(entries), entries)
	}
	if gotPath != dir {
		t.Fatalf("path = %q, want %q", gotPath, dir)
	}
}

// TestListDirectory_NotFound: a path that does not exist is rejected
// with Kind="not_found".
func TestListDirectory_NotFound(t *testing.T) {
	dir := t.TempDir()

	ld := ListDirectory{}
	call := tools.Call{Name: "list_directory", Arguments: map[string]any{
		"path": filepath.Join(dir, "no-such-dir"),
	}}
	res, err := ld.Execute(context.Background(), call)
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

// TestListDirectory_NotADirectory: a path that resolves to a regular
// file (not a directory) is rejected with Kind="not_a_directory".
func TestListDirectory_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}

	ld := ListDirectory{}
	call := tools.Call{Name: "list_directory", Arguments: map[string]any{
		"path": file,
	}}
	res, err := ld.Execute(context.Background(), call)
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

// TestListDirectory_PathEscapeRejected_DirectExecute: the test
// calls ListDirectory.Execute directly with an absolute path that
// escapes the test's t.TempDir workspace. The tool does not
// re-normalize; the dispatch pipeline handles that. The direct-
// Execute path resolves the absolute path against the OS, where it
// exists (the OS temp dir IS a directory), so the tool returns
// Status="ok" with the listing. The pipeline path-escape test lives
// in builtins_test.go (TestIntegration_PathEscape_PathSecond) and
// asserts Kind="path_escape" via Dispatch.
//
// What this test pins: the direct-Execute path does NOT crash on
// absolute paths outside the test's workspace (the tool's contract
// is "the dispatch pipeline handles escapes"; a direct caller is
// responsible for passing a sane path). We use a small known
// directory (a sibling temp dir we create and clean up) so the
// listing is stable and non-empty.
func TestListDirectory_PathEscapeRejected_DirectExecute(t *testing.T) {
	dir := t.TempDir()
	// Create a sibling directory outside dir to act as the
	// "outside" target. We control its contents so the listing
	// is reproducible.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sibling.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write sibling.txt: %v", err)
	}

	ld := ListDirectory{}
	call := tools.Call{Name: "list_directory", Arguments: map[string]any{
		"path": outside, // absolute, outside the test's workspace
	}}
	res, err := ld.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (direct-Execute sees a real directory; the pipeline path-escape test lives elsewhere)",
			res.Status, "ok")
	}
	entries, _ := listDirectoryContent(t, res)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (got %+v)", len(entries), entries)
	}
	if entries[0].Name != "sibling.txt" {
		t.Fatalf("entries[0].Name = %q, want \"sibling.txt\"", entries[0].Name)
	}
	// Silence the dir-variable warning.
	_ = dir
}