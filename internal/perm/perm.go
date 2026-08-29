package perm

import (
	"context"
	"errors"
	"strings"

	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// Authorize runs the SCOPE §13 pipeline and returns nil on success or a
// *tools.DecisionError on the first failure. The order is FIXED and
// binding per docs/ARCHITECTURE.md §"Permission boundary placement":
//
//	schema validation → path normalization → permission policy
//
// (execution is the caller's responsibility — tools.Dispatch runs
// Execute only after Authorize returns nil.)
//
// The signature matches tools.AuthorizeFunc so callers can pass
// perm.Authorize directly to tools.Registry.Dispatch:
//
//	registry.Dispatch(ctx, call, ws, pol, perm.Authorize)
//
// Run 004 replaces the Run-003 Permissive policy stub with the mode-
// aware Policy type. The Mode is carried by the Policy parameter
// (constructed via perm.NewPolicy(mode) or perm.Policy{Mode: mode});
// the same Authorize signature is preserved so tools.AuthorizeFunc
// stays compatible with perm.Authorize without a tools/types.go
// modification. The mode-aware decision happens inside pol.Decide
// (the policy step). Pipeline order is unchanged.
//
// The `pol` parameter remains as the future-extension-point seam per
// handoff 016: Run 005+ may grow Policy with stateful or context-
// dependent fields beyond Mode; today Policy is `{Mode Mode}` and
// the Mode field alone drives the decision.
func Authorize(ctx context.Context, call tools.Call, schema tools.Schema, ws path.Workspace, pol tools.Policy) *tools.DecisionError {
	// 1. Schema check — delegate to tools.Validate. The validator
	// returns a *ToolError; we map Kind="schema_violation" to the
	// DecisionError's Reason by inspecting the ToolError's message
	// (the validator's three failure modes produce messages starting
	// with "missing required field", "unknown field", and "field ... has
	// wrong type", which we map to one-word reasons).
	if err := tools.Validate(call, schema); err != nil {
		return &tools.DecisionError{
			Stage:  "schema",
			Reason: schemaReason(err),
			Call:   call,
		}
	}

	// 2. Path normalization — walk the call's arguments, normalize any
	// string that looks like a path, and reject if any normalization
	// fails. Run 004 adds mode-aware normalization; today every path-
	// like argument is normalized against the workspace root.
	for argName, argVal := range call.Arguments {
		s, ok := argVal.(string)
		if !ok {
			continue
		}
		if !looksLikePath(argName, s) {
			continue
		}
		if _, err := ws.Normalize(s); err != nil {
			var ee *path.EscapeError
			if errors.As(err, &ee) {
				return &tools.DecisionError{Stage: "path", Reason: ee.Reason, Call: call}
			}
			return &tools.DecisionError{Stage: "path", Reason: "normalize_failed", Call: call}
		}
	}

	// 3. Policy decision. The stub Permissive always returns Allowed;
	// Run 004's real policy will gate on Mode.
	d := pol.Decide(ctx, call, ws)
	if !d.Allowed {
		return &tools.DecisionError{Stage: "policy", Reason: "permission_denied", Call: call}
	}

	return nil
}

// schemaReason maps a *ToolError produced by tools.Validate to the
// one-word Reason stored on DecisionError. The validator's three
// failure messages are checked by prefix because the validator's
// messages are part of its public contract (the schema tests assert
// the exact strings).
func schemaReason(err *tools.ToolError) string {
	switch {
	case strings.HasPrefix(err.Message, "missing required field"):
		return "missing_field"
	case strings.HasPrefix(err.Message, "unknown field"):
		return "additional_property"
	case strings.HasPrefix(err.Message, "field ") && strings.Contains(err.Message, "has wrong type"):
		return "wrong_type"
	}
	return "schema_violation"
}

// looksLikePath returns true iff argName / argValue look like a path
// argument. The heuristic: argument names ending in "_path", "_file",
// "_dir", or named "path" / "file" / "dir" are path-like. The heuristic
// is the simpler and Run-003-correct choice; Run 014 and Run 015
// declare their tools' path arguments with the conventional names
// ("path", "file", "dir") so the heuristic catches them. A more
// precise schema-driven approach (the schema's PropertyType could grow
// a "path" variant) is a Run-004+ concern.
func looksLikePath(argName, argValue string) bool {
	// Reject empty strings — they are not paths.
	if argValue == "" {
		return false
	}
	switch argName {
	case "path", "file", "dir":
		return true
	}
	if strings.HasSuffix(argName, "_path") ||
		strings.HasSuffix(argName, "_file") ||
		strings.HasSuffix(argName, "_dir") {
		return true
	}
	return false
}