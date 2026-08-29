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

import "github.com/svend-blip/simple-harness/internal/tools"

// RegisterBuiltins registers the V1 builtin tools against the given
// registry. The tools are registered in alphabetical-name order so
// the simple-harness tools listing is deterministic regardless of
// the order the source calls them — Registry.Names sorts on output,
// but the registration list itself is the human-readable
// documentation of "what's wired up".
//
// Run 003 (handoffs 014 + 015) registered all four V1 read-only
// tools: grep, list_directory, read_file, search_files. Run 004
// handoff 017 adds the first mutation tool: write_file (SCOPE §10
// "Support direct file writing where appropriate"). Run 004 handoff
// 018 adds the second mutation tool: apply_patch (SCOPE §10
// "deterministic patching for source modifications").
//
// RegisterBuiltins is idempotent only in the sense that calling it
// twice on the same registry PANICS — Registry.Register panics on
// duplicate name. cmd/simple-harness/main.go calls it exactly once
// at startup, before run() is entered. The integration tests in
// builtins_test.go each construct a fresh registry and call
// RegisterBuiltins exactly once.
func RegisterBuiltins(r *tools.Registry) {
	// Order: alphabetical by tool name.
	r.Register(ApplyPatch{})
	r.Register(Grep{})
	r.Register(ListDirectory{})
	r.Register(ReadFile{})
	r.Register(SearchFiles{})
	r.Register(WriteFile{})
}