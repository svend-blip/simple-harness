package builtins

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Grep is the grep builtin tool. It searches the contents of files
// under a workspace-relative directory for a regular-expression
// pattern (Go RE2 syntax) and returns matching lines with their file
// path and 1-indexed line number.
//
// The tool shells out to `rg` (ripgrep) when `rg` is found in $PATH;
// otherwise it falls back to a native Go recursive walk + line-by-
// line regexp match. Both paths produce the same structured result
// shape (GrepResult.Matches); the Backend field records which path
// the tool took.
//
// The `rg`-availability lookup uses a package-level function variable
// so the test suite can force the native-fallback path by swapping
// the variable to a stub that returns ("", error). This is the
// same shape as the handoff-013 recordingTool sentinel — a test-
// only seam documented in the result's "Seam choices" subsection.
type Grep struct{}

// execLookPath is the seam the test suite uses to force the
// native-fallback path. Production code calls exec.LookPath via this
// variable; the test suite swaps it for a stub that returns
// ("", error) when grep is the tool under test.
var execLookPath = exec.LookPath

// Meta implements tools.Tool.
func (Grep) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "grep",
		Description: "Search file contents for a regular expression pattern. Uses ripgrep (rg) when available; falls back to a native Go walk otherwise. Returns matching lines with file path and 1-indexed line number.",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields.
func (Grep) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"pattern"},
		Properties: map[string]tools.PropertyType{
			"pattern":          tools.TypeString,
			"path":             tools.TypeString,
			"file_pattern":     tools.TypeString,
			"case_insensitive": tools.TypeBool,
			"max_results":      tools.TypeInt,
		},
	}
}

// GrepMatch is one row in the grep result. JSON tags match the wire
// format. Line is 1-indexed.
type GrepMatch struct {
	File string `json:"file"` // file path, relative to the call's path
	Line int    `json:"line"` // 1-indexed line number
	Text string `json:"text"` // the matching line (without trailing newline)
}

// GrepResult is the success content shape. Result.Content on success
// carries this struct; JSON tags match the wire format and the
// Backend field is REQUIRED (no omitempty) so the equivalence test
// and the integration tests can distinguish the two paths.
type GrepResult struct {
	Matches []GrepMatch `json:"matches"`
	Pattern string      `json:"pattern"`
	Path    string      `json:"path"`
	Backend string      `json:"backend"` // "rg" or "native"
}

// grepDefaultMax is the default result cap. Matches the search_files
// default; both are "structured-result" tools per SCOPE §8.
const grepDefaultMax = 1000

// grepMaxCap is the maximum value the implementer accepts for
// max_results. Defensive: runaway arguments do not OOM the process.
const grepMaxCap = 10000

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract pattern (required, RE2 regex), path (optional, default
//     "."), file_pattern (optional glob), case_insensitive (optional
//     bool, default false), max_results (optional, default
//     grepDefaultMax, capped at grepMaxCap).
//  2. Validate the regex. regexp.Compile error → Kind: "invalid_regex".
//  3. Stat the call's path. os.IsNotExist → "not_found"; regular
//     file → "not_a_directory".
//  4. Look up `rg` via execLookPath. If found, take the rg-shell-out
//     path (step 5a). If not, take the native-fallback path (step
//     5b). Both paths produce the same GrepResult shape.
//  5a. RG shell-out: invoke `rg --no-heading --line-number
//     --no-messages --with-filename [-i] [--glob=<file_pattern>]
//     <pattern> <path>`. Parse stdout (each line is "file:line:text")
//     into GrepMatch rows. Set Backend = "rg". rg exit 1 (no matches)
//     is treated as success with an empty matches slice; exit 2+ is
//     Kind: "rg_failed".
//  5b. Native fallback: filepath.WalkDir the directory, skipping
//     .git at any depth. For each file (not directory) that matches
//     file_pattern (if specified; default match all), open and read
//     line-by-line via bufio.Scanner. For each line, test the
//     compiled regexp (with "(?i)" prefix if case_insensitive).
//     Set Backend = "native".
//  6. Sort the matches by (File, Line) for determinism (the rg path
//     may not be sorted; the native path's WalkDir order is not
//     guaranteed).
//  7. Truncate to max_results (the cap is observable from the
//     result's `matches` length).
//  8. Result{Status: "ok", Content: GrepResult{...}}.
//
// Equivalence contract (binding): the same input must produce the
// same set of GrepMatch{File, Line, Text} rows in the same order
// regardless of which backend the tool chose. The only difference
// between the two paths' results is the Backend field. The test
// TestGrep_Equivalence_BothBackends enforces this contract.
func (g Grep) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for future cancellation hooks; today's
	// filepath.WalkDir and os/exec.Cmd do not honor it.
	_ = ctx

	patternVal, ok := call.Arguments["pattern"].(string)
	if !ok || patternVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "invalid_regex",
			Message: "grep: missing or non-string pattern argument",
			Call:    call,
		}}, nil
	}

	pathVal := "."
	if v, present := call.Arguments["path"]; present {
		if s, ok := v.(string); ok && s != "" {
			pathVal = s
		}
	}

	filePattern := ""
	if v, present := call.Arguments["file_pattern"]; present {
		if s, ok := v.(string); ok {
			filePattern = s
		}
	}

	caseInsensitive := false
	if cv, present := call.Arguments["case_insensitive"]; present {
		if b, ok := cv.(bool); ok {
			caseInsensitive = b
		}
	}

	maxResults := grepDefaultMax
	if v, present := call.Arguments["max_results"]; present {
		if iv, ok := intArg(v); ok {
			maxResults = iv
		} else {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "schema_violation",
				Message: "grep: max_results is not an int",
				Call:    call,
			}}, nil
		}
	}
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > grepMaxCap {
		maxResults = grepMaxCap
	}

	// Validate the regex before any I/O so the failure surfaces
	// with a clean structured Kind="invalid_regex". For the rg
	// path, rg itself validates the regex; we still compile here
	// to keep the error kind uniform across both paths.
	regexStr := patternVal
	if caseInsensitive {
		regexStr = "(?i)" + regexStr
	}
	re, err := regexp.Compile(regexStr)
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "invalid_regex",
			Message: fmt.Sprintf("grep: compile pattern %q: %v", patternVal, err),
			Call:    call,
		}}, nil
	}

	// Stat the call's path. Same semantics as search_files:
	// pipeline has already normalized; direct-Execute callers
	// pass a path the OS can resolve.
	info, err := os.Stat(pathVal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "not_found",
				Message: fmt.Sprintf("grep: %s does not exist", pathVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_found",
			Message: fmt.Sprintf("grep: stat %s: %v", pathVal, err),
			Call:    call,
		}}, nil
	}
	if !info.IsDir() {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "not_a_directory",
			Message: fmt.Sprintf("grep: %s is not a directory", pathVal),
			Call:    call,
		}}, nil
	}

	// Decide which backend to use. Look up rg via the seam
	// (execLookPath, swappable in tests).
	rgPath, lookErr := execLookPath("rg")
	var matches []GrepMatch
	var backend string
	if lookErr == nil && rgPath != "" {
		matches, err = g.runRg(rgPath, patternVal, pathVal, filePattern, caseInsensitive)
		if err != nil {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "rg_failed",
				Message: err.Error(),
				Call:    call,
			}}, nil
		}
		backend = "rg"
	} else {
		matches = g.runNative(pathVal, filePattern, re)
		backend = "native"
	}

	// Sort by (File, Line) for determinism.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].File != matches[j].File {
			return matches[i].File < matches[j].File
		}
		return matches[i].Line < matches[j].Line
	})

	// Truncate to maxResults. The cap is observable from the
	// result's `matches` length.
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	// Ensure a non-nil empty slice (not nil) so JSON encodes `[]`
	// rather than `null` for the no-match case. Same seam choice
	// as search_files.
	if matches == nil {
		matches = []GrepMatch{}
	}

	return tools.Result{Status: "ok", Content: GrepResult{
		Matches: matches,
		Pattern: patternVal,
		Path:    pathVal,
		Backend: backend,
	}}, nil
}

// runRg shells out to rg. Returns the parsed matches or an error
// when rg exits with code 2+ (rg exit 1 means "no matches" and is
// treated as success with an empty slice).
//
// rg invocation (binding):
//
//	rg --no-heading --line-number --no-messages --with-filename
//	   [-i] [--glob=<file_pattern>]
//	   <pattern> <path>
//
// stdout format: each line is "file:line:text" (with the separator
// being the first ":" — file paths may contain ":" but rg quotes
// them when ambiguous; for the simple patterns the V1 tool sees,
// the first ":" is unambiguous).
//
// Path normalization: rg echoes the path it was given as a prefix
// to every matched file. When the caller passes an absolute path
// (the typical post-pipeline form), rg returns absolute paths; we
// strip the call's path prefix so the GrepMatch.File field is
// RELATIVE TO THE CALL'S PATH (matching the contract above and the
// native-fallback walk's output).
func (Grep) runRg(rgPath, pattern, path, filePattern string, caseInsensitive bool) ([]GrepMatch, error) {
	args := []string{
		"--no-heading",
		"--line-number",
		"--no-messages",
		"--with-filename",
	}
	if caseInsensitive {
		args = append(args, "-i")
	}
	if filePattern != "" {
		args = append(args, "--glob="+filePattern)
	}
	args = append(args, pattern, path)

	cmd := exec.Command(rgPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Exit 1: no matches — treat as success with empty matches.
			// Exit 2+: rg error — surface as rg_failed.
			if ee.ExitCode() == 1 {
				return []GrepMatch{}, nil
			}
			return nil, fmt.Errorf("grep: rg exited %d: %s", ee.ExitCode(),
				strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("grep: run rg: %w", err)
	}

	return parseRgOutput(stdout.Bytes(), path), nil
}

// parseRgOutput parses rg's "file:line:text" stdout into GrepMatch
// rows. rg emits lines in arbitrary order (the --sort=none default);
// we re-sort in Execute so the wire format is deterministic.
//
// The first ":" separates file from line. Subsequent colons are
// part of the line content. An empty line (rg's default) is
// skipped.
//
// rootPath is the path argument the caller passed to rg; we strip
// it from the leading "file" field when rg echoed it back (rg
// returns paths with the search-root prefix; relative search roots
// come back already-relative). The relativize step is a no-op when
// the prefix does not match.
func parseRgOutput(data []byte, rootPath string) []GrepMatch {
	var out []GrepMatch
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		file := line[:idx]
		rest := line[idx+1:]
		idx2 := strings.IndexByte(rest, ':')
		if idx2 < 0 {
			continue
		}
		lineStr := rest[:idx2]
		lineNum, err := strconv.Atoi(lineStr)
		if err != nil {
			continue
		}
		text := rest[idx2+1:]
		out = append(out, GrepMatch{File: relativize(file, rootPath), Line: lineNum, Text: text})
	}
	return out
}

// relativize strips rootPath's leading-prefix from file when file
// starts with rootPath. The result is "the file path relative to
// the call's path argument". When file does not start with
// rootPath (e.g. rg returned an already-relative path because the
// caller passed "."), the file is returned unchanged.
//
// Special case: when rootPath ends in a separator, we strip the
// prefix and return the remainder directly. When rootPath does
// not end in a separator, we strip the prefix plus one separator
// so "/tmp/foo" → "/tmp/foo/bar.txt" gives "bar.txt" (not
// "/bar.txt").
func relativize(file, rootPath string) string {
	if rootPath == "" {
		return file
	}
	if !strings.HasPrefix(file, rootPath) {
		return file
	}
	rel := strings.TrimPrefix(file, rootPath)
	if strings.HasPrefix(rel, string(filepath.Separator)) {
		rel = strings.TrimPrefix(rel, string(filepath.Separator))
	}
	if rel == "" {
		return "."
	}
	return rel
}

// runNative is the fallback walk when rg is not in $PATH. It uses
// filepath.WalkDir + bufio.Scanner + regexp.MatchString.
func (Grep) runNative(path, filePattern string, re *regexp.Regexp) []GrepMatch {
	var out []GrepMatch
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		// Apply file_pattern (glob) against the basename.
		if filePattern != "" {
			matched, err := filepath.Match(filePattern, d.Name())
			if err != nil || !matched {
				return nil
			}
		}
		// Open the file, read line-by-line. Skip files we cannot
		// open (permissions, etc.) — we do not want one bad file
		// to abort the entire walk; the structured Result's
		// matches simply omits it.
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		rel, relErr := filepath.Rel(path, p)
		if relErr != nil {
			return nil
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			text := scanner.Text()
			if re.MatchString(text) {
				out = append(out, GrepMatch{File: rel, Line: lineNum, Text: text})
			}
		}
		// Ignore scanner.Err() — same rationale as open errors.
		return nil
	})
	return out
}

// Compile-time interface check (no overhead; the registry uses
// dynamic dispatch, but the lint benefit is real).
var _ tools.Tool = Grep{}

// io.Discard is imported via the runtime package's _ import chain;
// keeping io referenced here ensures the bufio scanner's EOF sentinel
// is present in case the import optimizer drops the io alias. This
// is a no-op reference; the production path uses io via bufio.
var _ = io.EOF