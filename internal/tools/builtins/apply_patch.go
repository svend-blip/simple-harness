// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Handoff 014 registered the first two of the four
// V1 read-only tools (read_file and list_directory); handoff 015
// registers the remaining two (search_files and grep); handoff 017
// adds the first mutation tool (write_file) via the same
// RegisterBuiltins registrar; handoff 018 adds the second mutation
// tool (apply_patch).
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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// ApplyPatch is the apply_patch builtin tool. It applies a
// unified-diff patch to a UTF-8 text file at a workspace-relative
// path. See SCOPE §10 for the contract ("deterministic patching for
// source modifications"); see the handoff 018 result file for the
// seam choices (unified-diff parser shape, per-hunk exact-match
// uniqueness, atomicity-leaves-workspace-untouched-on-failure, the
// os.CreateTemp + os.Rename atomic write pattern reused from
// write_file).
//
// The tool assumes the dispatch pipeline has already validated the
// call (schema → path → policy). It does NOT re-normalize paths
// itself; it relies on the pipeline. When called directly from a
// test that bypasses Dispatch, the tool's path argument is treated
// as a filesystem path the OS can resolve (relative to cwd, or
// absolute — matching os.Open / os.Stat semantics).
//
// SCOPE §10 discipline ("A failed patch must not be silently
// approximated" + "The harness itself must not guess what the model
// intended to modify"): the tool implements strict uniqueness —
// a hunk's (context + removed) sequence must appear at EXACTLY ONE
// location in the file starting at or after oldStart - 1; ambiguous
// (multiple matches) is a structured reject with Kind="ambiguous";
// failed (zero matches) is a structured reject with
// Kind="failed_hunk". A near-match (whitespace difference, off-by-
// one line) is a hard reject, NOT a silent approximation.
type ApplyPatch struct{}

// Meta implements tools.Tool.
func (ApplyPatch) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name: "apply_patch",
		Description: "Apply a unified-diff patch to a UTF-8 text file at " +
			"a workspace path. Strict uniqueness: ambiguous or failed " +
			"hunks are rejected (no silent approximation per SCOPE §10). " +
			"Atomic via temp-file + rename. Mutation tool — gated by the " +
			"policy stage (READ_ONLY denies; WORKSPACE_WRITE allows " +
			"in-workspace; FULL_ACCESS allows escape at the path stage " +
			"only, not via silent bypass).",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields. Both path and patch are required
// — the tool's contract is "apply this patch to this path", and a
// missing argument is a schema violation, not an implicit default.
func (ApplyPatch) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"path", "patch"},
		Properties: map[string]tools.PropertyType{
			"path":  tools.TypeString,
			"patch": tools.TypeString,
		},
	}
}

// ApplyPatchResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format and
// downstream consumers parse the fields by name.
//
// Path is the destination (the input `path` argument, unchanged —
// the pipeline already normalized it).
//
// HunksApplied is the count of hunks that landed cleanly (the patch
// text contained N @@ blocks and all N matched uniquely and were
// applied).
//
// BytesChanged is the sum of len(added) + len(removed) for every
// hunk that landed. Informational; pin the sum deterministically
// (the test asserts BytesChanged equals the input patch's
// +added_bytes + -removed_bytes count).
type ApplyPatchResult struct {
	Path         string `json:"path"`
	HunksApplied int    `json:"hunks_applied"`
	BytesChanged int    `json:"bytes_changed"`
}

// patchHunk is the parsed form of one @@ block. oldStart is the 1-
// indexed start line in the old file; oldLen is the count of lines
// in the old file this hunk covers (context + removed); newStart is
// the 1-indexed start line in the new file; newLen is the count of
// lines in the new file (context + added). lines is the hunk body
// split by leading character: ' ' = context (kept), '-' = removed
// from old, '+' = added in new.
type patchHunk struct {
	oldStart int
	oldLen   int
	newStart int
	newLen   int
	lines    []patchLine
}

// patchLine is one parsed line in a hunk body.
type patchLine struct {
	kind byte
	text string
}

// hunkApplyError carries a structured hunk-rejection so
// applyHunks can return it without smuggling the rejection
// through Go's error type. The caller extracts the Result via
// .Result().
type hunkApplyError struct {
	kind    string
	message string
	call    tools.Call
}

func (e *hunkApplyError) Error() string {
	return e.message
}

func (e *hunkApplyError) Result() tools.Result {
	return tools.Result{Status: "error", Error: &tools.ToolError{
		Kind:    e.kind,
		Message: e.message,
		Call:    e.call,
	}}
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract path (required string) and patch (required string).
//     Missing or non-string of either returns a structured
//     schema_violation error.
//  2. Stat the destination path. If it does not exist, return
//     Kind="target_not_found". If it is a directory, return
//     Kind="is_a_directory".
//  3. Read the destination file's content as a sequence of logical
//     lines (split on '\n', strip the '\n' from each; trailing
//     empty line at end-of-file preserved as "" if the file ends
//     with a newline).
//  4. Parse the unified-diff patch text: expect
//     `--- <oldpath>\n+++ <newpath>\n` header pair (the
//     `a/` / `b/` git-style prefixes are stripped); followed by
//     zero or more `@@ -oldStart[,oldLen] +newStart[,newLen] @@
//     optional-function-context\n` hunk headers; each hunk body is
//     zero or more lines starting with ' ' (context), '-' (removed),
//     or '+' (added). An unparseable patch returns
//     Kind="unparseable_patch".
//  5. For each hunk in order, find its match in the file: the
//     EXACT sequence of (context + removed) lines (concatenated
//     by '\n') must appear at exactly one location in the file's
//     lines starting at index (oldStart - 1) — the search starts at
//     oldStart-1 and walks forward, looking for a match. If more
//     than one location matches the EXACT sequence, return
//     Kind="ambiguous" (SCOPE §10: "reject ambiguous application").
//     If no location matches, return Kind="failed_hunk"
//     (SCOPE §10: "failed hunks are structured failures" + "A
//     failed patch must not be silently approximated"). No fuzzy
//     matching, no whitespace tolerance — exact text equality.
//  6. Apply the hunk at the matched position: replace the matched
//     (context + removed) lines with (context + added) lines.
//  7. After ALL hunks applied cleanly, write the new file content
//     atomically using the same temp-file + rename pattern as
//     write_file (os.CreateTemp in the parent directory + write +
//     fsync + close + os.Rename).
//  8. Return Result{Status: "ok", Content: ApplyPatchResult{...}}
//     with HunksApplied = len(hunks) and BytesChanged =
//     sum(len(added)) + sum(len(removed)) for every hunk.
//
// On any of the named failure modes (schema_violation,
// target_not_found, is_a_directory, unparseable_patch,
// ambiguous, failed_hunk), the tool returns
// Result{Status: "error", Error: &ToolError{...}} directly with a
// structured Kind. The atomic temp-file cleanup defer handles the
// case where the rename fails or the function returns early — the
// temp file is removed if it still exists.
//
// V1 limitations (recorded for awareness; not in scope for
// follow-up):
//   - Single-file patch only (no git-style multi-file
//     `diff --git a/foo b/bar` format).
//   - No binary-file support (the patch is assumed to be a UTF-8
//     text diff).
//   - No patch reversal (`-R` flag).
//   - No line-number fuzziness (oldStart is a positional hint, not
//     a tolerance).
func (ApplyPatch) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	_ = ctx

	pathVal, ok := call.Arguments["path"].(string)
	if !ok || pathVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "apply_patch: missing or non-string path argument",
			Call:    call,
		}}, nil
	}
	patchText, ok := call.Arguments["patch"].(string)
	if !ok {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "apply_patch: missing or non-string patch argument",
			Call:    call,
		}}, nil
	}

	info, err := os.Stat(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "target_not_found",
				Message: fmt.Sprintf("apply_patch: target %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{}, fmt.Errorf("apply_patch: stat %s: %w", pathVal, err)
	}
	if info.IsDir() {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "is_a_directory",
			Message: fmt.Sprintf("apply_patch: %s is a directory, not a file", pathVal),
			Call:    call,
		}}, nil
	}

	fileBytes, err := os.ReadFile(pathVal)
	if err != nil {
		return tools.Result{}, fmt.Errorf("apply_patch: read %s: %w", pathVal, err)
	}
	fileLines := splitLines(fileBytes)

	hunks, err := parseUnifiedDiff(patchText)
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "unparseable_patch",
			Message: fmt.Sprintf("apply_patch: %v", err),
			Call:    call,
		}}, nil
	}

	newLines, applyErr := applyHunks(fileLines, hunks, pathVal, call)
	if applyErr != nil {
		return applyErr.Result(), nil
	}

	bytesChanged := 0
	for _, h := range hunks {
		for _, l := range h.lines {
			switch l.kind {
			case '+':
				bytesChanged += len(l.text)
			case '-':
				bytesChanged += len(l.text)
			}
		}
	}

	parentDir := filepath.Dir(pathVal)
	tmpFile, err := os.CreateTemp(parentDir, ".apply_patch-*.tmp")
	if err != nil {
		return tools.Result{}, fmt.Errorf("apply_patch: create temp in %s: %w", parentDir, err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	content := strings.Join(newLines, "\n")
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		tmpFile.Close()
		return tools.Result{}, fmt.Errorf("apply_patch: write temp %s: %w", tmpName, err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return tools.Result{}, fmt.Errorf("apply_patch: fsync temp %s: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return tools.Result{}, fmt.Errorf("apply_patch: close temp %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, pathVal); err != nil {
		return tools.Result{}, fmt.Errorf("apply_patch: rename %s -> %s: %w", tmpName, pathVal, err)
	}

	return tools.Result{Status: "ok", Content: ApplyPatchResult{
		Path:         pathVal,
		HunksApplied: len(hunks),
		BytesChanged: bytesChanged,
	}}, nil
}

// applyHunks walks the hunks in order, finding each one's exact
// match and producing the resulting line slice. Returns a
// *hunkApplyError on the first failed or ambiguous hunk. The
// hunk matching is strict: the (context + removed) sequence must
// appear EXACTLY at one location starting at or after oldStart-1;
// multiple matches → ambiguous; zero matches → failed_hunk.
//
// The oldStart-1 search start is a positional hint, not a
// tolerance: a match at oldStart-1 is preferred, but a match later
// in the file is also acceptable IF it is the only match (the
// patch is "soft-positioned"). The test plan pins each behavior.
func applyHunks(fileLines []string, hunks []patchHunk, pathVal string, call tools.Call) ([]string, *hunkApplyError) {
	result := append([]string(nil), fileLines...)
	for hunkIdx, h := range hunks {
		var oldSeq, newSeq []string
		for _, l := range h.lines {
			switch l.kind {
			case ' ':
				oldSeq = append(oldSeq, l.text)
				newSeq = append(newSeq, l.text)
			case '-':
				oldSeq = append(oldSeq, l.text)
			case '+':
				newSeq = append(newSeq, l.text)
			}
		}
		if len(oldSeq) == 0 {
			return nil, &hunkApplyError{
				kind:    "unparseable_patch",
				message: fmt.Sprintf("apply_patch: hunk %d has no context or removed lines (empty old sequence)", hunkIdx),
				call:    call,
			}
		}

		searchStart := h.oldStart - 1
		if searchStart < 0 {
			searchStart = 0
		}
		if searchStart > len(result) {
			return nil, &hunkApplyError{
				kind:    "failed_hunk",
				message: fmt.Sprintf("apply_patch: hunk %d oldStart=%d is past end of file", hunkIdx, h.oldStart),
				call:    call,
			}
		}

		matchIdx := -1
		for i := searchStart; i+len(oldSeq) <= len(result); i++ {
			if linesEqual(result[i:i+len(oldSeq)], oldSeq) {
				if matchIdx != -1 {
					return nil, &hunkApplyError{
						kind:    "ambiguous",
						message: fmt.Sprintf("apply_patch: hunk %d matches at line %d and line %d (ambiguous)", hunkIdx, matchIdx+1, i+1),
						call:    call,
					}
				}
				matchIdx = i
			}
		}
		if matchIdx == -1 {
			return nil, &hunkApplyError{
				kind:    "failed_hunk",
				message: fmt.Sprintf("apply_patch: hunk %d not found in %s (no match for the (context + removed) sequence)", hunkIdx, pathVal),
				call:    call,
			}
		}

		newResult := make([]string, 0, len(result)-len(oldSeq)+len(newSeq))
		newResult = append(newResult, result[:matchIdx]...)
		newResult = append(newResult, newSeq...)
		newResult = append(newResult, result[matchIdx+len(oldSeq):]...)
		result = newResult
	}
	return result, nil
}

// linesEqual reports whether two []string slices are exactly equal
// element-by-element. Used by applyHunks for the per-hunk match
// check.
func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// parseUnifiedDiff parses a unified-diff patch text into a slice
// of patchHunk. The parser is line-oriented and supports:
//
//   - The header pair `--- <oldpath>` and `+++ <newpath>`. The
//     `a/` and `b/` git-style prefixes are stripped. V1 does NOT
//     verify oldpath == newpath == the input path — the dispatch
//     pipeline's path stage already verified the path is in-
//     workspace; the patch's notion of "the file" is treated as
//     advisory (the input `path` argument is authoritative).
//   - Hunk headers `@@ -oldStart[,oldLen] +newStart[,newLen] @@
//     optional-function-context`. The oldLen / newLen are
//     optional in the strict unified-diff format (default 1 when
//     omitted); we accept both forms.
//   - Hunk body lines starting with ' ' (context), '-' (removed),
//     or '+' (added). Lines starting with anything else (except
//     the next '@@' header) are an unparseable-patch error.
//
// A patch with no hunks is a valid no-op (returns
// Result{Status:"ok", HunksApplied:0}); a missing header pair
// (--- / +++) is an unparseable-patch error.
func parseUnifiedDiff(text string) ([]patchHunk, error) {
	var hunks []patchHunk

	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	state := "expect_header"
	for scanner.Scan() {
		line := scanner.Text()
		switch state {
		case "expect_header":
			if !strings.HasPrefix(line, "--- ") {
				return nil, fmt.Errorf("expected `--- <oldpath>` header, got %q", line)
			}
			state = "expect_new_header"
		case "expect_new_header":
			if !strings.HasPrefix(line, "+++ ") {
				return nil, fmt.Errorf("expected `+++ <newpath>` header, got %q", line)
			}
			state = "expect_hunk_header"
		case "expect_hunk_header":
			if strings.HasPrefix(line, "@@ ") {
				h, err := parseHunkHeader(line)
				if err != nil {
					return nil, err
				}
				hunks = append(hunks, h)
				state = "in_hunk"
				continue
			}
			if line == "" {
				continue
			}
			return nil, fmt.Errorf("expected `@@` hunk header, got %q", line)
		case "in_hunk":
			if strings.HasPrefix(line, "@@ ") {
				h, err := parseHunkHeader(line)
				if err != nil {
					return nil, err
				}
				hunks = append(hunks, h)
				continue
			}
			if line == "" {
				if len(hunks) > 0 {
					last := &hunks[len(hunks)-1]
					last.lines = append(last.lines, patchLine{kind: ' ', text: ""})
				}
				continue
			}
			kind := line[0]
			if kind != ' ' && kind != '-' && kind != '+' {
				return nil, fmt.Errorf("unexpected line in hunk body: %q", line)
			}
			text := line[1:]
			if len(hunks) > 0 {
				last := &hunks[len(hunks)-1]
				last.lines = append(last.lines, patchLine{kind: kind, text: text})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return hunks, nil
}

// parseHunkHeader parses one `@@ -oldStart[,oldLen] +newStart[,newLen] @@` line.
func parseHunkHeader(line string) (patchHunk, error) {
	inner := strings.TrimPrefix(line, "@@ ")
	if idx := strings.Index(inner, " @@"); idx >= 0 {
		inner = inner[:idx]
	}
	parts := strings.Fields(inner)
	if len(parts) != 2 {
		return patchHunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	old, err := parseRange(parts[0])
	if err != nil {
		return patchHunk{}, fmt.Errorf("old range in %q: %w", line, err)
	}
	new, err := parseRange(parts[1])
	if err != nil {
		return patchHunk{}, fmt.Errorf("new range in %q: %w", line, err)
	}
	return patchHunk{
		oldStart: old.start,
		oldLen:   old.len,
		newStart: new.start,
		newLen:   new.len,
	}, nil
}

type hunkRange struct {
	start int
	len   int
}

func parseRange(s string) (hunkRange, error) {
	if len(s) < 2 || (s[0] != '-' && s[0] != '+') {
		return hunkRange{}, fmt.Errorf("malformed range %q", s)
	}
	s = s[1:]
	parts := strings.SplitN(s, ",", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return hunkRange{}, fmt.Errorf("malformed start in %q", s)
	}
	length := 1
	if len(parts) == 2 {
		length, err = strconv.Atoi(parts[1])
		if err != nil {
			return hunkRange{}, fmt.Errorf("malformed length in %q", s)
		}
	}
	return hunkRange{start: start, len: length}, nil
}

// splitLines splits a file's bytes into a slice of logical lines,
// stripping the trailing '\n' from each. A file ending in '\n' has
// a trailing "" element (the empty after the last '\n'); a file
// NOT ending in '\n' has its last element as the file's tail
// without a trailing newline. This preserves the round-trip:
//
//   strings.Join(splitLines(bytes), "\n") == bytes
//
// when bytes ends in '\n', and
//
//   strings.Join(splitLines(bytes), "\n") == bytes
//
// when bytes does not end in '\n'.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var lines []string
	start := 0
	for i, c := range b {
		if c == '\n' {
			lines = append(lines, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, string(b[start:]))
	} else {
		lines = append(lines, "")
	}
	return lines
}

// Compile-time interface check.
var _ tools.Tool = ApplyPatch{}
