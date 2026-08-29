package builtins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// applyPatchContent pulls the structured ApplyPatchResult out of a
// successful Result.Content. Tests call this to assert the wire
// shape without re-implementing the type assertion in every test.
func applyPatchContent(t *testing.T, res tools.Result) ApplyPatchResult {
	t.Helper()
	apr, ok := res.Content.(ApplyPatchResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want ApplyPatchResult (content=%v)",
			res.Content, res.Content)
	}
	return apr
}

// TestApplyPatch_HappyPath_SingleHunk: pre-write a file with 3
// lines "alpha\nbeta\ngamma\n", apply a patch `@@ -2 +2 @@\n-beta
// \n+BETA\n` (replace "beta" with "BETA" at line 2); assert
// Status="ok", HunksApplied=1, BytesChanged=8 (4 removed + 4
// added); assert the file's bytes on disk are "alpha\nBETA
// \ngamma\n".
func TestApplyPatch_HappyPath_SingleHunk(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(dest, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "--- a/alpha.txt\n+++ b/alpha.txt\n@@ -2 +2 @@\n-beta\n+BETA\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	apr := applyPatchContent(t, res)
	if apr.Path != dest {
		t.Fatalf("Path = %q, want %q", apr.Path, dest)
	}
	if apr.HunksApplied != 1 {
		t.Fatalf("HunksApplied = %d, want 1", apr.HunksApplied)
	}
	if apr.BytesChanged != 8 {
		t.Fatalf("BytesChanged = %d, want 8 (4 removed + 4 added)", apr.BytesChanged)
	}

	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("on-disk bytes = %q, want %q", string(onDisk), "alpha\nBETA\ngamma\n")
	}
}

// TestApplyPatch_HappyPath_MultipleHunks: pre-write a file with 5
// lines "a\nb\nc\nd\ne\n", apply a patch with TWO @@ blocks (one
// replacing "b" with "B" at line 2, one replacing "d" with "D" at
// line 4); assert Status="ok", HunksApplied=2, BytesChanged=4
// (2+2); assert the file's bytes are "a\nB\nc\nD\ne\n".
func TestApplyPatch_HappyPath_MultipleHunks(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "multi.txt")
	if err := os.WriteFile(dest, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "--- a/multi.txt\n+++ b/multi.txt\n@@ -2 +2 @@\n-b\n+B\n@@ -4 +4 @@\n-d\n+D\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	apr := applyPatchContent(t, res)
	if apr.HunksApplied != 2 {
		t.Fatalf("HunksApplied = %d, want 2", apr.HunksApplied)
	}
	if apr.BytesChanged != 4 {
		t.Fatalf("BytesChanged = %d, want 4 (2+2)", apr.BytesChanged)
	}

	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != "a\nB\nc\nD\ne\n" {
		t.Fatalf("on-disk bytes = %q, want %q", string(onDisk), "a\nB\nc\nD\ne\n")
	}
}

// TestApplyPatch_AmbiguousContext_Rejects: pre-write a file with
// the same line "foo" repeated 3 times (file content "foo\nfoo
// \nfoo\n"); apply a patch that removes "foo" without enough
// context to disambiguate; assert Status="error",
// Error.Kind="ambiguous"; assert the file is UNCHANGED on disk
// (atomicity-leaves-workspace-untouched-on-failure).
func TestApplyPatch_AmbiguousContext_Rejects(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ambiguous.txt")
	original := "foo\nfoo\nfoo\n"
	if err := os.WriteFile(dest, []byte(original), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "--- a/ambiguous.txt\n+++ b/ambiguous.txt\n@@ -1 +1 @@\n-foo\n+FOO\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "ambiguous" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "ambiguous")
	}

	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != original {
		t.Fatalf("on-disk bytes = %q, want unchanged %q", string(onDisk), original)
	}
}

// TestApplyPatch_FailedHunk_Rejects: pre-write a file with 3
// lines "alpha\nbeta\ngamma\n"; apply a patch that tries to
// replace "DELTA" (not in the file) with "DELTA-NEW"; assert
// Status="error", Error.Kind="failed_hunk"; assert the file is
// UNCHANGED on disk.
func TestApplyPatch_FailedHunk_Rejects(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "fail.txt")
	original := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(dest, []byte(original), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "--- a/fail.txt\n+++ b/fail.txt\n@@ -1 +1 @@\n-DELTA\n+DELTA-NEW\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "failed_hunk" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "failed_hunk")
	}

	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != original {
		t.Fatalf("on-disk bytes = %q, want unchanged %q", string(onDisk), original)
	}
}

// TestApplyPatch_Atomicity_LeavesWorkspaceUntouchedOnFailure:
// pre-write a file with content X; apply a 2-hunk patch where
// hunk 1 succeeds and hunk 2 fails (because hunk 2's context
// doesn't match); assert Status="error",
// Error.Kind="failed_hunk"; assert the file's bytes on disk are
// still X (no partial application).
func TestApplyPatch_Atomicity_LeavesWorkspaceUntouchedOnFailure(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "atomic.txt")
	original := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(dest, []byte(original), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "--- a/atomic.txt\n+++ b/atomic.txt\n@@ -2 +2 @@\n-b\n+B\n@@ -4 +4 @@\n-ZZ\n+ZZ\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "failed_hunk" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "failed_hunk")
	}

	onDisk, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", dest, err)
	}
	if string(onDisk) != original {
		t.Fatalf("on-disk bytes = %q, want unchanged %q (atomicity violated: partial application?)",
			string(onDisk), original)
	}
}

// TestApplyPatch_UnparseablePatch: pre-write a file; apply a
// patch with a missing `---` header; assert Status="error",
// Error.Kind="unparseable_patch".
func TestApplyPatch_UnparseablePatch(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "unparseable.txt")
	if err := os.WriteFile(dest, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed write %s: %v", dest, err)
	}

	ap := ApplyPatch{}
	patch := "@@ -1 +1 @@\n-x\n+X\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  dest,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "unparseable_patch" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "unparseable_patch")
	}
}

// TestApplyPatch_MissingPath: omit the `path` argument; assert
// Status="error", Error.Kind="schema_violation" (the schema
// validator rejects before Execute runs; the defensive guard
// inside Execute catches it for direct callers).
func TestApplyPatch_MissingPath(t *testing.T) {
	ap := ApplyPatch{}
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"patch": "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+A\n",
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "schema_violation")
	}
}

// TestApplyPatch_MissingPatch: omit the `patch` argument; assert
// Status="error", Error.Kind="schema_violation".
func TestApplyPatch_MissingPatch(t *testing.T) {
	ap := ApplyPatch{}
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path": "/tmp/anything",
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "schema_violation" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "schema_violation")
	}
}

// TestApplyPatch_TargetNotFound: pass a path that does not exist
// (e.g. t.TempDir()/no-such-file.txt); assert Status="error",
// Error.Kind="target_not_found".
func TestApplyPatch_TargetNotFound(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "no-such-file.txt")

	ap := ApplyPatch{}
	patch := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+A\n"
	call := tools.Call{Name: "apply_patch", Arguments: map[string]any{
		"path":  bogus,
		"patch": patch,
	}}
	res, err := ap.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil || res.Error.Kind != "target_not_found" {
		t.Fatalf("Error.Kind = %v, want %q", res.Error, "target_not_found")
	}
}
