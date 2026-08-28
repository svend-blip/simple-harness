// Package perm owns the permission-gate scaffold for Simple Harness:
// the policy stub (Permissive, satisfying tools.Policy), the Authorize
// pipeline runner, and the re-export seam from internal/path.
//
// Run 003 ships the seam and the stub; Run 004 replaces Permissive with
// a real mode-aware policy (READ_ONLY / WORKSPACE_WRITE / FULL_ACCESS)
// and adds the Mode parameter to Authorize. The pipeline order is fixed
// now and matches docs/ARCHITECTURE.md §"Permission boundary placement"
// §"Enforcement placement" verbatim:
//
//	schema validation  (tools.Validate on the call)
//	      ↓
//	path normalization (path.Workspace.Normalize on path-like args)
//	      ↓
//	permission policy  (Policy.Decide)
//	      ↓
//	execution          (caller's responsibility)
//
// Authorize returns the FIRST failure as a *tools.DecisionError. The
// function type matches tools.AuthorizeFunc so it can be passed directly
// to tools.Registry.Dispatch (main.go wires it at startup). The runner
// does NOT execute the tool — execution is the caller's responsibility
// (tools.Dispatch calls Execute only after Authorize returns nil).
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy. It imports only the Go
// standard library and the local internal/path / internal/tools
// packages.
package perm

import "github.com/svend-blip/simple-harness/internal/path"

// Workspace re-exports path.Workspace so future code that imports
// internal/perm does not need to also import internal/path. The seam
// exists today so Run 004's mode-aware path normalization can layer
// here without re-importing internal/path.
type Workspace = path.Workspace

// EscapeError re-exports path.EscapeError.
type EscapeError = path.EscapeError

// NewWorkspace re-exports path.New. The seam mirrors the type alias
// above: code that wants a Workspace via the perm package uses
// perm.NewWorkspace; the package internally is path.New.
//
// Reason constants are re-exported alongside.
const (
	ReasonAbsolutePath    = path.ReasonAbsolutePath
	ReasonParentTraversal = path.ReasonParentTraversal
	ReasonSymlinkEscape   = path.ReasonSymlinkEscape
)

// NewWorkspace constructs a Workspace rooted at the given path. See
// path.New for the semantics.
func NewWorkspace(root string) (Workspace, error) { return path.New(root) }