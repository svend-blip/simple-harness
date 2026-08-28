// Command simple-harness is the CLI entry point for the Simple Harness
// project. Run 002 / handoff 005 ships the smallest direct-understandable
// CLI skeleton: --version prints the runtime version and exits 0, --help
// prints a brief usage summary and exits 0, every other flag is rejected
// with a non-zero exit code (SCOPE §28 code 1, generic failure).
//
// No production behaviour beyond the CLI skeleton: no config loader
// (handoff 006), no model client (handoff 007), no loop, no headless mode,
// no tools. This file makes the public entry-point contract verifiable;
// later handoffs populate the interior.
//
// Architectural boundary: this is a Simple Harness component. It does not
// import orchestration, harness selection, GPU/VRAM allocation, model
// lifecycle, or Model Allocator policy.
package main

import (
	"flag"
	"fmt"
	"os"
)

// Version is the runtime version literal. It is a package-level const so
// the test in main_test.go can pin the exact bytes --version prints
// without shelling out or reading the binary itself. The format is a
// single line, project-name first, so an external parser does not need to
// interpret it to extract the version.
const Version = "simple-harness 0.1.0-dev (Run 002, handoff 005)"

// usage is the brief usage summary printed by --help and by the
// no-argument default. Kept short on purpose; later handoffs expand it
// as the public CLI surface grows.
const usage = `Usage: simple-harness [flags]

Simple Harness — a small, deterministic, terminal-first execution kernel
for one AI role. Run 002 ships the CLI skeleton only; production verbs
arrive in later handoffs.

Flags:
  --version   print the runtime version and exit 0
  --help      print this usage summary and exit 0

Any other flag is rejected with a non-zero exit code (SCOPE §28 code 1,
generic failure). See docs/ARCHITECTURE.md §"Distribution shape" for the
distribution and exit-code contract.
`

// run is the testable inner entry point. It returns the process exit
// code rather than calling os.Exit directly so the unit tests in
// main_test.go can drive the same code path the CLI uses without forking
// the binary.
func run(args []string) int {
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

func main() {
	os.Exit(run(os.Args[1:]))
}
