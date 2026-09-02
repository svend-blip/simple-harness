// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Handoff 014 registers the first two of the four
// V1 read-only tools: read_file and list_directory. Handoff 015 adds
// search_files and grep via the same RegisterBuiltins registrar.
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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// readFileMaxBytes is the size limit. SCOPE §9 requires a "reasonable
// file-size limit"; 1 MiB matches the convention common in coding-
// agent harnesses and is large enough for typical source files.
const readFileMaxBytes = 1 << 20 // 1 MiB

// readFileBinaryProbeBytes is the number of leading bytes scanned
// for a NUL byte (the canonical binary-text marker). 8 KiB is large
// enough to catch any plausible header (ELF, PE, PNG, JPEG magic
// bytes are all in the first 8 bytes; text files are NUL-free).
const readFileBinaryProbeBytes = 8 << 10 // 8 KiB

// ReadFile is the read_file builtin tool. It reads a workspace-
// relative file path, optionally sliced by line range, with a 1 MiB
// size limit and binary detection on the first 8 KiB. See SCOPE §9
// for the contract; see the handoff 014 result file for the seam
// choices (struct vs map encoding, empty-file end_line edge case).
//
// The tool assumes the dispatch pipeline has already validated the
// call (schema → path → policy). It does NOT re-normalize paths
// itself; it relies on the pipeline. When called directly from a
// test that bypasses Dispatch, the tool's path argument is treated
// as a relative-to-cwd path (os.Open semantics) — the test is
// responsible for using a path the file system can resolve.
type ReadFile struct{}

// Meta implements tools.Tool.
func (ReadFile) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "read_file",
		Description: "Read a workspace file with optional line range. Supports size limit and binary-file rejection per SCOPE §9.",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields.
func (ReadFile) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"path"},
		Properties: map[string]tools.PropertyType{
			"path":       tools.TypeString,
			"start_line": tools.TypeInt,
			"end_line":   tools.TypeInt,
		},
	}
}

// ReadFileResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format and
// downstream consumers parse the fields by name.
//
// Empty-file choice (seam choice recorded in the handoff 014 result
// file): for an empty file, TotalLines == 0, StartLine == 1,
// EndLine == 0. The end_line of 0 reflects "the last line in the
// file, indexed from 1, is line 0" — a clean shape that doesn't
// require callers to special-case empty files. Test
// TestReadFile_EmptyFile pins this contract.
type ReadFileResult struct {
	Content    string `json:"content"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	SizeBytes  int64  `json:"size_bytes"`
	Path       string `json:"path"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract path (required string), start_line (optional int,
//     default 1), end_line (optional int, default = file's last
//     line, inclusive 1-indexed).
//  2. Stat the file. os.IsNotExist → Kind: "not_found". Directory
//     → Kind: "not_a_file".
//  3. Size guard: if Size() > readFileMaxBytes (1 MiB) → Kind:
//     "file_too_large" with a message naming the size and limit.
//  4. Open the file. Read the leading readFileBinaryProbeBytes
//     (8 KiB). If any byte is 0x00 → Kind: "binary_file".
//  5. Otherwise read up to readFileMaxBytes total, split by '\n'
//     into lines, slice by [start_line..end_line] (1-indexed
//     inclusive, clamped to the file's line count).
//  6. Return Result{Status: "ok", Content: ReadFileResult{...}}.
//
// On any of the named failure modes the tool returns Result{Status:
// "error", Error: &tools.ToolError{...}} directly with a structured
// Kind. The pipeline wraps these in execution_failed only if the
// tool returns a non-nil error; returning a structured Result with
// Status="error" reaches the caller verbatim.
func (ReadFile) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for future cancellation hooks; today's
	// os.File reads do not honor it. The parameter is kept in the
	// signature so a future handoff can swap in a ctx-aware read
	// path without touching every call site.
	_ = ctx

	// Extract path (required; the schema validator already
	// enforced presence, so a missing path here would be a
	// pipeline bypass, which we still handle defensively).
	pathVal, ok := call.Arguments["path"].(string)
	if !ok || pathVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: "read_file: missing or non-string path argument",
			Call:    call,
		}}, nil
	}

	// Optional start_line / end_line. Both default to the file's
	// line range when omitted; both are clamped on the way out.
	startLine := 1
	if v, present := call.Arguments["start_line"]; present {
		if iv, ok := intArg(v); ok {
			startLine = iv
		} else {
			// Schema validator catches this for the
			// pipeline path; the defensive guard keeps
			// direct-Execute callers honest.
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "schema_violation",
				Message: "read_file: start_line is not an int",
				Call:    call,
			}}, nil
		}
	}
	endLine := 0 // sentinel: "set later from the file's last line"
	if v, present := call.Arguments["end_line"]; present {
		if iv, ok := intArg(v); ok {
			endLine = iv
		} else {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "schema_violation",
				Message: "read_file: end_line is not an int",
				Call:    call,
			}}, nil
		}
	}

	// Stat the file. The path is opened relative to the
	// process's cwd (the foundation's path stage has already
	// normalized workspace-relative arguments to safe absolute
	// paths; the tool operates on the safe form). Direct-Execute
	// callers (tests) pass a relative path the test's t.TempDir
	// can resolve.
	info, err := os.Stat(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("read_file: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("read_file: stat %s: %v", pathVal, err),
			Call:    call,
		}}, nil
	}
	if info.IsDir() {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_a_file",
			Message: fmt.Sprintf("read_file: %s is a directory, not a file", pathVal),
			Call:    call,
		}}, nil
	}
	size := info.Size()
	if size > readFileMaxBytes {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind: "file_too_large",
			Message: fmt.Sprintf("read_file: %s is %d bytes, exceeds %d-byte limit",
				pathVal, size, readFileMaxBytes),
			Call: call,
		}}, nil
	}

	// Open and read. We open once and read up to readFileMaxBytes
	// (the size check above guarantees the file is at most that
	// big, so a single Read is sufficient and bounded).
	f, err := os.Open(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("read_file: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("read_file: open %s: %v", pathVal, err),
			Call:    call,
		}}, nil
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, readFileMaxBytes))
	if err != nil {
		return tools.Result{}, fmt.Errorf("read_file: read %s: %w", pathVal, err)
	}

	// Binary probe: scan up to the first readFileBinaryProbeBytes
	// (8 KiB) for a NUL byte. We cap at the smaller of the probe
	// window and the actual file size (an empty file's probe is
	// zero bytes; a 100-byte file's probe is 100 bytes).
	probeEnd := len(data)
	if probeEnd > readFileBinaryProbeBytes {
		probeEnd = readFileBinaryProbeBytes
	}
	for _, b := range data[:probeEnd] {
		if b == 0x00 {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "binary_file",
				Message: fmt.Sprintf("read_file: %s appears to be a binary file", pathVal),
				Call:    call,
			}}, nil
		}
	}

	// Split into lines. We use bufio.Scanner with a 1 MiB buffer
	// (matching readFileMaxBytes) so a single very-long-line file
	// fits in one token; the size guard above already rejected
	// files larger than 1 MiB.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), readFileMaxBytes)
	lines := make([]string, 0, 16)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return tools.Result{}, fmt.Errorf("read_file: scan %s: %w", pathVal, err)
	}

	totalLines := len(lines)

	// Empty-file edge case (seam choice recorded in the result
	// file): TotalLines == 0, StartLine == 1, EndLine == 0. The
	// Content is "".
	if totalLines == 0 {
		return tools.Result{Status: "ok", Content: ReadFileResult{
			Content:    "",
			StartLine:  1,
			EndLine:    0,
			TotalLines: 0,
			SizeBytes:  size,
			Path:       pathVal,
		}}, nil
	}

	// Default endLine to the file's last line when omitted.
	if endLine == 0 {
		endLine = totalLines
	}
	// Clamp the slice to the file's line range. Negative or
	// zero startLine collapses to line 1; endLine larger than
	// totalLines collapses to totalLines.
	if startLine < 1 {
		startLine = 1
	}
	if startLine > totalLines {
		startLine = totalLines
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine > totalLines {
		endLine = totalLines
	}

	// 1-indexed inclusive slice. lines[startLine-1..endLine].
	slice := lines[startLine-1 : endLine]
	content := strings.Join(slice, "\n")

	return tools.Result{Status: "ok", Content: ReadFileResult{
		Content:    content,
		StartLine:  startLine,
		EndLine:    endLine,
		TotalLines: totalLines,
		SizeBytes:  size,
		Path:       pathVal,
	}}, nil
}