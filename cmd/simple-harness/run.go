// run.go is the headless `simple-harness run` subcommand. Handoff 022
// (Run 006, deliverable 1 of 3) lands the FOUNDATION of this surface:
// the `run` subcommand dispatch, the flag parser, the help/version
// short-circuits, and the config-error exit-2 wiring for the four
// SCOPE §28 / GOAL §2 config-error conditions (missing prompt file,
// invalid --output, empty --base-url, empty --model) plus the
// missing-system-file case. Handoff 024 lands the run-execution
// path: a separate `Emitter` writing JSONL to os.Stdout (--output
// jsonl) or to a <workspace>/sessions/<session-id>/events.jsonl
// sidecar (--output terminal), the model client invocation through
// loop.RunOne, the SCOPE §28 exit-code mapping for *model.ModelError
// (ErrHTTP|ErrParse|ErrUpstream -> 3, ErrTimeout -> 6), and the
// stdout-purity guarantee that every line on stdout parses as JSON
// (TG3) for the --output jsonl surface.
//
// This file wires the full run-mode execution path:
// (a) parses + validates the run-mode flags with SCOPE §28-correct
// exit codes and stderr messages (handoff 022's foundation),
// (b) reads the prompt file's contents into memory and constructs
// the model client + emitter + loop (handoff 024's extension),
// (c) calls loop.RunOne and maps the returned error to the SCOPE §28
// exit code, emitting a terminal completed(exit_code) event on the
// failure path (the loop's success path emits Completed(0) from
// inside RunOne), and
// (d) lands the four run-mode testgoals end-to-end (TG1 regression,
// full TG2 unreachable endpoint + JSONL events + exit 3, full TG3
// every-JSONL-line-parses, full TG4 `simple-harness run --help` +
// `./scripts/test.sh`).
//
// runUsage is the help text printed by `simple-harness run --help`.
// The full text is a const so the test can pin the substring
// (TestRun_Help asserts "model_request" is mentioned, pinning the
// GOAL §2 minimum event set in the help text).

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/svend-blip/simple-harness/internal/config"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/loop"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/session"
	"github.com/svend-blip/simple-harness/internal/skill"
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
  --state-dir <dir>       state directory for session persistence
                          (defaults to ~/.simple-harness/sessions)
  --skills-dir <dir>      skills directory override (defaults to
                          ~/.simple-harness/skills and <workspace>/.simple-harness/skills;
                          the test-only deterministic handle per GOAL §2)
  --skill <name>          skill name to load; SKILL.md is read from the
                          resolved skills dir; an unknown name is a
                          configuration error (exit 2). Skill content
                          is loaded but NOT YET injected into the model
                          context (composition lands on a future handoff).
                          SCOPE §15, SCOPE §16.
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
  0  clean exit (run-mode validation passed; also returned on a
     successful model turn)
  1  generic failure (flag parse error, runtime I/O error, etc.)
  2  configuration error (missing prompt file, invalid --output,
     empty --base-url, empty --model, missing --system-file)
  3  model/API failure (unreachable endpoint, HTTP 5xx, malformed
     SSE; the JSONL stream carries a 'status: FAILED' event and a
     'completed(exit_code: 3)' event before the process exits)
  6  interrupted (SIGINT/SIGTERM on the harness process; the
     JSONL stream carries an interrupted event with session_id
     before the process exits; this is distinct from model-
     timeout exit 6 which carries completed(exit_code: 6))

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
// emitted. Handoff 024 wraps this validation with the emitter
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
	stateDir := fs.String("state-dir", "", "state directory for session persistence (defaults to ~/.simple-harness/sessions)")
	skillsDir := fs.String("skills-dir", "", "skills directory override (defaults to ~/.simple-harness/skills + <workspace>/.simple-harness/skills; this is the test-only deterministic handle per GOAL §2)")
	skillName := fs.String("skill", "", "skill name to load; SKILL.md is read from the resolved skills dir; an unknown name is a configuration error (exit 2). Composition into the model context lands on handoff 033. SCOPE §15.")
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
	// handoff 024 can use the value as-is without re-running
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
	// human-facing surface); --output jsonl is the TG3 stdout
	// purity surface (every line on stdout is a JSON object).
	switch *output {
	case "terminal", "jsonl":
		// accepted
	default:
		fmt.Fprintf(os.Stderr, "config error: --output must be 'terminal' or 'jsonl', got %q\n", *output)
		return 2
	}

	// --state-dir: optional. Default to ~/.simple-harness/sessions
	// when empty (the SCOPE §17 contract). Run 008 (handoff 030)
	// introduces this flag — every run-mode execution now writes
	// <state-dir>/<session-id>/{session.json, events.jsonl,
	// messages.jsonl}.
	if *stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine home directory: %v\n", err)
			return 2
		}
		*stateDir = filepath.Join(home, ".simple-harness", "sessions")
	}

	// --system-file: optional. If non-empty, the file must
	// exist and be readable (same validation as --prompt-file).
	// The system prompt is not yet used by the loop in this
	// handoff; the validation is the only thing landing now so
	// a handoff 024 caller cannot trip over a missing file
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

	// --skill + --skills-dir: optional. --skills-dir is the
	// test-only override (when non-empty, REPLACES BOTH search
	// roots); --skill is the skill name to load. An unknown
	// --skill name is a configuration error (SCOPE §15: "V1
	// skills should primarily inject reusable instructions/context")
	// and exits 2 per GOAL §2 and TG1. Composition into the model
	// context is deferred to a future handoff; this handoff only
	// validates the skill name resolves and the file is readable.
	// The validation runs BEFORE the --prompt-file check so a
	// missing prompt file doesn't mask a missing skill (and so the
	// error message is about the skill, not the prompt).
	if *skillName != "" {
		var resolvedSkillsDir string
		var resolvedHome string
		if *skillsDir != "" {
			resolvedSkillsDir = *skillsDir
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "config error: cannot determine home directory: %v\n", err)
				return 2
			}
			resolvedHome = home
		}
		if _, err := skill.Load(*skillName, skill.LoadOptions{
			SkillsDir:    resolvedSkillsDir,
			WorkspaceDir: *workspace,
			HomeDir:      resolvedHome,
		}); err != nil {
			if errors.Is(err, skill.ErrSkillNotFound) {
				fmt.Fprintf(os.Stderr, "config error: unknown skill %q\n", *skillName)
				return 2
			}
			fmt.Fprintf(os.Stderr, "config error: load skill %q: %v\n", *skillName, err)
			return 2
		}
	}

	// --prompt-file: required, non-empty. The "-" value is
	// accepted as a parseable sentinel (the stdin-reader is a
	// future handoff; for handoff 024 the validation-only path
	// treats it as a no-op-parseable value and returns 0 with
	// no events). Any other non-empty value must point at an
	// existing, readable file; otherwise exit 2.
	if *promptFile == "" {
		fmt.Fprintf(os.Stderr, "config error: --prompt-file is required\n")
		return 2
	}
	if *promptFile == "-" {
		// Stdin prompt handling is a future handoff. For
		// handoff 024 the flag-parsed config is valid; we
		// return 0 without reading from stdin so the test
		// surface (TestRun_StdinPolicy_NonDashSentinel_Returns0)
		// is not bound to a specific stdin policy. The
		// decomposition choice is also recorded in the
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

	// Handoff 024's run-execution path: the flag-parsed config
	// is valid, the prompt file is readable, the system file
	// (if given) is readable, the workspace is resolved, and
	// the output mode is one of the two allowed values. Read
	// the prompt file's full contents into memory and hand off
	// to runModeExecute for the model-client wiring + loop
	// invocation + error mapping.
	//
	// Errors that escape the existing validateReadableFile
	// check (e.g. a TOCTOU race where the file disappears
	// between validation and read) fall through to exit 1
	// with a SCOPE §28 generic-failure stderr message — the
	// config WAS valid at parse time, so this is not a config
	// error.
	prompt, err := os.ReadFile(*promptFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: read prompt-file: %v\n", err)
		return 1
	}
	return runModeExecute(
		string(prompt),
		*baseURL,
		*model,
		*workspace,
		*output,
		*stateDir,
	)
}

// runModeExecute is the run-execution inner body. It is extracted
// from runRun for testability — the four new TestRun_* tests in
// main_test.go (TG2 + TG3 + stdin-policy pin + version-advance
// pin) drive it indirectly through runRun, but the function is
// its own unit so a future refactor that wants to exercise the
// run-mode executor without the flag.NewFlagSet ceremony can call
// it directly. It is NOT part of the public CLI surface; it is
// internal (lowercase, same package).
//
// The function:
//  1. Loads the resolved config (config.Load() — existing API).
//  2. Generates a session ID (newSessionID() — existing helper in
//     main.go).
//  3. Decides the emitter writer (stdout for jsonl, sidecar for
//     terminal).
//  4. Decides the loop's r.out writer (io.Discard for jsonl,
//     stdout for terminal — the TG3 stdout-purity contract).
//  5. Constructs loop.Config + model.NewClient + loop.New.
//  6. Calls r.RunOne(context.Background(), prompt).
//  7. Maps the returned error to a SCOPE §28 exit code and emits
//     Completed(exit_code) if the error path didn't already emit
//     Completed (the loop's success path emits Completed(0) from
//     inside RunOne).
//
// The function returns the SCOPE §28 exit code.
func runModeExecute(prompt, baseURL, modelName, workspace, outputMode, stateDir string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}

	sessionID, err := newSessionID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: cannot generate session id: %v\n", err)
		return 1
	}

	var em *event.Emitter
	var loopOut io.Writer
	var sidecar *os.File
	switch outputMode {
	case "jsonl":
		// Run 008 (handoff 030): events.jsonl is the canonical
		// session record (SCOPE §17); it ALWAYS lands at
		// <state-dir>/<session-id>/events.jsonl. Under
		// --output jsonl, the same events ALSO stream to stdout
		// (the TG3 stdout-purity contract preserved from Run 006).
		// The emitter writes to both via io.MultiWriter.
		sidecarDir := filepath.Join(stateDir, sessionID)
		if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "internal error: cannot create %s: %v\n", sidecarDir, err)
			return 1
		}
		sidecarPath := filepath.Join(sidecarDir, "events.jsonl")
		sf, err := os.Create(sidecarPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "internal error: cannot create %s: %v\n", sidecarPath, err)
			return 1
		}
		sidecar = sf
		em = event.NewEmitter(io.MultiWriter(sf, os.Stdout), sessionID)
		loopOut = io.Discard
	case "terminal":
		sidecarDir := filepath.Join(stateDir, sessionID)
		if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "internal error: cannot create %s: %v\n", sidecarDir, err)
			return 1
		}
		sidecarPath := filepath.Join(sidecarDir, "events.jsonl")
		sf, err := os.Create(sidecarPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "internal error: cannot create %s: %v\n", sidecarPath, err)
			return 1
		}
		sidecar = sf
		em = event.NewEmitter(sidecar, sessionID)
		loopOut = os.Stdout
	default:
		// Pre-flag-parse validation already rejects this;
		// defensive return for any path that bypassed the flag
		// parser.
		fmt.Fprintf(os.Stderr, "config error: --output must be 'terminal' or 'jsonl', got %q\n", outputMode)
		return 2
	}

	// SCOPE §26 cancellation plumbing: SIGINT/SIGTERM cancel the
	// in-flight context. The model.Client already maps ctx
	// cancellation to *model.ModelError{ErrTimeout} (internal/model
	// client.go lines 224, 256, 326); the loop's RunOne propagates
	// that error to the cmd. The `interrupted` flag distinguishes
	// signal-triggered cancellation (the SCOPE §26 path; this
	// handoff's NEW behavior — emit `interrupted` event + exit 6)
	// from model-timeout cancellation (the SCOPE §28 path; the
	// existing handoff-024 mapping — emit `completed(6)` + exit 6).
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	interrupted := false
	go func() {
		<-sigCh
		interrupted = true
		cancel()
	}()

	normalizedBase := loop.NormalizeBaseURL(baseURL)
	permissionStr := modeToLoopString(activePermissionMode)

	// Run 008 (handoff 030): open a session.Writer to persist
	// session.json (identity + config snapshot + final status/exit)
	// and messages.jsonl (per-message log) under
	// <state-dir>/<session-id>/. The defer below writes session.json
	// with the final status/exit on every return path of this
	// function.
	sessWriter, err := session.NewWriter(stateDir, sessionID, session.Config{
		BaseURL:    normalizedBase,
		Model:      modelName,
		Workspace:  workspace,
		Permission: permissionStr,
		OutputMode: outputMode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: cannot open session writer: %v\n", err)
		return 1
	}
	defer func() {
		var finalStatus session.Status
		var finalCode int
		if interrupted {
			finalStatus = session.StatusInterrupted
			finalCode = 6
		} else if err != nil {
			var me *model.ModelError
			if errors.As(err, &me) {
				switch me.Kind {
				case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
					finalStatus = session.StatusFailed
					finalCode = 3
				case model.ErrTimeout:
					finalStatus = session.StatusFailed
					finalCode = 6
				default:
					finalStatus = session.StatusFailed
					finalCode = 1
				}
			} else {
				finalStatus = session.StatusFailed
				finalCode = 1
			}
		} else {
			finalStatus = session.StatusCompleted
			finalCode = 0
		}
		_ = sessWriter.Write(finalStatus, finalCode)
	}()

	modelOpts := model.Options{
		BaseURL:         normalizedBase,
		Model:           modelName,
		APIKey:          cfg.Model.APIKey,
		Temperature:     cfg.Model.Temperature,
		MaxOutputTokens: cfg.Model.MaxOutputTokens,
		RequestTimeout:  cfg.Model.RequestTimeout,
	}
	client := model.NewClient(modelOpts)
	r := loop.New(loop.Config{
		Model:      modelOpts,
		Workspace:  workspace,
		Permission: permissionStr,
	}, client, em, loopOut)

	// Run 008 (handoff 030): record the user message in
	// messages.jsonl before the model call.
	_ = sessWriter.AppendMessage("user", prompt)

	response, err := r.RunOne(ctx, prompt)

	// If the signal fired, the SCOPE §26 sequence is: emit
	// `interrupted` event -> flush sidecar (if terminal) -> exit 6.
	// The success path (err == nil) and the existing handoff-024
	// error mapping (errors.As -> SCOPE §28 exit codes) are
	// unchanged; only the NEW `interrupted` branch is added.
	if interrupted {
		signal.Stop(sigCh)
		_ = em.Interrupted(sessionID)
		if sidecar != nil {
			_ = sidecar.Sync()
			_ = sidecar.Close()
		}
		return 6
	}

	// Stop the signal handler — the in-flight operation has
	// completed (success or error); no further signals matter.
	signal.Stop(sigCh)

	if err == nil {
		// Success path: RunOne already emitted Completed(0) from
		// inside (loop.go lines 161-166). Close the sidecar if
		// terminal mode and return 0 — do NOT emit a second
		// Completed.
		if sidecar != nil {
			_ = sidecar.Sync()
			_ = sidecar.Close()
		}
		// Run 008 (handoff 030): record the assistant response in
		// messages.jsonl after a successful turn.
		_ = sessWriter.AppendMessage("assistant", response)
		return 0
	}

	// Error path: RunOne emitted a Status(FAILED|INTERRUPTED) but
	// NOT a Completed event (loop.go lines 142-159). Emit
	// Completed(exit_code) here so the wire has the terminal event
	// the SCOPE §21 protocol requires. NOTE: close the sidecar
	// AFTER the terminal event so the events.jsonl in state-dir
	// captures the terminal Completed event (the MultiWriter in
	// jsonl mode writes to both sidecar + stdout).
	var me *model.ModelError
	if errors.As(err, &me) {
		switch me.Kind {
		case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
			_ = em.Completed(3)
			if sidecar != nil {
				_ = sidecar.Sync()
				_ = sidecar.Close()
			}
			return 3
		case model.ErrTimeout:
			_ = em.Completed(6)
			if sidecar != nil {
				_ = sidecar.Sync()
				_ = sidecar.Close()
			}
			return 6
		}
	}
	_ = em.Completed(1)
	if sidecar != nil {
		_ = sidecar.Sync()
		_ = sidecar.Close()
	}
	return 1
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
