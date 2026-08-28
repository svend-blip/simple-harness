package perm

import (
	"context"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Permissive is the Run 003 policy stub. Run 004 replaces it with a real
// mode-aware policy. The stub is honest about being a stub: every Decide
// returns Allowed: true with Reason: "policy-stub", so the audit trail
// (a structured tool_result event with Reason "policy-stub") is durable
// and the reviewer can verify the stub is the stub by reading this
// code directly, not by trusting any result-file paste.
//
// Policy / Decision are re-exported from the tools package via the
// normalize.go file's type aliases (Policy, Decision). The stub
// implements tools.Policy.
type Permissive struct{}

// NewPermissive returns the stub policy.
func NewPermissive() Permissive { return Permissive{} }

// Decide returns Allowed: true with Reason: "policy-stub" for every
// call. The stub never blocks; the seam is honest about being a seam.
//
// Do NOT add a "production" comment to this method — Run 004's diff
// will replace Permissive wholesale, and the diff's "removed Permissive"
// line is the load-bearing evidence the stub existed.
func (Permissive) Decide(_ context.Context, _ tools.Call, _ tools.Workspace) tools.Decision {
	return tools.Decision{Allowed: true, Reason: "policy-stub"}
}

// Compile-time assertion that Permissive satisfies tools.Policy.
var _ tools.Policy = Permissive{}