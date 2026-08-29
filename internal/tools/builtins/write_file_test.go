package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// writeFileContent pulls the structured WriteFileResult out of a
// successful Result.Content. Tests call this to assert the wire
// shape without re-implementing the type assertion in every test.
func writeFileContent(t *testing.T, res tools.Result) WriteFileResult {
	t.Helper()
	wfr, ok := res.Content.(WriteFileResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want WriteFileResult (content=%v)",
			res.Content, res.Content)
	}
	return wfr
}

// TestWriteFile_HappyPath_CreateNew: write content to a
// non-existent file under t.TempDir(); assert Status="ok",
// BytesWritten == len(content), Created == true; assert the
// file's bytes on disk match the content.
func TestWriteFile_HappyPath_CreateNew(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "new.txt")

	wf := WriteFile{}
	content := "hello, world\nline two\n"
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dest,
		"content": content,
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	wfr := writeFileContent(t, res)
	if wfr.Path != dest {
		t.Fatalf("Path = %q, want %q", wfr.Path, dest)
	}
	if wfr.BytesWritten != len(content) {
		t.Fatalf("BytesWritten = %d, want %d", wfr.BytesWritten, len(content))
	}
	if !wfr.Created {
		t.Fatalf("Created = false, want true (file did not exist pre-call)")
	}

	// On-disk bytes match the content.
	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != content {
		t.Fatalf("on-disk bytes = %q, want %q", string(onDisk), content)
	}
}

// TestWriteFile_OverwriteExisting: pre-write a file with content
// X, then write content Y; assert Status="ok", BytesWritten ==
// len(Y), Created == false; assert the file's bytes on disk match
// Y (X is gone).
func TestWriteFile_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "existing.txt")
	original := "original content\n"
	if err := os.WriteFile(dest, []byte(original), writeFileMode); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	wf := WriteFile{}
	replacement := "replacement\n"
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dest,
		"content": replacement,
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	wfr := writeFileContent(t, res)
	if wfr.BytesWritten != len(replacement) {
		t.Fatalf("BytesWritten = %d, want %d", wfr.BytesWritten, len(replacement))
	}
	if wfr.Created {
		t.Fatalf("Created = true, want false (file existed pre-call)")
	}

	// On-disk bytes match the replacement; the original is gone.
	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != replacement {
		t.Fatalf("on-disk bytes = %q, want %q (the original should be gone)",
			string(onDisk), replacement)
	}
}

// TestWriteFile_EmptyContent: write empty content to a new file;
// assert Status="ok", BytesWritten == 0, Created == true; assert
// the file exists and is empty.
func TestWriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "empty.txt")

	wf := WriteFile{}
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dest,
		"content": "",
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	wfr := writeFileContent(t, res)
	if wfr.BytesWritten != 0 {
		t.Fatalf("BytesWritten = %d, want 0", wfr.BytesWritten)
	}
	if !wfr.Created {
		t.Fatalf("Created = false, want true (file did not exist pre-call)")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat %s: %v", dest, err)
	}
	if info.Size() != 0 {
		t.Fatalf("on-disk size = %d, want 0 (empty file)", info.Size())
	}
}

// TestWriteFile_MissingPath: omit the path argument; the schema
// validator normally catches this first, but the defensive guard
// inside Execute returns a structured schema_violation error when
// called directly.
func TestWriteFile_MissingPath(t *testing.T) {
	wf := WriteFile{}
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"content": "hello",
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want \"schema_violation\"", res.Error)
	}
}

// TestWriteFile_MissingContent: omit the content argument; same
// defensive schema_violation contract as the missing-path test.
func TestWriteFile_MissingContent(t *testing.T) {
	dir := t.TempDir()
	wf := WriteFile{}
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path": filepath.Join(dir, "anything.txt"),
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want \"schema_violation\"", res.Error)
	}
}

// TestWriteFile_DirectoryAsPath: pass a directory path (the test's
// t.TempDir) as the destination; assert Status="error",
// Error.Kind="is_a_directory".
func TestWriteFile_DirectoryAsPath(t *testing.T) {
	dir := t.TempDir()

	wf := WriteFile{}
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dir,
		"content": "should fail",
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "is_a_directory" {
		t.Fatalf("Error.Kind = %v, want \"is_a_directory\"", res.Error)
	}
}

// TestWriteFile_ParentNotFound: pass a path whose parent does not
// exist (e.g. t.TempDir() + "/no-such-dir/file.txt"); assert
// Status="error", Error.Kind="parent_not_found".
func TestWriteFile_ParentNotFound(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "no-such-dir", "file.txt")

	wf := WriteFile{}
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    bogus,
		"content": "should fail",
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "parent_not_found" {
		t.Fatalf("Error.Kind = %v, want \"parent_not_found\"", res.Error)
	}
}

// TestWriteFile_OverwriteIsAtomic: pre-write a file with content
// X, then write content Y; assert that after the call returns,
// the file's bytes are exactly Y (no partial-write window — the
// temp-file + rename pattern guarantees the destination is either
// the old X or the new Y at every observable moment, never a
// half-written file). The on-disk assertion is the deterministic
// verification; concurrency-stress is documented but not
// exhaustively tested in V1.
func TestWriteFile_OverwriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "atomic.txt")
	original := strings.Repeat("X", 4096)
	if err := os.WriteFile(dest, []byte(original), writeFileMode); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	wf := WriteFile{}
	replacement := strings.Repeat("Y", 4096)
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dest,
		"content": replacement,
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	// After the call returns, the on-disk content is exactly Y.
	// The atomicity contract guarantees no partial write window
	// was observable; the test asserts the post-call invariant.
	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != replacement {
		t.Fatalf("on-disk bytes differ from the intended content (atomicity violated: "+
			"want %d Ys, got mixed content)", len(replacement))
	}

	// No leftover .write_file-*.tmp file in the parent directory
	// (the deferred cleanup ran when the rename succeeded).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.Name() != "atomic.txt" {
			t.Fatalf("unexpected leftover entry in %s: %q (temp file cleanup failed?)",
				dir, e.Name())
		}
	}
}

// TestWriteFile_FileMode: set a RESTRICTIVE umask via syscall.Umask
// (0o077 — strips all group/other bits at file-creation time),
// call WriteFile{}.Execute to write a new file, read the file's
// info.Mode().Perm() and assert it equals the LITERAL writeFileMode
// (0o644) — NOT writeFileMode &^ umask.
//
// Why a restrictive umask: under umask 0o077, IF os.Chmod honored
// the process umask, the on-disk mode would be 0o644 &^ 0o077 ==
// 0o600. Because os.Chmod ignores the umask (chmod(2) sets the
// literal mode bits unconditionally), the on-disk mode is always
// 0o644 regardless of umask. This test therefore PASSES only if
// os.Chmod truly ignores umask (the current correct behavior); if
// a future change implements umask-honoring (e.g. via syscall.Umask
// get/set-and-restore masking), the file would land at 0o600 and
// this test would FAIL — correctly catching the contract change.
//
// The previous version of this test used umask 0o022, which cannot
// distinguish "chmod honors umask" from "chmod sets the literal
// mode" (0o644 &^ 0o022 == 0o644 either way). The chosen umask
// 0o077 is the canonical restrictive umask that exposes the
// difference.
//
// Placed last in the file so the umask change (which is process-
// wide) happens after all other TestWriteFile_* tests have already
// run. The umask is restored via defer before the test returns.
func TestWriteFile_FileMode(t *testing.T) {
	origUmask := syscall.Umask(0o077)
	t.Cleanup(func() {
		syscall.Umask(origUmask)
	})

	dir := t.TempDir()
	dest := filepath.Join(dir, "restrictive.txt")

	wf := WriteFile{}
	content := "mode test\n"
	call := tools.Call{Name: "write_file", Arguments: map[string]any{
		"path":    dest,
		"content": content,
	}}
	res, err := wf.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat %s: %v", dest, err)
	}
	// Assert the LITERAL writeFileMode. If os.Chmod honored the
	// umask (0o077), the on-disk mode would be 0o600; we assert
	// 0o644 to pin the documented contract that os.Chmod ignores
	// the umask.
	wantPerm := os.FileMode(writeFileMode)
	if info.Mode().Perm() != wantPerm {
		t.Fatalf("info.Mode().Perm() = %o, want %o (writeFileMode=%o, umask=0o077; "+
			"if os.Chmod honored umask, on-disk mode would be 0o600 — this test "+
			"asserts the literal mode to pin the contract that os.Chmod ignores umask)",
			info.Mode().Perm(), wantPerm, writeFileMode)
	}
}
