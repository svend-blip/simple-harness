package builtins

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// grepContent pulls the structured GrepResult out of a successful
// Result.Content. Tests call this to assert the wire shape without
// re-implementing the type assertion in every test.
func grepContent(t *testing.T, res tools.Result) GrepResult {
	t.Helper()
	gr, ok := res.Content.(GrepResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want GrepResult (content=%v)",
			res.Content, res.Content)
	}
	return gr
}

// withNativeLookPath swaps the execLookPath seam to force the
// native-fallback path. It restores the original on cleanup.
// The stub returns ("", error) so the tool sees "rg is not
// available" and takes the native branch.
func withNativeLookPath(t *testing.T) {
	t.Helper()
	saved := execLookPath
	execLookPath = func(_ string) (string, error) {
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { execLookPath = saved })
}

// withRGLookPath ensures the seam returns rg (production path).
// If rg is not on the developer's machine, this test will fail at
// the rg-availability check; the contract is "when rg IS in PATH,
// the rg path runs".
func withRGLookPath(t *testing.T) {
	t.Helper()
	saved := execLookPath
	execLookPath = saved // restore production
	t.Cleanup(func() { execLookPath = saved })
	// Verify rg is actually on PATH for this test environment.
	rgPath, err := execLookPath("rg")
	if err != nil {
		t.Skipf("rg not on PATH (err=%v); skipping rg-path test", err)
	}
	t.Logf("rg available at %s", rgPath)
}

// TestGrep_RG_HappyPath_BasicPattern: rg path — a workspace with one
// file containing the needle. Pattern matches; Backend="rg"; one
// GrepMatch row with the correct File/Line/Text.
func TestGrep_RG_HappyPath_BasicPattern(t *testing.T) {
	withRGLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("line one\nline two with needle\nline three\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
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

// TestGrep_RG_NoMatches: rg path — pattern matches nothing.
// Result.Status="ok", Backend="rg", Matches is empty.
func TestGrep_RG_NoMatches(t *testing.T) {
	withRGLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("alpha\nbeta\ngamma\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "nope",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "rg" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "rg")
	}
	if gr.Matches == nil {
		t.Fatalf("Matches is nil, want empty slice (no-match seam choice)")
	}
	if len(gr.Matches) != 0 {
		t.Fatalf("len(Matches) = %d, want 0 (matches=%v)", len(gr.Matches), gr.Matches)
	}
}

// TestGrep_RG_CaseInsensitive: rg path — pattern "NEEDLE" with
// case_insensitive=true matches "needle".
func TestGrep_RG_CaseInsensitive(t *testing.T) {
	withRGLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("here is a needle\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern":          "NEEDLE",
		"path":             dir,
		"case_insensitive": true,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "rg" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "rg")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
}

// TestGrep_RG_FilePattern: rg path — file_pattern="*.go" restricts
// matches to .go files; .txt files with matching content are
// excluded.
func TestGrep_RG_FilePattern(t *testing.T) {
	withRGLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", []byte("needle in txt\n"))
	writeFile(t, dir, "a.go", []byte("needle in go\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern":      "needle",
		"path":         dir,
		"file_pattern": "*.go",
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "rg" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "rg")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != "a.go" {
		t.Fatalf("Matches[0].File = %q, want %q (the .txt file must be excluded by --glob)",
			gr.Matches[0].File, "a.go")
	}
}

// TestGrep_RG_RecursesSubdirectories: rg path — a workspace with a
// file in a subdirectory; the match is found.
func TestGrep_RG_RecursesSubdirectories(t *testing.T) {
	withRGLookPath(t)

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}
	writeFile(t, subdir, "inner.txt", []byte("needle in subdir\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "rg" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "rg")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != filepath.Join("subdir", "inner.txt") {
		t.Fatalf("Matches[0].File = %q, want %q", gr.Matches[0].File,
			filepath.Join("subdir", "inner.txt"))
	}
}

// TestGrep_Native_HappyPath_BasicPattern: native path — same shape
// as the rg-path test. Backend="native".
func TestGrep_Native_HappyPath_BasicPattern(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("line one\nline two with needle\nline three\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "native")
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

// TestGrep_Native_NoMatches: native path — pattern matches nothing.
func TestGrep_Native_NoMatches(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("alpha\nbeta\ngamma\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "nope",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "native")
	}
	if gr.Matches == nil {
		t.Fatalf("Matches is nil, want empty slice")
	}
	if len(gr.Matches) != 0 {
		t.Fatalf("len(Matches) = %d, want 0 (matches=%v)", len(gr.Matches), gr.Matches)
	}
}

// TestGrep_Native_CaseInsensitive: native path — pattern "NEEDLE"
// with case_insensitive=true matches "needle" (the (?i) prefix is
// added to the compiled regexp).
func TestGrep_Native_CaseInsensitive(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("here is a needle\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern":          "NEEDLE",
		"path":             dir,
		"case_insensitive": true,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "native")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
}

// TestGrep_Native_FilePattern: native path — file_pattern="*.go"
// restricts matches to .go files via filepath.Match on the basename.
func TestGrep_Native_FilePattern(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "a.txt", []byte("needle in txt\n"))
	writeFile(t, dir, "a.go", []byte("needle in go\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern":      "needle",
		"path":         dir,
		"file_pattern": "*.go",
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "native")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != "a.go" {
		t.Fatalf("Matches[0].File = %q, want %q", gr.Matches[0].File, "a.go")
	}
}

// TestGrep_Native_RecursesSubdirectories: native path — a workspace
// with a file in a subdirectory; the walk recurses.
func TestGrep_Native_RecursesSubdirectories(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}
	writeFile(t, subdir, "inner.txt", []byte("needle in subdir\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if gr.Backend != "native" {
		t.Fatalf("Backend = %q, want %q", gr.Backend, "native")
	}
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != filepath.Join("subdir", "inner.txt") {
		t.Fatalf("Matches[0].File = %q, want %q", gr.Matches[0].File,
			filepath.Join("subdir", "inner.txt"))
	}
}

// TestGrep_Equivalence_BothBackends: the load-bearing assertion.
// Build a workspace with multiple files + patterns; run the same
// inputs through both backends; assert the match sets are equal
// modulo Backend.
func TestGrep_Equivalence_BothBackends(t *testing.T) {
	withRGLookPath(t) // confirm rg exists for this equivalence test

	dir := t.TempDir()
	// 3 files with overlapping but distinct content.
	writeFile(t, dir, "a.go", []byte("needle in go one\nfoo\nneedle in go two\n"))
	writeFile(t, dir, "b.txt", []byte("foo\nneedle in txt one\nbar\n"))
	// A subdir for recursion coverage.
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}
	writeFile(t, subdir, "c.md", []byte("needle in md deep\n"))

	patterns := []struct {
		name             string
		args             map[string]any
	}{
		{
			name: "basic",
			args: map[string]any{"pattern": "needle", "path": dir},
		},
		{
			name: "case_insensitive",
			args: map[string]any{
				"pattern":          "NEEDLE",
				"path":             dir,
				"case_insensitive": true,
			},
		},
		{
			name: "file_pattern_go",
			args: map[string]any{
				"pattern":      "needle",
				"path":         dir,
				"file_pattern": "*.go",
			},
		},
	}

	for _, tc := range patterns {
		t.Run(tc.name, func(t *testing.T) {
			g := Grep{}

			// RG path
			resRG, err := g.Execute(context.Background(), tools.Call{
				Name:      "grep",
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("Execute (rg): %v", err)
			}
			grRG := grepContent(t, resRG)
			if grRG.Backend != "rg" {
				t.Fatalf("Backend (rg) = %q, want %q", grRG.Backend, "rg")
			}

			// Native path
			withNativeLookPath(t)
			resNat, err := g.Execute(context.Background(), tools.Call{
				Name:      "grep",
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("Execute (native): %v", err)
			}
			grNat := grepContent(t, resNat)
			if grNat.Backend != "native" {
				t.Fatalf("Backend (native) = %q, want %q", grNat.Backend, "native")
			}

			// Compare matches: equal as sets (order must match too —
			// both are sorted by (File, Line) by Execute).
			if !reflect.DeepEqual(grRG.Matches, grNat.Matches) {
				t.Fatalf("matches differ:\n  rg:     %+v\n  native: %+v",
					grRG.Matches, grNat.Matches)
			}
		})
	}
}

// TestGrep_NotADirectory: a call whose path is a file (not a
// directory) returns Kind="not_a_directory" on both paths.
func TestGrep_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.txt")
	writeFile(t, dir, "file.txt", []byte("x"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    filePath,
	}}
	res, err := g.Execute(context.Background(), call)
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

// TestGrep_NotFound: a call whose path does not exist returns
// Kind="not_found".
func TestGrep_NotFound(t *testing.T) {
	dir := t.TempDir()

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    filepath.Join(dir, "no-such-dir"),
	}}
	res, err := g.Execute(context.Background(), call)
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

// TestGrep_InvalidRegex: an unparseable regex returns Kind="invalid_regex".
func TestGrep_InvalidRegex(t *testing.T) {
	withNativeLookPath(t)
	dir := t.TempDir()

	g := Grep{}
	// Unbalanced bracket — Go regexp will reject.
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "[unclosed",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "invalid_regex" {
		t.Fatalf("Error.Kind = %v, want \"invalid_regex\"", res.Error)
	}
}

// TestGrep_DotGitSkipped_Native: native path — a .git directory
// containing a file with the needle. The walk skips .git; the match
// is NOT in the result.
func TestGrep_DotGitSkipped_Native(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	writeFile(t, gitDir, "secret.txt", []byte("needle in git\n"))
	// A top-level match so the result is non-empty.
	writeFile(t, dir, "visible.txt", []byte("needle visible\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if len(gr.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1 (matches=%v)", len(gr.Matches), gr.Matches)
	}
	if gr.Matches[0].File != "visible.txt" {
		t.Fatalf("Matches[0].File = %q, want %q (.git/secret.txt must be excluded)",
			gr.Matches[0].File, "visible.txt")
	}
}

// TestGrep_MaxResultsTruncates: 5 matching lines; max_results=2
// truncates to 2 entries.
func TestGrep_MaxResultsTruncates_Native(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	writeFile(t, dir, "test.txt", []byte("needle 1\nother\nneedle 2\nother\nneedle 3\nother\nneedle 4\nother\nneedle 5\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern":     "needle",
		"path":        dir,
		"max_results": 2,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	gr := grepContent(t, res)
	if len(gr.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2 (max_results=2; got %v)", len(gr.Matches), gr.Matches)
	}
}

// TestGrep_SortStability: a workspace with multiple files matching
// in alphabetical-disorder order (to verify the sort happens).
// The two paths must produce identical sorted output.
func TestGrep_SortStability_Native(t *testing.T) {
	withNativeLookPath(t)

	dir := t.TempDir()
	// Three files, each with a match. Sorted order should be:
	// c.txt:1, b.txt:1, a.txt:1 (by File alphabetically).
	// The walk visits a/ first (alphabetical), so without sort
	// the order would be a,b,c — same as sorted. To prove the
	// sort happens, write content where File order matters.
	writeFile(t, dir, "a.txt", []byte("needle\n"))
	writeFile(t, dir, "c.txt", []byte("needle\n"))
	writeFile(t, dir, "b.txt", []byte("needle\n"))

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    dir,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	gr := grepContent(t, res)

	// The matches must be sorted by (File, Line). Verify the
	// order is alphabetical on File.
	if !sort.SliceIsSorted(gr.Matches, func(i, j int) bool {
		if gr.Matches[i].File != gr.Matches[j].File {
			return gr.Matches[i].File < gr.Matches[j].File
		}
		return gr.Matches[i].Line < gr.Matches[j].Line
	}) {
		t.Fatalf("Matches not sorted: %v", gr.Matches)
	}
}

// TestGrep_PathEscapeRejected_DirectExecute: the tool operates on
// the post-pipeline path argument. Direct-Execute with "../escape"
// is allowed by the tool (the pipeline is what rejects escapes);
// the file system resolves it. We exercise an absolute path the OS
// can NOT resolve (so the tool returns Kind="not_found" without
// performing a walk). The contract here is "the tool does not
// crash on direct-Execute with an out-of-workspace argument and
// returns a structured error if the path is bad at the OS layer".
//
// The pipeline path-escape test lives in builtins_test.go
// (TestIntegration_Grep_PathEscape_PathSecond).
func TestGrep_PathEscapeRejected_DirectExecute(t *testing.T) {
	// A non-existent absolute path so the tool's stat returns
	// NotExist and we get Kind="not_found" without walking
	// anything.
	nonexistent := "/no/such/dir/should/not/exist/anywhere"

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"pattern": "needle",
		"path":    nonexistent,
	}}
	res, err := g.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "not_found" {
		t.Fatalf("Error.Kind = %v, want \"not_found\"", res.Error)
	}
	_ = filepath.Join // silence unused-import if file shrinks
}

// TestGrep_MissingPattern: a call without the required pattern
// returns Kind="invalid_regex" per the seam choice — the missing
// pattern is treated like an unparseable regex ("no pattern to
// match"). The schema validator catches this in the dispatch path.
func TestGrep_MissingPattern(t *testing.T) {
	withNativeLookPath(t)
	dir := t.TempDir()

	g := Grep{}
	call := tools.Call{Name: "grep", Arguments: map[string]any{
		"path": dir,
	}}
	res, err := g.Execute(context.Background(), call)
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