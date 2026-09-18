package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// --- grep: the pattern is data, never an rg flag ---------------------

// TestGrep_PatternBeginningWithDashIsAPattern — `rg <pattern> <path>`
// with a pattern such as "--version" or "--pre=/bin/sh" let the model
// pass flags to rg; the second one makes rg run a program over every
// file, under READ_ONLY. The pattern must be passed as a pattern.
func TestGrep_PatternBeginningWithDashIsAPattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("plain line\nuse --version here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"rg", "native"} {
		t.Run(backend, func(t *testing.T) {
			if backend == "rg" {
				withRGLookPath(t)
			} else {
				withNativeLookPath(t)
			}
			res, err := Grep{}.Execute(context.Background(), tools.Call{Name: "grep", Arguments: map[string]any{
				"pattern": "--version", "path": dir}})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Status != "ok" {
				t.Fatalf("status %q: %+v", res.Status, res.Error)
			}
			gr := res.Content.(GrepResult)
			if len(gr.Matches) != 1 || gr.Matches[0].Line != 2 {
				t.Errorf("matches = %+v, want the one literal '--version' line", gr.Matches)
			}
		})
	}
}

// --- apply_patch ------------------------------------------------------

// TestApplyPatch_LaterHunkAfterAnEarlierDeletion — hunk 2's old line
// numbers refer to the ORIGINAL file, but the search started from that
// number in the already-modified slice. After hunk 1 deleted lines,
// hunk 2's target had moved above the search start: a correct patch was
// rejected, and with a duplicate sequence further down it was applied
// to the wrong lines.
func TestApplyPatch_LaterHunkAfterAnEarlierDeletion(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "f.txt")
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line" + itoa(i) + "\n")
	}
	if err := os.WriteFile(dest, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/f.txt\n+++ b/f.txt\n" +
		"@@ -2,6 +2,1 @@\n line2\n-line3\n-line4\n-line5\n-line6\n-line7\n" +
		"@@ -18,2 +13,2 @@\n line18\n-line19\n+LINE19\n"
	res, err := ApplyPatch{}.Execute(context.Background(), tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path": dest, "patch": patch}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status %q: %+v", res.Status, res.Error)
	}
	got, _ := os.ReadFile(dest)
	want := "line1\nline2\nline8\nline9\nline10\nline11\nline12\nline13\nline14\nline15\nline16\nline17\nline18\nLINE19\nline20\n"
	if string(got) != want {
		t.Errorf("on disk:\n%s\nwant:\n%s", got, want)
	}
}

// TestApplyPatch_NoNewlineAtEndOfFileMarker — every `git diff` of a
// file without a trailing newline carries the standard
// "\ No newline at end of file" line. It was rejected as
// unparseable_patch.
func TestApplyPatch_NoNewlineAtEndOfFileMarker(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(dest, []byte("a\nb"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+B\n\\ No newline at end of file\n"
	res, err := ApplyPatch{}.Execute(context.Background(), tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path": dest, "patch": patch}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status %q: %+v", res.Status, res.Error)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "a\nB" {
		t.Errorf("on disk = %q, want %q", got, "a\nB")
	}
}

// TestApplyPatch_CRLFFile — read_file hands the model lines without
// their '\r'; the patch it writes back therefore has none. apply_patch
// kept the '\r' in the file's lines and reported failed_hunk for a
// correct patch. Matching ignores the line ending, and the file keeps
// the endings it had.
func TestApplyPatch_CRLFFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(dest, []byte("a\r\nb\r\nc\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -2 +2 @@\n-b\n+B\n"
	res, err := ApplyPatch{}.Execute(context.Background(), tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path": dest, "patch": patch}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status %q: %+v", res.Status, res.Error)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "a\r\nB\r\nc\r\n" {
		t.Errorf("on disk = %q, want CRLF preserved with line 2 replaced", got)
	}
}

// --- read_file --------------------------------------------------------

// TestReadFile_StartLinePastEndIsAnError — start_line beyond the last
// line was clamped to the last line and that line returned as ok, so
// a model paging through a file received the final line again and
// again instead of learning it had reached the end.
func TestReadFile_StartLinePastEndIsAnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("1\n2\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ReadFile{}.Execute(context.Background(), tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": p, "start_line": 50}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" || res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("result = %+v, want a schema_violation naming the line range", res)
	}
	if !strings.Contains(res.Error.Message, "3") {
		t.Errorf("message %q should name the file's line count", res.Error.Message)
	}
}

// --- search_files -----------------------------------------------------

// TestSearchFiles_UnreadableSubdirectoryIsSkipped — one unreadable
// subdirectory aborted the whole search as not_found, hiding every
// match elsewhere; the grep tool skips such directories, and rg does.
func TestSearchFiles_UnreadableSubdirectoryIsSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "needle.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	res, err := SearchFiles{}.Execute(context.Background(), tools.Call{Name: "search_files", Arguments: map[string]any{
		"pattern": "needle", "path": dir}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status %q: %+v", res.Status, res.Error)
	}
	sr := res.Content.(SearchFilesResult)
	if len(sr.Files) != 1 || sr.Files[0] != "needle.txt" {
		t.Errorf("files = %v, want [needle.txt]", sr.Files)
	}
}

// --- list_directory ---------------------------------------------------

// TestListDirectory_SymlinkToDirectoryIsNotAFile — a symlink to a
// directory was reported as type "file" with the link's own size.
func TestListDirectory_SymlinkToDirectoryIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	res, err := ListDirectory{}.Execute(context.Background(), tools.Call{Name: "list_directory", Arguments: map[string]any{"path": dir}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	entries, _ := listDirectoryContent(t, res)
	var linkType string
	for _, e := range entries {
		if e.Name == "link" {
			linkType = e.Type
		}
	}
	if linkType != "symlink" {
		t.Errorf("link type = %q, want symlink", linkType)
	}
}

// --- shell ------------------------------------------------------------

// TestShell_DefaultCwdIsTheWorkspace — with no cwd argument the child
// ran in the harness process's working directory, which is the
// workspace only when the harness happens to be launched from it.
func TestShell_DefaultCwdIsTheWorkspace(t *testing.T) {
	ws := tempWorkspace(t)
	ctx := tools.WithWorkspace(context.Background(), ws)
	res, err := Shell{}.Execute(ctx, tools.Call{Name: "shell", Arguments: map[string]any{"command": "pwd"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr := res.Content.(ShellResult)
	if got := strings.TrimSpace(sr.Stdout); got != ws.Root() {
		t.Errorf("pwd = %q, want workspace %q", got, ws.Root())
	}
}

// TestShell_HugeTimeoutIsRejected — timeout_ms=1e18 overflowed the
// Duration multiplication; the result was unspecified (observed: no
// timeout at all).
func TestShell_HugeTimeoutIsRejected(t *testing.T) {
	res, err := Shell{}.Execute(context.Background(), tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "true", "timeout_ms": 1e18}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" || res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Errorf("result = %+v, want schema_violation", res)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + intToStr(i))
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}
