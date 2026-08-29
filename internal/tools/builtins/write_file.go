// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Handoff 014 registered the first two of the four
// V1 read-only tools (read_file and list_directory); handoff 015
// registers the remaining two (search_files and grep); handoff 017
// adds the first mutation tool (write_file) via the same
// RegisterBuiltins registrar.
//
// The package is a thin layer over internal/tools: each builtin
// implements the tools.Tool interface (Meta, Schema, Execute) and is
// registered through a single RegisterBuiltins(*tools.Registry) call
// that cmd/simple-harness/main.go invokes at startup. The package
// imports ONLY the Go standard library plus internal/tools and the
// perm package (for the integration tests in builtins_test.go); it
// does NOT introduce new dependencies, new architecture layers, or
// new pipeline stages.
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy. It imports only the Go
// standard library and the local internal/tools package (and the
// internal/perm package from tests, where the seam exists).
package builtins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// writeFileMode is the file mode for newly created AND atomically-
// overwritten files. After os.Rename, os.Chmod applies this mode
// UNCONDITIONALLY: os.Chmod calls chmod(2), which sets the literal
// mode bits and ignores the process umask (umask only filters mode
// bits at file-creation time via open(2)/creat(2)/mkdir(2); it has
// no effect on chmod(2)). The on-disk mode is therefore always
// writeFileMode (0o644) regardless of the process's umask.
//
// Conventional for source files: readable by owner + group,
// writable by owner.
//
// Security implication: an operator who sets a restrictive umask
// (e.g. 0o077, common in CI / security-hardened images) will still
// get world-and-group-readable 0o644 files out of write_file. This
// is the truthful contract — write_file's mode is independent of
// the process's umask, by design (deterministic and predictable).
// Operators who need 0o600 under a 0o077 umask should post-process
// the file with chmod(..., 0o600) after write_file returns.
const writeFileMode = 0o644

// WriteFile is the write_file builtin tool. It writes a UTF-8 text
// file at a workspace-relative path, atomically replacing any
// existing file at the destination. See SCOPE §10 for the contract
// ("Support direct file writing where appropriate"); see the
// handoff 017 result file for the seam choices (atomic temp-file +
// rename, is_a_directory vs parent_not_found distinction, Created
// vs bytes_written semantics).
//
// The tool assumes the dispatch pipeline has already validated the
// call (schema → path → policy). It does NOT re-normalize paths
// itself; it relies on the pipeline. When called directly from a
// test that bypasses Dispatch, the tool's path argument is treated
// as a filesystem path the OS can resolve (relative to cwd, or
// absolute — matching os.Open / os.Stat semantics).
type WriteFile struct{}

// Meta implements tools.Tool.
func (WriteFile) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name: "write_file",
		Description: "Write a UTF-8 text file at a workspace path. " +
			"Atomic via temp-file + rename. Mutation tool — gated by the " +
			"policy stage (READ_ONLY denies; WORKSPACE_WRITE allows " +
			"in-workspace; FULL_ACCESS allows escape).",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields. Both path and content are
// required — the tool's contract is "write this content to this
// path", and a missing argument is a schema violation, not an
// implicit default.
func (WriteFile) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"path", "content"},
		Properties: map[string]tools.PropertyType{
			"path":    tools.TypeString,
			"content": tools.TypeString,
		},
	}
}

// WriteFileResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format and
// downstream consumers parse the fields by name.
//
// Path is the destination (the input `path` argument, unchanged —
// the pipeline already normalized it).
//
// BytesWritten is the count of bytes written; for UTF-8 text this
// equals len(content) byte-for-byte (no encoding step). For empty
// content BytesWritten is 0.
//
// Created is true if the destination did not exist before this
// call (a fresh file was created); false if the destination
// existed and was overwritten atomically.
type WriteFileResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
	Created      bool   `json:"created"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract path (required string) and content (required string).
//     Missing or non-string of either returns a structured
//     schema_violation error (the pipeline's schema validator
//     normally catches this first; the defensive guard keeps
//     direct-Execute callers honest).
//  2. Stat the destination path. If it exists and is a directory,
//     return Kind="is_a_directory".
//  3. Determine the parent directory: the destination's dirname.
//     If the parent directory does not exist (or is not a
//     directory), return Kind="parent_not_found".
//  4. Create a temp file in the parent directory (os.CreateTemp
//     in the same directory so the rename stays on the same
//     filesystem and is atomic on POSIX). Write content, fsync,
//     close.
//  5. os.Rename the temp file over the destination. On POSIX
//     this is atomic.
//  6. Return Result{Status:"ok", Content: WriteFileResult{...}}
//     with BytesWritten = len(content) and Created = (destination
//     did not exist pre-call).
//
// On any of the named failure modes the tool returns
// Result{Status:"error", Error:&ToolError{...}} directly with a
// structured Kind.
func (WriteFile) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for future cancellation hooks; today's
	// os.CreateTemp + os.Write + os.Rename sequence does not honor
	// it. The parameter is kept in the signature so a future
	// handoff can swap in a ctx-aware write path without touching
	// every call site.
	_ = ctx

	// Extract path (required string). The schema validator
	// normally catches a missing arg, but the defensive guard
	// keeps direct-Execute callers honest.
	pathVal, ok := call.Arguments["path"].(string)
	if !ok || pathVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "write_file: missing or non-string path argument",
			Call:    call,
		}}, nil
	}
	// Extract content (required string). Same defensive guard.
	content, ok := call.Arguments["content"].(string)
	if !ok {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "write_file: missing or non-string content argument",
			Call:    call,
		}}, nil
	}

	// Track whether the destination existed pre-call so the
	// result's Created flag is accurate. Stat the destination
	// first; if it doesn't exist, the Created flag is true.
	created := true
	if info, err := os.Stat(pathVal); err == nil {
		// Destination exists. If it's a directory, the
		// write_file contract rejects (writing to a directory
		// is not a file write). If it's a file (or symlink,
		// etc.), we will atomically replace it; Created=false.
		if info.IsDir() {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "is_a_directory",
				Message: fmt.Sprintf("write_file: %s is a directory, not a file", pathVal),
				Call:    call,
			}}, nil
		}
		created = false
	} else if !errors.Is(err, os.ErrNotExist) {
		// Stat failed for a non-NotExist reason (permission,
		// EIO, etc.). Surface as an execute error.
		return tools.Result{}, fmt.Errorf("write_file: stat %s: %w", pathVal, err)
	}

	// The parent directory must exist (otherwise the temp-file
	// creation below would fail in a confusing way). Pre-check
	// the parent and surface a structured parent_not_found error
	// if it is missing.
	parentDir := filepath.Dir(pathVal)
	if parentInfo, err := os.Stat(parentDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "parent_not_found",
				Message: fmt.Sprintf("write_file: parent directory %s does not exist", parentDir),
				Call:    call,
			}}, nil
		}
		return tools.Result{}, fmt.Errorf("write_file: stat parent %s: %w", parentDir, err)
	} else if !parentInfo.IsDir() {
		// The parent is not a directory (a file with that name,
		// for example). Treat as parent_not_found.
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "parent_not_found",
			Message: fmt.Sprintf("write_file: parent %s is not a directory", parentDir),
			Call:    call,
		}}, nil
	}

	// Atomic write: create temp file in parentDir, write content,
	// fsync, close, rename over destination. The temp file is in
	// the same directory as the destination so the rename stays on
	// the same filesystem and is atomic on POSIX.
	tmpFile, err := os.CreateTemp(parentDir, ".write_file-*.tmp")
	if err != nil {
		return tools.Result{}, fmt.Errorf("write_file: create temp in %s: %w", parentDir, err)
	}
	tmpName := tmpFile.Name()
	// Defer cleanup of the temp file if the rename fails or the
	// function returns early. If the rename succeeds, tmpName no
	// longer exists (it was renamed over the destination), and
	// os.Remove returns an error we can ignore.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write([]byte(content)); err != nil {
		tmpFile.Close()
		return tools.Result{}, fmt.Errorf("write_file: write temp %s: %w", tmpName, err)
	}
	// fsync to ensure the bytes are on disk before the rename.
	// Without this, a power loss between the rename and the
	// filesystem flushing the data could leave the destination
	// pointing at zero-byte content.
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return tools.Result{}, fmt.Errorf("write_file: fsync temp %s: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return tools.Result{}, fmt.Errorf("write_file: close temp %s: %w", tmpName, err)
	}

	// Atomic rename over the destination. On POSIX this is
	// atomic (the destination is either the old inode or the new
	// inode at every observable moment, never a half-written
	// file).
	if err := os.Rename(tmpName, pathVal); err != nil {
		return tools.Result{}, fmt.Errorf("write_file: rename %s -> %s: %w", tmpName, pathVal, err)
	}

	// Set the documented mode bits. os.Chmod calls chmod(2),
	// which sets the literal mode bits unconditionally — the
	// process umask is irrelevant to chmod(2). The on-disk mode
	// is always writeFileMode (0o644) regardless of the process's
	// umask. The constant is load-bearing here; the documented
	// contract is verified by TestWriteFile_FileMode.
	if err := os.Chmod(pathVal, writeFileMode); err != nil {
		return tools.Result{}, fmt.Errorf("write_file: chmod %s: %w", pathVal, err)
	}

	return tools.Result{Status: "ok", Content: WriteFileResult{
		Path:         pathVal,
		BytesWritten: len(content),
		Created:      created,
	}}, nil
}

// Compile-time interface check (no overhead; the registry uses
// dynamic dispatch, but the lint benefit is real).
var _ tools.Tool = WriteFile{}
