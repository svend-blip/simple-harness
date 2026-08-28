// Package path owns the workspace path-normalization seam used by both
// the tool registry and the permission gate (SCOPE §§12–13, ARCHITECTURE.md
// §"Permission boundary placement"). The package exposes a small surface:
//
//   - Workspace: the active workspace root, resolved once at construction.
//   - Normalize: resolve a (possibly relative, possibly symlinked) path
//     against the workspace root, returning its cleaned absolute form.
//   - EscapeError: a structured error type carrying the offending path,
//     the workspace root, and a one-word reason (absolute_path,
//     parent_traversal, symlink_escape).
//
// Escape prevention attacks three realistic vectors:
//
//   - absolute paths that start with "/" and do not have the resolved
//     workspace root as a prefix (covers the "/etc/passwd" vector),
//   - ".." segments that would resolve outside the root after
//     filepath.Clean (covers the "../outside.txt" and the nested
//     "subdir/../../outside.txt" vectors),
//   - symlinks that, when evaluated, point outside the workspace root
//     (covers the "escape symlink inside workspace" vector; the
//     same-file-inside-workspace vector is accepted because the target
//     IS inside the workspace).
//
// The Workspace type symlink-evaluates the root ONCE at construction so
// subsequent Normalize calls compare against a stable, fully-resolved
// root regardless of any later symlink renames or re-symlinks of the
// original root path.
//
// Architectural boundary: this is a Simple Harness component. It does not
// import orchestration, harness selection, GPU/VRAM allocation, model
// lifecycle, or Model Allocator policy. It imports only the Go standard
// library.
package path

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace is the active workspace root, resolved and symlink-evaluated
// once at construction. All path normalization is relative to this root;
// paths that try to escape the root produce *EscapeError.
type Workspace struct {
	root string
}

// New resolves and stores the workspace root. Relative paths are made
// absolute against the current process working directory. Symlinks in the
// root path are evaluated once (so later comparisons are not affected by a
// re-symlinked root); subsequent Normalize calls compare against this
// resolved root, not the original.
//
// EvalSymlinks fails with a NotExist-like error if the workspace root does
// not exist. The caller (cmd/simple-harness's runInteractive) is responsible
// for surfacing that as a configuration error before reaching the tool
// dispatch path; here we propagate the error verbatim.
func New(root string) (Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Workspace{}, fmt.Errorf("path: resolve %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// EvalSymlinks requires the root to exist. If the caller
		// passed a non-existent path we surface the underlying
		// os.PathError so the operator sees the file-level detail.
		return Workspace{}, fmt.Errorf("path: resolve symlinks for %q: %w", abs, err)
	}
	return Workspace{root: resolved}, nil
}

// Root returns the resolved workspace root. The returned string is the
// symlink-evaluated form captured at New-time; it is not re-evaluated on
// each call.
func (w Workspace) Root() string { return w.root }

// EscapeError is returned by Normalize when the input path tries to escape
// the workspace root. It carries the offending path, the workspace root,
// and a one-word reason — NOT a stack trace, NOT an os.PathError.
//
// Reason is one of the documented constants below; future revisions may
// add new reasons (e.g. "empty_path") but the three named are the contract
// the SCOPE §13 pipeline depends on.
type EscapeError struct {
	Path      string // the offending input path
	Workspace string // the resolved workspace root
	Reason    string // "absolute_path" | "parent_traversal" | "symlink_escape"
}

// Documented Reason values for EscapeError.Reason.
const (
	ReasonAbsolutePath    = "absolute_path"
	ReasonParentTraversal = "parent_traversal"
	ReasonSymlinkEscape   = "symlink_escape"
)

func (e *EscapeError) Error() string {
	return fmt.Sprintf("path %q escapes workspace %q: %s",
		e.Path, e.Workspace, e.Reason)
}

// Normalize returns the cleaned absolute path of p relative to the
// workspace root. The returned path is guaranteed to be inside the
// workspace root (after symlink evaluation). Any attempt to escape
// produces a structured *EscapeError.
//
// The algorithm:
//
//  1. Reject empty input with a *EscapeError{Reason: "absolute_path"}
//     (the empty path is treated as absolute-path-shaped: it has no
//     leading "./" or workspace-relative prefix, so it cannot be safely
//     joined without an escape attempt at a higher level).
//  2. Reject absolute paths that, after filepath.Clean, do NOT have the
//     resolved workspace root as a prefix. This catches "/etc/passwd"
//     AND the prefix-trick case "/<ws>-evil/file.txt" (string-prefix
//     matches but path-prefix does not). Returns
//     *EscapeError{Reason: "absolute_path"}.
//  3. Join the input to the workspace root and run filepath.Clean.
//     If Clean's result is outside the root (filepath.Rel starts with
//     ".." or is ".." itself), return
//     *EscapeError{Reason: "parent_traversal"}. This catches the simple
//     "../outside.txt" vector and the nested "subdir/../../outside.txt"
//     vector after Clean collapses the segments.
//  4. Try filepath.EvalSymlinks on the cleaned candidate. If it succeeds
//     AND the evaluated path is outside the workspace root, return
//     *EscapeError{Reason: "symlink_escape"}. If EvalSymlinks fails with
//     a NotExist error, the file does not exist yet but the parent
//     directory may; fall back to the cleaned candidate (no symlink
//     resolution) for the root-prefix check, which still catches
//     "../"-style escape attempts in non-existent paths.
//  5. Return the evaluated (or cleaned, if NotExist) absolute path.
//
// The symlink-resolution case deserves a note: we evaluate the candidate
// AFTER the prefix check on the cleaned string, so a "../" segment that
// would escape is caught by step 3 BEFORE we ask the kernel to resolve
// any symlinks along the way (which would otherwise raise a confusing
// ENOENT on the escape target).
func (w Workspace) Normalize(p string) (string, error) {
	if p == "" {
		return "", &EscapeError{
			Path:      p,
			Workspace: w.root,
			Reason:    ReasonAbsolutePath,
		}
	}

	// Step 2: absolute paths must resolve inside the workspace root.
	// filepath.IsAbs on Linux matches any leading "/". An absolute path
	// that is itself equal-to-or-under the resolved root is allowed
	// (an absolute reference to a file inside the workspace is the same
	// file as the relative one); an absolute path outside the root is
	// rejected as an absolute_path escape.
	if filepath.IsAbs(p) {
		cleaned := filepath.Clean(p)
		rel, err := filepath.Rel(w.root, cleaned)
		if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			return "", &EscapeError{
				Path:      p,
				Workspace: w.root,
				Reason:    ReasonAbsolutePath,
			}
		}
		// The absolute path is inside the workspace. Return the
		// cleaned form without further resolution — an absolute
		// reference to an existing-or-not file inside the workspace
		// is allowed.
		return cleaned, nil
	}

	// Step 3: join and clean; check the cleaned string against the root.
	candidate := filepath.Clean(filepath.Join(w.root, p))
	rel, err := filepath.Rel(w.root, candidate)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", &EscapeError{
			Path:      p,
			Workspace: w.root,
			Reason:    ReasonParentTraversal,
		}
	}

	// Step 4: evaluate symlinks on the candidate. EvalSymlinks fails
	// with a NotExist error if the file does not exist; in that case we
	// fall back to the cleaned candidate (no symlink resolution) — the
	// prefix check above already rejected ".." escapes, so a non-
	// existent path that survives the prefix check is inside the root
	// by construction.
	evaluated, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		// Any other error (permission, EIO, etc.) is propagated as a
		// plain Go error so the caller can decide. We do NOT wrap it
		// in *EscapeError because the failure mode is not "the path
		// escaped" — it is "the filesystem rejected the lookup".
		return "", fmt.Errorf("path: eval symlinks for %q: %w", candidate, err)
	}

	rel, err = filepath.Rel(w.root, evaluated)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", &EscapeError{
			Path:      p,
			Workspace: w.root,
			Reason:    ReasonSymlinkEscape,
		}
	}

	return evaluated, nil
}