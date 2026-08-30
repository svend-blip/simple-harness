// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Handoff 014 registered the first two of the four
// V1 read-only tools (read_file and list_directory); handoff 015
// registers the remaining two (search_files and grep); handoff 017
// adds the first mutation tool (write_file) via the same
// RegisterBuiltins registrar; handoff 018 adds the second mutation
// tool (apply_patch); handoff 020 adds the third mutation tool
// (shell) with process-group ownership per SCOPE §27; handoff 021
// extends shell with the SCOPE §11 advanced-behavior contract:
// timeout (timeout_ms argument, SIGTERM to process group),
// cancellation (ctx.Done), bounded SIGKILL escalation after a 2s
// grace if the child ignores SIGTERM, per-stream output-size cap
// (max_output_bytes argument) with explicit in-stream truncation
// marker, and the SCOPE §27 orphan-survival proof. Run 021 /
// handoff 068 adds the two SCOPE §45 builtin tools — list_skills
// (enumerates skills under §15's locations with workspace-wins
// collision order) and load_skill (loads a named skill's
// instruction material into the next model request's context) —
// both READ-ONLY per reviewer duty §1; the model-invoked surface
// for skill discovery + composition per GOAL §2 bound decisions.
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
// "deterministic patching for source modifications"). Run 005
// handoff 020 adds the third mutation tool: shell (SCOPE §§11, 27
// "controlled execution in its own process group"). Run 021
// handoff 068 adds the two SCOPE §45 model-invoked skill tools:
// list_skills + load_skill.
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
	r.Register(ListSkills{})   // Run 021 / handoff 068 — SCOPE §45
	r.Register(LoadSkill{})    // Run 021 / handoff 068 — SCOPE §45
	r.Register(ReadFile{})
	r.Register(SearchFiles{})
	r.Register(Shell{})
	r.Register(WriteFile{})
}