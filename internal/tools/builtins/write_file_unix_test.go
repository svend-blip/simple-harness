//go:build !windows

package builtins

// The file-mode test sets a umask, which Windows does not have; it kept the
// package's tests from compiling there (CI, 2026-09-21).

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

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
