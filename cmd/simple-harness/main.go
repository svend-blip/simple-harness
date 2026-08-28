// Command simple-harness is the CLI entry point for the Simple Harness
// project. Run 002 / handoff 005 ships the smallest direct-understandable
// CLI skeleton: --version prints the runtime version and exits 0, --help
// prints a brief usage summary and exits 0, every other flag is rejected
// with a non-zero exit code (SCOPE §28 code 1, generic failure).
//
// Handoff 006 wires the `config show` subcommand which loads the
// resolved configuration and prints it to stdout with secrets redacted
// per SCOPE §30. The dispatch is a positional check on args[0] ==
// "config"; it is additive on top of the existing flag parsing and
// does not change --version/--help behaviour.
//
// No model client, no loop, no interactive mode, no tools. Handoff
// 007 lands the model client; handoff 008 lands the loop and
// interactive mode.
//
// Architectural boundary: this is a Simple Harness component. It does not
// import orchestration, harness selection, GPU/VRAM allocation, model
// lifecycle, or Model Allocator policy.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/svend-blip/simple-harness/internal/config"
)

// Version is the runtime version literal. It is a package-level const so
// the test in main_test.go can pin the exact bytes --version prints
// without shelling out or reading the binary itself. The format is a
// single line, project-name first, so an external parser does not need to
// interpret it to extract the version.
const Version = "simple-harness 0.1.0-dev (Run 002, handoff 006)"

// usage is the brief usage summary printed by --help and by the
// no-argument default. Kept short on purpose; later handoffs expand it
// as the public CLI surface grows.
const usage = `Usage: simple-harness [flags] <subcommand>

Simple Harness — a small, deterministic, terminal-first execution kernel
for one AI role.

Flags:
  --version   print the runtime version and exit 0
  --help      print this usage summary and exit 0

Subcommands:
  config show    print the resolved configuration (secrets redacted)

Any other flag or subcommand is rejected with a non-zero exit code
(SCOPE §28 code 1, generic failure). See docs/ARCHITECTURE.md
§"Distribution shape" for the distribution and exit-code contract.
`

// run is the testable inner entry point. It returns the process exit
// code rather than calling os.Exit directly so the unit tests in
// main_test.go can drive the same code path the CLI uses without forking
// the binary.
func run(args []string) int {
	// Subcommand dispatch: "config show" is the only subcommand in V1.
	// A positional check (args[0] == "config") is enough — handoff 008
	// may refactor to a real subcommand parser if more verbs land.
	if len(args) > 0 && args[0] == "config" {
		return runConfig(args[1:])
	}

	fs := flag.NewFlagSet("simple-harness", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "print the runtime version and exit 0")
	help := fs.Bool("help", false, "print the usage summary and exit 0")

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed the parse error to
		// fs.Output(); exit 1 (SCOPE §28, generic failure) for any
		// unparseable flag, including unknown flags. This is the
		// behaviour TG4 measures via the wrapper.
		return 1
	}

	switch {
	case *version:
		fmt.Println(Version)
		return 0
	case *help:
		fmt.Print(usage)
		return 0
	}

	// No recognised flag set. Print the usage summary to stderr (so it
	// is distinguishable from --help's stdout output) and exit 1.
	fmt.Fprint(os.Stderr, usage)
	return 1
}

// runConfig handles the "config" subcommand. In V1 the only verb is
// "show"; everything else is rejected with usage + exit 1.
func runConfig(args []string) int {
	if len(args) != 1 || args[0] != "show" {
		fmt.Fprintf(os.Stderr, "Usage: simple-harness config show\n")
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2 // SCOPE §28, configuration error
	}
	if err := cfg.Render(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "config render error: %v\n", err)
		return 1 // SCOPE §28, generic failure
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}
