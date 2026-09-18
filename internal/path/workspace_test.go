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

// TestNormalize_DotDotPrefixFile: a file whose name LITERALLY starts
// with the characters ".." (e.g. "..oddfile") is inside the workspace
// by construction — the leading ".." is part of the filename, not a
// parent segment. The pre-Run-004 prefix check
// strings.HasPrefix(rel, "..") wrongly rejected such a path. The
// segment-boundary-safe form (rel == ".." ||
// strings.HasPrefix(rel, ".."+sep)) accepts it and returns the
// cleaned absolute path under the workspace root.
func TestNormalize_DotDotPrefixFile(t *testing.T) {
	ws := tempWorkspace(t)

	got, err := ws.Normalize("..oddfile")
	if err != nil {
		t.Fatalf("Normalize(..oddfile): %v (the leading '..' is part of the filename, not a parent segment)", err)
	}
	want := filepath.Join(ws.Root(), "..oddfile")
	if got != want {
		t.Fatalf("Normalize(..oddfile) = %q, want %q", got, want)
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

// TestNormalize_AbsolutePathThroughSymlinkEscapes — an absolute path
// spelled inside the workspace whose components include a symlink
// pointing outside must be rejected exactly like its relative twin.
// The absolute branch used to return the cleaned string without
// evaluating symlinks, so `<ws>/link/secret` read the outside file
// while `link/secret` was rejected.
func TestNormalize_AbsolutePathThroughSymlinkEscapes(t *testing.T) {
	ws := tempWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(ws.Root(), "link", "secret.txt"),
		"link/secret.txt",
	} {
		_, err := ws.Normalize(p)
		var ee *EscapeError
		if !errors.As(err, &ee) || ee.Reason != ReasonSymlinkEscape {
			t.Errorf("Normalize(%q) = err %v, want EscapeError{symlink_escape}", p, err)
		}
	}
}

// TestNormalize_NewFileUnderSymlinkedParentEscapes — a file that does
// not exist yet, under a directory symlink that points outside the
// workspace, must be rejected: write_file and apply_patch create
// files through exactly this path, and a NotExist on the leaf used to
// skip symlink evaluation altogether.
func TestNormalize_NewFileUnderSymlinkedParentEscapes(t *testing.T) {
	ws := tempWorkspace(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"link/new.txt",
		"link/deeper/new.txt",
		filepath.Join(ws.Root(), "link", "new.txt"),
	} {
		_, err := ws.Normalize(p)
		var ee *EscapeError
		if !errors.As(err, &ee) || ee.Reason != ReasonSymlinkEscape {
			t.Errorf("Normalize(%q) = err %v, want EscapeError{symlink_escape}", p, err)
		}
	}
	// A new file under a real subdirectory is still fine, and comes
	// back as the absolute path it will be created at.
	if err := os.Mkdir(filepath.Join(ws.Root(), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Normalize("sub/new.txt")
	if err != nil || got != filepath.Join(ws.Root(), "sub", "new.txt") {
		t.Errorf("Normalize(sub/new.txt) = %q, %v", got, err)
	}
}

// TestNormalize_AbsolutePathSpelledThroughSymlinkedRoot — when the
// workspace root itself was given through a symlink, an absolute path
// spelled with that unresolved root names a file inside the workspace
// and must be accepted (it used to be rejected as absolute_path
// because the string did not start with the resolved root).
func TestNormalize_AbsolutePathSpelledThroughSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "ws")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := New(link)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ws.Normalize(filepath.Join(link, "f.txt"))
	if err != nil {
		t.Fatalf("Normalize(<link-root>/f.txt): %v", err)
	}
	if want := filepath.Join(ws.Root(), "f.txt"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	// And a not-yet-existing file spelled the same way.
	got, err = ws.Normalize(filepath.Join(link, "new.txt"))
	if err != nil || got != filepath.Join(ws.Root(), "new.txt") {
		t.Errorf("Normalize(<link-root>/new.txt) = %q, %v", got, err)
	}
}
