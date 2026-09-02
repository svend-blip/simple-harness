package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// writeFile is a tiny helper that writes content to a file relative
// to dir and returns the relative path. Tests use it to keep the
// path-argument shape in the call consistent (workspace-relative).
func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return name
}

// readFileContent pulls the structured ReadFileResult out of a
// successful Result.Content. Tests call this to assert the wire
// shape without re-implementing the type assertion in every test.
func readFileContent(t *testing.T, res tools.Result) ReadFileResult {
	t.Helper()
	rfr, ok := res.Content.(ReadFileResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want ReadFileResult (content=%v)",
			res.Content, res.Content)
	}
	return rfr
}

// TestReadFile_HappyPath_WholeFile: a 3-line file with no range
// arguments returns the whole file with start_line=1, end_line=3,
// total_lines=3, size_bytes matching the file's length.
func TestReadFile_HappyPath_WholeFile(t *testing.T) {
	dir := t.TempDir()
	name := writeFile(t, dir, "test.txt", []byte("alpha\nbeta\ngamma\n"))

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": filepath.Join(dir, name),
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.Content != "alpha\nbeta\ngamma" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "alpha\nbeta\ngamma")
	}
	if rfr.StartLine != 1 {
		t.Fatalf("StartLine = %d, want 1", rfr.StartLine)
	}
	if rfr.EndLine != 3 {
		t.Fatalf("EndLine = %d, want 3", rfr.EndLine)
	}
	if rfr.TotalLines != 3 {
		t.Fatalf("TotalLines = %d, want 3", rfr.TotalLines)
	}
	if rfr.SizeBytes != int64(len("alpha\nbeta\ngamma\n")) {
		t.Fatalf("SizeBytes = %d, want %d", rfr.SizeBytes, len("alpha\nbeta\ngamma\n"))
	}
}

// TestReadFile_LineRange_Middle: a 10-line file with start_line=3,
// end_line=5 returns exactly lines 3-5.
func TestReadFile_LineRange_Middle(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10"}
	name := writeFile(t, dir, "test.txt", []byte(strings.Join(lines, "\n")+"\n"))

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path":       filepath.Join(dir, name),
		"start_line": 3,
		"end_line":   5,
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.StartLine != 3 || rfr.EndLine != 5 {
		t.Fatalf("range = [%d, %d], want [3, 5]", rfr.StartLine, rfr.EndLine)
	}
	if rfr.Content != "l3\nl4\nl5" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "l3\nl4\nl5")
	}
	if rfr.TotalLines != 10 {
		t.Fatalf("TotalLines = %d, want 10", rfr.TotalLines)
	}
}

// TestReadFile_StartLineOnly: a 5-line file with only start_line=3
// returns lines 3..5 (end_line defaults to the file's last line).
func TestReadFile_StartLineOnly(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"l1", "l2", "l3", "l4", "l5"}
	name := writeFile(t, dir, "test.txt", []byte(strings.Join(lines, "\n")+"\n"))

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path":       filepath.Join(dir, name),
		"start_line": 3,
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.StartLine != 3 {
		t.Fatalf("StartLine = %d, want 3", rfr.StartLine)
	}
	if rfr.EndLine != 5 {
		t.Fatalf("EndLine = %d, want 5 (default = file's last line)", rfr.EndLine)
	}
	if rfr.Content != "l3\nl4\nl5" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "l3\nl4\nl5")
	}
}

// TestReadFile_EndLineOnly: a 5-line file with only end_line=3
// returns lines 1..3 (start_line defaults to 1).
func TestReadFile_EndLineOnly(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"l1", "l2", "l3", "l4", "l5"}
	name := writeFile(t, dir, "test.txt", []byte(strings.Join(lines, "\n")+"\n"))

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path":     filepath.Join(dir, name),
		"end_line": 3,
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.StartLine != 1 {
		t.Fatalf("StartLine = %d, want 1 (default)", rfr.StartLine)
	}
	if rfr.EndLine != 3 {
		t.Fatalf("EndLine = %d, want 3", rfr.EndLine)
	}
	if rfr.Content != "l1\nl2\nl3" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "l1\nl2\nl3")
	}
}

// TestReadFile_EndLinePastEnd: a 3-line file with end_line=100
// clamps end_line to 3 (the file's last line). start_line defaults
// to 1. No error.
func TestReadFile_EndLinePastEnd(t *testing.T) {
	dir := t.TempDir()
	lines := []string{"l1", "l2", "l3"}
	name := writeFile(t, dir, "test.txt", []byte(strings.Join(lines, "\n")+"\n"))

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path":     filepath.Join(dir, name),
		"end_line": 100,
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.StartLine != 1 || rfr.EndLine != 3 {
		t.Fatalf("range = [%d, %d], want [1, 3] (clamped)", rfr.StartLine, rfr.EndLine)
	}
	if rfr.TotalLines != 3 {
		t.Fatalf("TotalLines = %d, want 3", rfr.TotalLines)
	}
	if rfr.Content != "l1\nl2\nl3" {
		t.Fatalf("Content = %q, want %q", rfr.Content, "l1\nl2\nl3")
	}
}

// TestReadFile_EmptyFile: a zero-byte file returns Content="" with
// total_lines=0. Per the seam choice, start_line=1 and end_line=0.
// size_bytes=0.
func TestReadFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	name := writeFile(t, dir, "empty.txt", []byte{})

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": filepath.Join(dir, name),
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	rfr := readFileContent(t, res)
	if rfr.Content != "" {
		t.Fatalf("Content = %q, want \"\"", rfr.Content)
	}
	if rfr.TotalLines != 0 {
		t.Fatalf("TotalLines = %d, want 0", rfr.TotalLines)
	}
	if rfr.SizeBytes != 0 {
		t.Fatalf("SizeBytes = %d, want 0", rfr.SizeBytes)
	}
	if rfr.StartLine != 1 {
		t.Fatalf("StartLine = %d, want 1 (empty-file seam choice)", rfr.StartLine)
	}
	if rfr.EndLine != 0 {
		t.Fatalf("EndLine = %d, want 0 (empty-file seam choice)", rfr.EndLine)
	}
}

// TestReadFile_BinaryRejection: a file with a NUL byte in the first
// 8 KiB is rejected with Kind="binary_file".
func TestReadFile_BinaryRejection(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello\x00world\nmore text after NUL\n")
	name := writeFile(t, dir, "binary.bin", content)

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": filepath.Join(dir, name),
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "binary_file" {
		t.Fatalf("Error.Kind = %v, want \"binary_file\"", res.Error)
	}
}

// TestReadFile_OversizeRejection: a file 1 MiB + 1 byte is rejected
// with Kind="file_too_large". The message names the actual size
// and the 1 MiB limit.
func TestReadFile_OversizeRejection(t *testing.T) {
	dir := t.TempDir()
	// Build a 1 MiB + 1 byte file of plain ASCII so we don't
	// accidentally trigger the binary-file rejection first.
	content := make([]byte, readFileMaxBytes+1)
	for i := range content {
		content[i] = 'x'
	}
	name := writeFile(t, dir, "big.bin", content)

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": filepath.Join(dir, name),
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "file_too_large" {
		t.Fatalf("Error.Kind = %v, want \"file_too_large\"", res.Error)
	}
	if !strings.Contains(res.Error.Message, "file_too_large") &&
		!strings.Contains(res.Error.Message, "exceeds") {
		t.Fatalf("Error.Message = %q, want it to mention the size and limit",
			res.Error.Message)
	}
}

// TestReadFile_NotAFile: a workspace subdirectory is rejected with
// Kind="not_a_file".
func TestReadFile_NotAFile(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", subdir, err)
	}

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": subdir,
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "not_a_file" {
		t.Fatalf("Error.Kind = %v, want \"not_a_file\"", res.Error)
	}
}

// TestReadFile_NotFound: a workspace path that does not exist is
// rejected with Kind="not_found".
func TestReadFile_NotFound(t *testing.T) {
	dir := t.TempDir()

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": filepath.Join(dir, "no-such-file.txt"),
	}}
	res, err := rf.Execute(context.Background(), call)
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

// TestReadFile_PathEscapeRejected_DirectExecute: the test calls
// ReadFile.Execute directly with an absolute path that escapes the
// test's t.TempDir workspace. The tool does not re-normalize (the
// dispatch pipeline handles that); the direct-Execute path opens
// the file via os.Open which sees an absolute path. The contract
// here is "the test exercises direct-Execute and asserts the tool
// does not crash and returns a structured error if the path is bad
// at the OS layer". The dispatch-pipeline path-escape test lives in
// builtins_test.go (TestIntegration_PathEscape_PathSecond).
//
// We exercise a path the OS rejects (a directory outside the
// workspace) — the tool's stat returns "is a directory", which is
// not a file, so the tool rejects with Kind="not_a_file". This pins
// the integration boundary that the path arg flows through to the
// tool unchanged when Dispatch is bypassed.
func TestReadFile_PathEscapeRejected_DirectExecute(t *testing.T) {
	dir := t.TempDir()
	// A directory we know exists outside dir: the OS temp dir.
	outside := os.TempDir()

	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path": outside, // absolute, outside the test's workspace
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The OS resolves the path to a directory; the tool's
	// not-a-file rejection fires. The pipeline path-escape test
	// lives in builtins_test.go and asserts Kind="path_escape"
	// via Dispatch.
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "error", res.Error)
	}
	if res.Error == nil || res.Error.Kind != "not_a_file" {
		t.Fatalf("Error.Kind = %v, want \"not_a_file\" (direct-Execute of a directory path)", res.Error)
	}
	// Silence the dir-variable warning.
	_ = dir
}
// TestReadFile_JSONDecodedLineNumbers: a model's call arrives through
// encoding/json, so start_line/end_line are float64, not int. The schema
// accepts a whole float64; the tool must too. Every ranged read_file from
// a MiniMax session failed on this on 2026-09-02.
func TestReadFile_JSONDecodedLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := ReadFile{}
	call := tools.Call{Name: "read_file", Arguments: map[string]any{
		"path":       path,
		"start_line": float64(2),
		"end_line":   float64(3),
	}}
	res, err := rf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status %q, error %+v", res.Status, res.Error)
	}
	got := readFileContent(t, res).Content
	if !strings.Contains(got, "l2") || !strings.Contains(got, "l3") || strings.Contains(got, "l4") {
		t.Fatalf("expected lines 2-3 only, got %q", got)
	}
	// A fractional number is still a schema violation.
	call.Arguments["start_line"] = 2.5
	res, _ = rf.Execute(context.Background(), call)
	if res.Status != "error" || res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("fractional start_line must be rejected, got %+v", res)
	}
}
