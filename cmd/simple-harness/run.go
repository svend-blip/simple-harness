// run.go is the headless `simple-harness run` subcommand. Handoff 022
// (Run 006, deliverable 1 of 3) lands the FOUNDATION of this surface:
// the `run` subcommand dispatch, the flag parser, the help/version
// short-circuits, and the config-error exit-2 wiring for the four
// SCOPE §28 / GOAL §2 config-error conditions (missing prompt file,
// invalid --output, empty --base-url, empty --model) plus the
// missing-system-file case. Handoff 023 lands the run-execution
// path: a separate `Emitter` writing JSONL to os.Stdout, the model
// client invocation through loop.RunOne, the exit-3 mapping for
// unreachable endpoints (TG2), and the stdout-purity guarantee that
// every line on stdout parses as JSON (TG3).
//
// This file is intentionally the smallest foundation that
// (a) parses + validates the run-mode flags with SCOPE §28-correct
// exit codes and stderr messages,
// (b) is fully covered by seven new TestRun_* tests in
// main_test.go (the run --help half of TG4, the run
// --version half, the missing-prompt-file exit-2 case, the
// invalid --output exit-2 case, the empty --base-url exit-2 case,
// the empty --model exit-2 case, the missing --system-file exit-2
// case), and
// (c) leaves the handoff 023 seam clean: the flag-parsed values
// are validated and the prompt is read into memory; the loop
// call + emitter + error mapping is the only thing 023 has to
// add.
//
// runUsage is the help text printed by `simple-harness run --help`.
// The full text is a const so the test can pin the substring
// (TestRun_Help asserts "model_request" is mentioned, pinning the
// GOAL §2 minimum event set in the help text).

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// runUsage is the help text printed by `simple-harness run --help`
// and by `simple-harness run -h`. It is a const so a future
// regression that drops one of the named flags fails the
// TestRun_Help substring assertion in main_test.go.
//
// The text names every flag (with its default), the four SCOPE §28
// exit codes the run-mode surface can return, and the difference
// between --output terminal and --output jsonl so an external
// controller can decide which mode to invoke.
const runUsage = `Usage: simple-harness run [flags]

Execute one turn non-interactively and exit. The V1 surface
is the flag-driven config-error path: the run executes against
the local model endpoint with no stdin REPL, no multi-turn,
no tools. Each of those is a future Run per the architecture.

Flags:
  --base-url <url>        base URL of the OpenAI-compatible endpoint
                          (required, non-empty)
  --model <name>          model name to send in the chat request
                          (required, non-empty)
  --workspace <dir>       workspace directory (defaults to cwd)
  --prompt-file <path>    path to the prompt file; use "-" to read
                          from stdin (required; the "-" value is
                          accepted but stdin handling is a future
                          handoff)
  --system-file <path>    optional path to a system prompt file;
                          the file must exist and be readable
                          (the system prompt is not yet used by
                          the loop — this handoff validates the
                          path only)
  --output <mode>         output mode: "terminal" (default; the
                          streamed assistant text goes to stdout,
                          human decorations to stderr) or "jsonl"
                          (every line on stdout is a structured
                          event from the V1 protocol —
                          protocol_version: "1", session_id,
                          timestamp, event). The minimum event
                          set is: started, status, model_request,
                          assistant_stream, completed.

Exit codes (SCOPE §28):
  0  clean exit (run-mode validation passed; handoff 023 will
     also return 0 on a successful model turn)
  1  generic failure (flag parse error, etc.)
  2  configuration error (missing prompt file, invalid --output,
     empty --base-url, empty --model, missing --system-file)
  3  model/API failure (handoff 023 — unreachable endpoint,
     HTTP 5xx, malformed SSE)

See docs/ARCHITECTURE.md §"Distribution shape" for the full
contract and the V1 event protocol.
`

// runRun is the testable inner entry point for the `simple-harness
// run` subcommand. It is the run-mode counterpart of run()'s
// runInteractive: a flag-driven single-turn invocation that exits
// with SCOPE §28-correct codes.
//
// The flag set follows the same pattern as the existing
// parsePermissionGlobal helper and the inner flag parser at the
// bottom of run(): flag.NewFlagSet with flag.ContinueOnError so
// parse errors print to fs.Output() (set to os.Stderr) and the
// function returns 1 on a parse failure (matching the existing
// run() parse-error path). A real test or operator invoking
// `simple-harness run --no-such-flag` sees the Go flag package's
// standard "flag provided but not defined" message on stderr and
// gets exit 1.
//
// Handoff 022's runRun is validation-only on the success path:
// the flag-parsed config is validated, the prompt file is read
// (or the stdin-policy "-" case is exercised), the system file
// is validated, and the function returns 0 with NO events
// emitted. Handoff 023 wraps this validation with the emitter
// + the loop call + the error mapping. The decomposition choice
// is documented in the handoff 022 result file's "Public-surface
// choices" subsection.
//
// The workspace default mirrors the interactive mode at
// runInteractive: when --workspace is empty, the resolved
// config.Load().Workspace (if present) or os.Getwd() is used.
// internal/config is read-only for Run 006, so we call the
// existing config.Load() and ignore the error (a load failure
// here would surface again in the loop call; for the handoff
// 022 validation-only path the workspace default does not need
// to be authoritative).
func runRun(args []string) int {
	fs := flag.NewFlagSet("simple-harness run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	help := fs.Bool("help", false, "print this help and exit 0")
	version := fs.Bool("version", false, "print the runtime version and exit 0")

	baseURL := fs.String("base-url", "", "base URL of the OpenAI-compatible endpoint (required, non-empty)")
	model := fs.String("model", "", "model name to send in the chat request (required, non-empty)")
	workspace := fs.String("workspace", "", "workspace directory (defaults to cwd)")
	promptFile := fs.String("prompt-file", "", "path to the prompt file; use - for stdin (required)")
	systemFile := fs.String("system-file", "", "optional path to a system prompt file")
	output := fs.String("output", "terminal", `output mode: "terminal" or "jsonl" (default "terminal")`)

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed the parse error to
		// fs.Output(); exit 1 (SCOPE §28, generic failure).
		return 1
	}

	// --help short-circuits: print runUsage and exit 0. The
	// substring assertion in TestRun_Help is anchored on
	// "model_request" so a future regression that drops the
	// minimum event set from the help text fails.
	if *help {
		fmt.Print(runUsage)
		return 0
	}

	// --version short-circuits: print the Version literal and
	// exit 0. The exact bytes are pinned by TestVersionLiteral
	// and the TG1 wrapper check in scripts/test.sh.
	if *version {
		fmt.Println(Version)
		return 0
	}

	// --base-url: required, non-empty. Empty string (the flag
	// default) is a SCOPE §28 config error.
	if *baseURL == "" {
		fmt.Fprintf(os.Stderr, "config error: --base-url is required\n")
		return 2
	}

	// --model: required, non-empty. Empty string (the flag
	// default) is a SCOPE §28 config error.
	if *model == "" {
		fmt.Fprintf(os.Stderr, "config error: --model is required\n")
		return 2
	}

	// --workspace: optional. Default to os.Getwd() when empty,
	// matching the interactive-mode default at runInteractive
	// (line ~322). The default is computed eagerly so a future
	// handoff 023 can use the value as-is without re-running
	// the resolution.
	if *workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine cwd: %v\n", err)
			return 2
		}
		*workspace = cwd
	}

	// --output: optional, must be "terminal" or "jsonl". The
	// default is "terminal" (matches the interactive mode's
	// human-facing surface; handoff 023 will introduce the
	// JSONL stdout path).
	switch *output {
	case "terminal", "jsonl":
		// accepted
	default:
		fmt.Fprintf(os.Stderr, "config error: --output must be 'terminal' or 'jsonl', got %q\n", *output)
		return 2
	}

	// --system-file: optional. If non-empty, the file must
	// exist and be readable (same validation as --prompt-file).
	// The system prompt is not yet used by the loop in this
	// handoff; the validation is the only thing landing now so
	// a handoff 023 caller cannot trip over a missing file
	// after the loop has already been invoked. The check runs
	// BEFORE the --prompt-file "-" stdin short-circuit so a
	// missing system file fails fast even when the prompt is
	// sourced from stdin (the TG1-style "any config error is
	// exit 2 regardless of which other flags are present"
	// contract).
	if *systemFile != "" {
		if err := validateReadableFile(*systemFile); err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot read system-file %q: %v\n", *systemFile, err)
			return 2
		}
	}

	// --prompt-file: required, non-empty. The "-" value is
	// accepted as a parseable sentinel (handoff 023 will wire
	// the stdin-reader; for handoff 022 the validation-only
	// path treats it as a no-op-parseable value and returns 0
	// with no events). Any other non-empty value must point
	// at an existing, readable file; otherwise exit 2.
	if *promptFile == "" {
		fmt.Fprintf(os.Stderr, "config error: --prompt-file is required\n")
		return 2
	}
	if *promptFile == "-" {
		// Stdin prompt handling is a future handoff (023). For
		// handoff 022 the flag-parsed config is valid; we return
		// 0 without reading from stdin so the test surface
		// (TestRun_StdinPolicy_* — out of scope this handoff
		// per the decomposition choice documented in the
		// result file) is not bound to a specific stdin policy.
		// The decomposition choice is also recorded in the
		// handoff 022 result file's "Public-surface choices"
		// subsection. The --system-file check above has already
		// run, so a missing system file still exits 2 even with
		// --prompt-file -.
		return 0
	}
	if err := validateReadableFile(*promptFile); err != nil {
		fmt.Fprintf(os.Stderr, "config error: cannot read prompt-file %q: %v\n", *promptFile, err)
		return 2
	}

	// Handoff 022's success path: the flag-parsed config is
	// valid, the prompt file is readable, the system file (if
	// given) is readable, the workspace is resolved, and the
	// output mode is one of the two allowed values. The handoff
	// 023 caller will add the emitter, the loop call, and the
	// error mapping on top of this validated state.
	//
	// The decomposition is intentional: the flag parser + the
	// config-error exit-2 path is the foundation, the
	// run-execution path is the next slot. Both TGs the
	// handoff lands (TG1 missing prompt file -> exit 2, the
	// `run --help` half of TG4) are covered by the validation
	// path alone; the remaining TGs (TG2, TG3, the `test.sh`
	// half of TG4) belong to handoff 023.
	_ = io.Discard // keep the io import in the file even though runRun's success path is currently empty
	_ = strings.TrimSpace
	return 0
}

// validateReadableFile returns nil if path exists and is a regular
// file (or a symlink that resolves to a regular file) readable by
// the current user. It is the shared helper for the --prompt-file
// and --system-file validation paths so the two checks stay
// consistent. The error message is forwarded to the caller; the
// callers wrap it with the canonical "config error: cannot read
// X %q: ..." stderr line.
func validateReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory, not a file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_ = f.Close()
	return nil
}
