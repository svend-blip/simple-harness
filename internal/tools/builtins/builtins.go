package builtins

import "github.com/svend-blip/simple-harness/internal/tools"

// RegisterBuiltins registers the V1 read-only builtin tools against
// the given registry. The tools are registered in alphabetical-name
// order so the simple-harness tools listing is deterministic
// regardless of the order the source calls them — Registry.Names
// sorts on output, but the registration list itself is the human-
// readable documentation of "what's wired up".
//
// Handoff 014 registers read_file and list_directory. Handoff 015
// will add search_files and grep via the same Registrar pattern; the
// implementer of 015 extends this list and updates the package
// documentation.
//
// RegisterBuiltins is idempotent only in the sense that calling it
// twice on the same registry PANICS — Registry.Register panics on
// duplicate name. cmd/simple-harness/main.go calls it exactly once
// at startup, before run() is entered. The integration tests in
// builtins_test.go each construct a fresh registry and call
// RegisterBuiltins exactly once.
func RegisterBuiltins(r *tools.Registry) {
	// Order: alphabetical by tool name.
	r.Register(ListDirectory{})
	r.Register(ReadFile{})
}