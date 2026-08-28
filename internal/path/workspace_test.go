package path

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNormalize_HappyPath: a simple relative path resolves inside the
// workspace and is returned as an absolute path under the root.
func TestNormalize_HappyPath(t *testing.T) {
	ws := tempWorkspace(t)
	got, err := ws.Normalize("subdir/file.txt")
	if err != nil {
		t.Fatalf("Normalize(subdir/file.txt): %v", err)
	}
	want := filepath.Join(ws.Root(), "subdir", "file.txt")
	if got != want {
		t.Fatalf("Normalize(subdir/file.txt) = %q, want %q", got, want)
	}
}

// TestNormalize_RejectsAbsolutePath: "/etc/passwd" is rejected with
// Reason="absolute_path".
func TestNormalize_RejectsAbsolutePath(t *testing.T) {
	ws := tempWorkspace(t)
	_, err := ws.Normalize("/etc/passwd")
	if err == nil {
		t.Fatalf("Normalize(/etc/passwd) returned nil error, want *EscapeError")
	}
	var ee *EscapeError
	if !errors.As(err, &ee) {
		t.Fatalf("Normalize(/etc/passwd) error type = %T, want *EscapeError", err)
	}
	if ee.Reason != ReasonAbsolutePath {
		t.Fatalf("EscapeError.Reason = %q, want %q", ee.Reason, ReasonAbsolutePath)
	}
}

// TestNormalize_RejectsAbsolutePath_PrefixTrick: a path like
// "/<ws>-evil/file.txt" string-starts with the workspace but is not
// inside it. The escape is rejected; the Reason is one of the two
// acceptable values (absolute_path or parent_traversal). The handoff
// leaves the choice to the implementer.
func TestNormalize_RejectsAbsolutePath_PrefixTrick(t *testing.T) {
	ws := tempWorkspace(t)
	trickPath := ws.Root() + "-evil/file.txt"
	_, err := ws.Normalize(trickPath)
	if err == nil {
		t.Fatalf("Normalize(%q) returned nil error, want *EscapeError", trickPath)
	}
	var ee *EscapeError
	if !errors.As(err, &ee) {
		t.Fatalf("Normalize(%q) error type = %T, want *EscapeError", trickPath, err)
	}
	if ee.Reason != ReasonAbsolutePath && ee.Reason != ReasonParentTraversal {
		t.Fatalf("EscapeError.Reason = %q, want %q or %q",
			ee.Reason, ReasonAbsolutePath, ReasonParentTraversal)
	}
}

// TestNormalize_RejectsParentTraversal: "../outside.txt" is rejected
// with Reason="parent_traversal".
func TestNormalize_RejectsParentTraversal(t *testing.T) {
	ws := tempWorkspace(t)
	_, err := ws.Normalize("../outside.txt")
	if err == nil {
		t.Fatalf("Normalize(../outside.txt) returned nil error, want *EscapeError")
	}
	var ee *EscapeError
	if !errors.As(err, &ee) {
		t.Fatalf("Normalize(../outside.txt) error type = %T, want *EscapeError", err)
	}
	if ee.Reason != ReasonParentTraversal {
		t.Fatalf("EscapeError.Reason = %q, want %q", ee.Reason, ReasonParentTraversal)
	}
}

// TestNormalize_RejectsParentTraversal_Nested: "subdir/../../outside.txt"
// is rejected with Reason="parent_traversal" — the nested form must be
// caught after filepath.Clean collapses the "..".
func TestNormalize_RejectsParentTraversal_Nested(t *testing.T) {
	ws := tempWorkspace(t)
	_, err := ws.Normalize("subdir/../../outside.txt")
	if err == nil {
		t.Fatalf("Normalize(subdir/../../outside.txt) returned nil error, want *EscapeError")
	}
	var ee *EscapeError
	if !errors.As(err, &ee) {
		t.Fatalf("Normalize(subdir/../../outside.txt) error type = %T, want *EscapeError", err)
	}
	if ee.Reason != ReasonParentTraversal {
		t.Fatalf("EscapeError.Reason = %q, want %q", ee.Reason, ReasonParentTraversal)
	}
}

// TestNormalize_AllowsSameFileSymlink: a symlink INSIDE the workspace
// that points to another file INSIDE the workspace must succeed — the
// target is not an escape.
func TestNormalize_AllowsSameFileSymlink(t *testing.T) {
	ws := tempWorkspace(t)
	target := filepath.Join(ws.Root(), "real.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write real.txt: %v", err)
	}
	link := filepath.Join(ws.Root(), "link.txt")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := ws.Normalize("link.txt")
	if err != nil {
		t.Fatalf("Normalize(link.txt): %v", err)
	}
	// The evaluated path resolves to target. It must equal either the
	// cleaned candidate or the symlink target itself, and it must be
	// inside the workspace.
	if !strings.HasPrefix(got, ws.Root()+string(filepath.Separator)) && got != ws.Root() {
		t.Fatalf("Normalize(link.txt) = %q, want path inside %q", got, ws.Root())
	}
}

// TestNormalize_RejectsSymlinkEscape: a symlink INSIDE the workspace
// that points to a file OUTSIDE the workspace is rejected with
// Reason="symlink_escape". The escape target lives at /tmp/outside.txt
// (a real file we create so EvalSymlinks succeeds on the link target).
func TestNormalize_RejectsSymlinkEscape(t *testing.T) {
	ws := tempWorkspace(t)

	// Create the escape target OUTSIDE the workspace at /tmp/outside.txt.
	// We use a per-test unique filename so parallel test runs do not
	// collide on the same path.
	outside := filepath.Join(os.TempDir(), "sh-test-escape-"+t.Name()+".txt")
	if err := os.WriteFile(outside, []byte("escaped"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	// Create a symlink INSIDE the workspace that points to the outside target.
	link := filepath.Join(ws.Root(), "escape-link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ws.Normalize("escape-link.txt")
	if err == nil {
		t.Fatalf("Normalize(escape-link.txt) returned nil error, want *EscapeError")
	}
	var ee *EscapeError
	if !errors.As(err, &ee) {
		t.Fatalf("Normalize(escape-link.txt) error type = %T, want *EscapeError", err)
	}
	if ee.Reason != ReasonSymlinkEscape {
		t.Fatalf("EscapeError.Reason = %q, want %q", ee.Reason, ReasonSymlinkEscape)
	}
}

// tempWorkspace creates a temporary directory and returns a Workspace
// rooted at it. The directory is cleaned up automatically at the end of
// the test.
func tempWorkspace(t *testing.T) Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := New(dir)
	if err != nil {
		t.Fatalf("New(%q): %v", dir, err)
	}
	return ws
}