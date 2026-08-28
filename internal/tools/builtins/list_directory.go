package builtins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// ListDirectory is the list_directory builtin tool. It lists the
// direct children of a workspace-relative directory, sorted by name,
// with each entry's type and (for files) size.
//
// The tool is one-level only; recursive listing is search_files's
// territory (handoff 015). Hidden files (dotfiles) ARE included; the
// tool is "what's in this directory", not "what a casual listing
// shows".
type ListDirectory struct{}

// Meta implements tools.Tool.
func (ListDirectory) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "list_directory",
		Description: "List the direct children of a workspace directory, sorted by name. Each entry carries type (file|dir) and size_bytes (files only).",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields.
func (ListDirectory) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"path"},
		Properties: map[string]tools.PropertyType{
			"path": tools.TypeString,
		},
	}
}

// ListDirectoryEntry is one row in the listing. JSON tags match the
// wire format. SizeBytes is omitted for directories via the
// omitempty tag (seam choice recorded in the result file).
type ListDirectoryEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "file" or "dir"
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// ListDirectoryResult is the success content shape. Result.Content
// on success carries a map[string]any with this shape:
//
//	{
//	  "entries": []ListDirectoryEntry,
//	  "path":    string
//	}
//
// The seam choice (struct vs map encoding) is recorded in the
// handoff 014 result file.
type ListDirectoryResult struct {
	Entries []ListDirectoryEntry `json:"entries"`
	Path    string               `json:"path"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract path (required string).
//  2. Stat the path. os.IsNotExist → Kind: "not_found". Regular
//     file (not a directory) → Kind: "not_a_directory".
//  3. os.ReadDir(path). For each entry:
//     - directory: Type="dir", SizeBytes omitted.
//     - file (or symlink, mode aside): Type="file", SizeBytes =
//       entry.Info.Size().
//  4. Sort the entries by Name (case-sensitive; os.ReadDir's
//     alphabetical order is OS-dependent, so we re-sort to make the
//     contract explicit and stable across platforms).
//  5. Return Result{Status: "ok", Content: map[string]any{...}}.
func (ListDirectory) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for future cancellation hooks; today's
	// os.ReadDir does not honor it.
	_ = ctx

	pathVal, ok := call.Arguments["path"].(string)
	if !ok || pathVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: "list_directory: missing or non-string path argument",
			Call:    call,
		}}, nil
	}

	info, err := os.Stat(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("list_directory: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("list_directory: stat %s: %v", pathVal, err),
			Call:    call,
		}}, nil
	}
	if !info.IsDir() {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_a_directory",
			Message: fmt.Sprintf("list_directory: %s is not a directory", pathVal),
			Call:    call,
		}}, nil
	}

	entries, err := os.ReadDir(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("list_directory: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{}, fmt.Errorf("list_directory: readdir %s: %w", pathVal, err)
	}

	out := make([]ListDirectoryEntry, 0, len(entries))
	for _, e := range entries {
		entry := ListDirectoryEntry{Name: e.Name()}
		// IsDir reports the directory-bit in the entry's mode
		// without following symlinks (Lstat semantics, baked
		// into os.ReadDir per Go docs).
		if e.IsDir() {
			entry.Type = "dir"
			// SizeBytes omitted for directories.
		} else {
			entry.Type = "file"
			info, err := e.Info()
			if err != nil {
				return tools.Result{}, fmt.Errorf("list_directory: stat %s/%s: %w",
					pathVal, e.Name(), err)
			}
			entry.SizeBytes = info.Size()
		}
		out = append(out, entry)
	}

	// Sort by name (case-sensitive, lexicographic). os.ReadDir
	// already returns alphabetical order on most platforms, but
	// the contract is "sorted by name", so we re-sort to be
	// explicit and immune to platform differences.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return tools.Result{Status: "ok", Content: map[string]any{
		"entries": out,
		"path":    pathVal,
	}}, nil
}