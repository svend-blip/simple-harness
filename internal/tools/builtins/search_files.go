package builtins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// SearchFiles is the search_files builtin tool. It walks a workspace-
// relative directory recursively and returns the relative paths of
// files whose name (basename) contains the given substring pattern.
// The walk is bounded by the workspace root: a `..` segment or an
// absolute path is rejected by the dispatch pipeline's path-
// normalization step before the tool's Execute runs; the tool itself
// does NOT re-normalize paths.
//
// The tool is native Go only — no `rg` shell-out. The walk uses
// filepath.WalkDir for efficiency. Hidden directories (names starting
// with `.`) ARE walked, except for the literal `.git` directory (the
// V1 convention; matches the model-allocator's `.gitignore` behavior
// without requiring a `.gitignore` parser). Hidden files (dotfiles)
// ARE included in the result — only the `.git` directory is special.
type SearchFiles struct{}

// Meta implements tools.Tool.
func (SearchFiles) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "search_files",
		Description: "Recursively find files in a workspace directory whose name contains the given substring pattern. Returns matching relative paths, sorted.",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields.
func (SearchFiles) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"pattern"},
		Properties: map[string]tools.PropertyType{
			"pattern":     tools.TypeString,
			"path":        tools.TypeString,
			"max_results": tools.TypeInt,
		},
	}
}

// SearchFilesResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format and
// downstream consumers parse the fields by name. The seam choice
// (struct vs map encoding) is recorded in the handoff 015 result
// file's "Seam choices" subsection: this Run's implementer chose
// the struct form for type safety at the call site.
type SearchFilesResult struct {
	Files   []string `json:"files"`
	Pattern string   `json:"pattern"`
	Path    string   `json:"path"`
}

// searchFilesDefaultMax is the default result cap when the call does
// not specify max_results. SCOPE §8 requires "observable start/
// completion" and "structured result"; a hard cap matches the "without
// forcing the model to consume entire source trees" requirement of
// SCOPE §9.
const searchFilesDefaultMax = 1000

// searchFilesMaxCap is the maximum value the implementer accepts for
// max_results. Values above this are clamped to searchFilesMaxCap
// (defensive — the model is not expected to ask for more than the
// default cap, but a runaway argument does not OOM the process).
const searchFilesMaxCap = 10000

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract pattern (required, substring), path (optional, default
//     "."), max_results (optional, default searchFilesDefaultMax,
//     capped at searchFilesMaxCap).
//  2. Stat the call's path. os.IsNotExist → Kind: "not_found";
//     regular file (not a directory) → Kind: "not_a_directory".
//  3. filepath.WalkDir the directory, skipping the literal ".git"
//     directory at any depth (fs.SkipDir). For each non-directory
//     file, check if the basename contains the pattern (case-
//     sensitive substring; the V1 contract is case-sensitive).
//  4. Collect matching relative paths (relative to the call's path
//     argument, NOT to the workspace root) up to max_results. If the
//     cap is hit, truncate and continue (do not error); the cap is
//     observable from the result's `files` length.
//  5. Sort the collected paths alphabetically (case-sensitive).
//  6. Result{Status: "ok", Content: SearchFilesResult{...}}.
//
// No-match result shape (seam choice recorded in the result file):
// an empty match returns Files as a non-nil empty slice (not nil);
// JSON encodes an empty slice as `[]` rather than `null`. The
// contract is "always a JSON array, possibly empty".
func (SearchFiles) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for future cancellation hooks; today's
	// filepath.WalkDir does not honor it.
	_ = ctx

	patternVal, ok := call.Arguments["pattern"].(string)
	if !ok || patternVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: "search_files: missing or non-string pattern argument",
			Call:    call,
		}}, nil
	}

	pathVal := "."
	if v, present := call.Arguments["path"]; present {
		if s, ok := v.(string); ok && s != "" {
			pathVal = s
		}
	}

	maxResults := searchFilesDefaultMax
	if v, present := call.Arguments["max_results"]; present {
		if iv, ok := intArg(v); ok {
			maxResults = iv
		} else {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "schema_violation",
				Message: "search_files: max_results is not an int",
				Call:    call,
			}}, nil
		}
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > searchFilesMaxCap {
		maxResults = searchFilesMaxCap
	}

	// Stat the call's path. The dispatch pipeline has already
	// normalized the argument against the workspace root; the
	// tool operates on the post-pipeline form. Direct-Execute
	// callers (tests) pass a relative path the test's t.TempDir
	// can resolve.
	info, err := os.Stat(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("search_files: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("search_files: stat %s: %v", pathVal, err),
			Call:    call,
		}}, nil
	}
	if !info.IsDir() {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_a_directory",
			Message: fmt.Sprintf("search_files: %s is not a directory", pathVal),
			Call:    call,
		}}, nil
	}

	// The walk produces paths relative to pathVal. We capture them
	// in `matches` and slice to maxResults on completion. The walk
	// itself is unbounded; the cap is applied after the walk so
	// the result remains deterministic (alphabetically sorted) on
	// every workspace.
	var matches []string
	walkErr := filepath.WalkDir(pathVal, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip the literal .git directory at any depth. fs.SkipDir
		// tells WalkDir to not recurse into this directory; since
		// we skip the directory entirely before any file check, no
		// file inside .git ever enters the result.
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		// Non-directory file: check basename for substring match.
		// strings.Contains is case-sensitive (the V1 contract).
		if strings.Contains(d.Name(), patternVal) {
			rel, relErr := filepath.Rel(pathVal, p)
			if relErr != nil {
				return relErr
			}
			matches = append(matches, rel)
		}
		return nil
	})
	if walkErr != nil {
		// A walk error (other than SkipDir, which is handled
		// internally by WalkDir) is rare — usually a permission
		// problem on a subtree. Surface as a structured error.
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("search_files: walk %s: %v", pathVal, walkErr),
			Call:    call,
		}}, nil
	}

	// Sort for determinism (WalkDir order is OS-dependent).
	sort.Strings(matches)

	// Truncate to maxResults. If matches has fewer than maxResults,
	// this is a no-op. The truncation is observable from the
	// result's `files` length — the contract is "at most max_results
	// matches, sorted alphabetically".
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	// Ensure a non-nil empty slice (not nil) so JSON encodes `[]`
	// rather than `null` for the no-match case.
	if matches == nil {
		matches = []string{}
	}

	return tools.Result{Status: "ok", Content: SearchFilesResult{
		Files:   matches,
		Pattern: patternVal,
		Path:    pathVal,
	}}, nil
}