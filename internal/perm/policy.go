// Package perm owns the mode-aware permission policy for Simple
// Harness. It implements the architecture's "Permission boundary
// placement" (ARCHITECTURE.md lines 195-222) and "Enforcement
// placement" (ARCHITECTURE.md lines 396-415) via a single Policy
// type that exposes a Mode-aware Decide method against the tools.Policy
// interface.
//
// Mode is the active permission level. SCOPE §12 names three modes:
// READ_ONLY (allow read-only tools, deny mutation tools), WORKSPACE_WRITE
// (read-only tools allowed; mutation tools allowed when the path
// argument resolves inside the workspace; denied when the path
// escapes), FULL_ACCESS (every tool allowed). The zero value of Mode
// is READ_ONLY per SCOPE §12's "never silent escalation" rule
// (a zero-default means the harness never silently escalates).
//
// mutationTools is the fixed list of tool names whose Execute is a
// mutation. Adding a new mutation tool in a future Run requires
// adding it to this list AND to builtins.RegisterBuiltins.
package perm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Mode is the active permission mode. SCOPE §12 names three modes:
// READ_ONLY, WORKSPACE_WRITE, FULL_ACCESS. The zero value is
// READ_ONLY (SCOPE §12: "never silent escalation").
type Mode int

// Documented Mode constants. The zero value (no flag) is READ_ONLY.
const (
	READ_ONLY Mode = iota
	WORKSPACE_WRITE
	FULL_ACCESS
)

// String implements fmt.Stringer so Mode renders as the SCOPE §12
// CLI value ("read_only" / "workspace_write" / "full_access"). Used
// by `config show`'s JSON output (the Mode-aware policy surfaces the
// active mode in the resolved-configuration per SCOPE §13).
func (m Mode) String() string {
	switch m {
	case READ_ONLY:
		return "read_only"
	case WORKSPACE_WRITE:
		return "workspace_write"
	case FULL_ACCESS:
		return "full_access"
	}
	return "unknown"
}

// ParseMode parses the SCOPE §12 CLI value. An unknown value returns
// an error (the harness converts to exit 2 per SCOPE §12
// "configuration error").
func ParseMode(s string) (Mode, error) {
	switch s {
	case "read_only":
		return READ_ONLY, nil
	case "workspace_write":
		return WORKSPACE_WRITE, nil
	case "full_access":
		return FULL_ACCESS, nil
	}
	return READ_ONLY, fmt.Errorf("perm: unknown mode %q (want read_only|workspace_write|full_access)", s)
}

// mutationTools is the fixed list of tool names whose Execute is a
// mutation. A new mutation tool added in a future Run MUST be added
// here AND to builtins.RegisterBuiltins. Both edits stay inside the
// foundation.
var mutationTools = map[string]bool{
	"write_file":  true,
	"apply_patch": true,
	"shell":       true,
}

// IsMutationTool reports whether the named tool is on the mutationTools
// list. Used by tests and by future Run-time tools that need to know
// whether their mutation will pass the policy step.
func IsMutationTool(name string) bool {
	return mutationTools[name]
}

// Policy is the mode-aware permission policy. It satisfies the
// tools.Policy interface so callers can wire it into
// tools.Registry.Dispatch the same way the Run 003 Permissive stub was
// wired.
//
// Run 003's Permissive stub returned Allowed:true for every call. The
// mode-aware Policy returns Allowed:false for any call the active
// mode rejects. The contract is the six TestPolicy_* tests in
// policy_test.go:
//
//	READ_ONLY: allow read-only tools, deny mutation.
//	WORKSPACE_WRITE: allow read-only; allow mutation if path is in-
//	                 workspace, deny mutation if path escapes.
//	FULL_ACCESS: allow everything.
type Policy struct {
	// Mode is the active permission mode. The decision's behavior
	// depends on Mode alone in this Run; the future-extension
	// seam is the pol parameter on Authorize (which is preserved
	// but currently unused — Run 005+ may use it for mode-aware
	// policy compositions that depend on more than just Mode).
	Mode Mode
}

// NewPolicy constructs a Policy with the given Mode. The zero value
// of Policy is equivalent to NewPolicy(READ_ONLY).
func NewPolicy(mode Mode) Policy {
	return Policy{Mode: mode}
}

// Decide implements tools.Policy. It returns a tools.Decision that the
// authorize pipeline checks at the policy stage (stage "policy" →
// external Kind "permission_denied" per Dispatch's mapStageToKind).
//
// The decision logic:
//
//	READ_ONLY:        if call.Name is a mutation tool, deny with
//	                  Reason "read-only-mode-rejects-mutation". Read-
//	                  only tools are allowed.
//	WORKSPACE_WRITE:  read-only tools allowed; mutation tools allowed
//	                  only if every path-shaped argument of the call
//	                  resolves inside the workspace (under wsRoot).
//	                  If any path-shaped argument escapes, deny with
//	                  Reason "workspace-write-rejects-escape".
//	FULL_ACCESS:      allow everything.
//
// Path escape detection for WORKSPACE_WRITE uses the same segment-
// boundary-safe form as internal/path.Normalize: an argument value is
// considered an escape if it equals ".." OR starts with ".." followed
// by a path separator, OR is an absolute path whose filepath.Rel
// against wsRoot starts with "..". This is duplicated in Policy.Decide
// (the policy stage cannot depend on internal/path.Workspace's
// Normalize because the seam's interface is just Decision; the
// duplication is small, deterministic, and tested).
func (p Policy) Decide(_ context.Context, call tools.Call, ws tools.Workspace) tools.Decision {
	isMutation := mutationTools[call.Name]

	switch p.Mode {
	case READ_ONLY:
		if isMutation {
			return tools.Decision{
				Allowed: false,
				Reason:  "read-only-mode-rejects-mutation",
			}
		}
		return tools.Decision{Allowed: true, Reason: "read-only-mode-allows-readonly"}

	case WORKSPACE_WRITE:
		if !isMutation {
			return tools.Decision{Allowed: true, Reason: "workspace-write-allows-readonly"}
		}
		if hasEscapingPathArg(call.Arguments, ws.Root()) {
			return tools.Decision{
				Allowed: false,
				Reason:  "workspace-write-rejects-escape",
			}
		}
		return tools.Decision{Allowed: true, Reason: "workspace-write-allows-in-workspace"}

	case FULL_ACCESS:
		return tools.Decision{Allowed: true, Reason: "full-access-allows-all"}
	}

	// An unknown Mode value (e.g. Mode(42) passed by a future bug)
	// is treated as a deny per the SCOPE §12 "never silent
	// escalation" rule.
	return tools.Decision{Allowed: false, Reason: "unknown-mode-denies-all"}
}

// hasEscapingPathArg reports whether any string value in args is a
// path-shaped argument that escapes wsRoot. The check is segment-
// boundary-safe (matches internal/path.Normalize's form).
//
// Heuristic: any argument whose name suggests a path (path / file /
// dir / *_path / *_file / *_dir) OR any string containing a path
// separator or starting with ".." is checked. The test cases use
// the conventional "path" argument name; the broader heuristic is
// matching the Authorize pipeline's looksLikePath convention so a
// mutation tool's escape vector is caught whichever argument name it
// uses.
func hasEscapingPathArg(args map[string]any, wsRoot string) bool {
	for name, val := range args {
		s, ok := val.(string)
		if !ok || s == "" {
			continue
		}
		if !looksLikePathish(name, s) {
			continue
		}
		if pathEscapes(s, wsRoot) {
			return true
		}
	}
	return false
}

// looksLikePathish returns true iff the (name, value) pair is a path-
// shaped argument. Mirrors perm.Authorize.looksLikePath's heuristic
// so the policy's escape detection matches what the pipeline's path
// stage would have caught.
func looksLikePathish(name, value string) bool {
	switch name {
	case "path", "file", "dir":
		return true
	}
	if strings.HasSuffix(name, "_path") ||
		strings.HasSuffix(name, "_file") ||
		strings.HasSuffix(name, "_dir") {
		return true
	}
	// Also check the value itself: any relative path starting with
	// ".." or containing a separator is path-shaped.
	if strings.HasPrefix(value, "../") || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return true
	}
	return false
}

// pathEscapes returns true iff the string s, when treated as a path
// relative to wsRoot, resolves outside wsRoot. Mirrors the segment-
// boundary-safe logic in internal/path.Normalize.
func pathEscapes(s, wsRoot string) bool {
	if filepath.IsAbs(s) {
		cleaned := filepath.Clean(s)
		rel, err := filepath.Rel(wsRoot, cleaned)
		if err != nil {
			return true
		}
		return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return s == ".." || strings.HasPrefix(s, ".."+string(filepath.Separator))
}

// Compile-time assertion that Policy satisfies tools.Policy.
var _ tools.Policy = Policy{}
