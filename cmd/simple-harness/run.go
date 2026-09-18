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
	"sync/atomic"
	"syscall"

	"github.com/svend-blip/simple-harness/internal/config"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/loop"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/session"
	"github.com/svend-blip/simple-harness/internal/skill"
	"github.com/svend-blip/simple-harness/internal/tools/builtins"
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

Execute one task non-interactively and exit: the prompt is sent
to the model, tool calls are dispatched against the workspace
under the active permission mode, and the loop continues until
the model answers without a tool call or --max-turns is reached.

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
                          configuration error (exit 2). The skill's
                          content is composed into the model context
                          at the SCOPE §14 step-3 position. SCOPE §15,
                          SCOPE §16.
  --prompt-file <path>    path to the prompt file; use "-" to read
                          the prompt from stdin (required)
  --system <text>         inline external system/governance prompt
                          (mutually exclusive with --system-file;
                          the resolved value is composed into the
                          model context at the SCOPE §14 step 2
                          position). SCOPE §14.
  --system-file <path>    optional path to a system prompt file;
                          the file must exist and be readable; the
                          file's content is composed into the model
                          context at the SCOPE §14 step 2 position.
                          Mutually exclusive with --system.
  --output <mode>         output mode: "terminal" (default; the
                          streamed assistant text goes to stdout,
                          human decorations to stderr) or "jsonl"
                          (every line on stdout is a structured
                          event from the V1 protocol —
                          protocol_version: "1", session_id,
                          timestamp, event). The minimum event
                          set is: started, status, model_request,
                          assistant_stream, completed.
  --limit <n>             configured context limit in tokens
                          (default: 0 = unknown, no overflow
                          check). When set, the populated ledger
                          is checked for overflow AFTER the model
                          call returns; an overflow exits 2 with
                          the SCOPE §18 overflow error. SCOPE §18.
  --max-turns <n>         upper bound on the agent's
                          model-request/tool-execution cycles
                          (default: 8 per GOAL §2 deliverable 6).
                          Exceeding the limit emits an explicit
                          overflow reason and a completed event
                          with a non-zero exit code (SCOPE §3).
                          0 means the default; a negative value
                          is a configuration error (exit 2).

Exit codes (SCOPE §28):
  0  clean exit (run-mode validation passed; also returned on a
     successful model turn)
  1  generic failure (flag parse error, runtime I/O error, etc.)
  2  configuration error (missing prompt file, invalid --output,
     empty --base-url, empty --model, missing --system-file,
     unknown skill, --system and --system-file both set)
  3  model/API failure (unreachable endpoint, HTTP 5xx, malformed
     SSE; the JSONL stream carries a 'status: FAILED' event and a
     'completed(exit_code: 3)' event before the process exits)
  6  interrupted (SIGINT/SIGTERM on the harness process; the
     JSONL stream carries an interrupted event with session_id
     before the process exits; this is distinct from model-
     timeout exit 6 which carries completed(exit_code: 6))

See docs/ARCHITECTURE.md §"Distribution shape" for the full
contract and the V1 event protocol.

The composed model context is SCOPE §14 ordered: minimal harness
system -> external system/governance (--system or --system-file)
-> loaded skills (--skill NAME) -> user task.
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
	systemText := fs.String("system", "", "inline external system/governance prompt (mutually exclusive with --system-file; one of the two is required when --skill is set, optional otherwise). SCOPE §14.")
	systemFile := fs.String("system-file", "", "optional path to a system prompt file")
	output := fs.String("output", "terminal", `output mode: "terminal" or "jsonl" (default "terminal")`)
	// Run 010 / handoff 038: --limit <n> flag. The value flows
	// into the per-Run ledger via r.Ledger().Limit = *limit after
	// loop.New returns; the overflow check fires AFTER RunOne
	// returns success (defensive: the model call was already
	// made, but the response is not delivered to stdout and the
	// exit code is 2 so the operator notices the
	// misconfiguration). SCOPE §18.
	limit := fs.Int("limit", 0, "configured context limit in tokens (default: 0 = unknown, no overflow check). When set, the populated ledger is checked for overflow AFTER the model call returns; an overflow exits 2 with the SCOPE §18 overflow error. SCOPE §18.")
	contextLimitFlag := fs.Int("context-limit", 0, "the model's context window in tokens, for the Bounded Context Lifecycle (addendum §5). Distinct from --limit, which is an accounting check applied after the call. Zero means take it from the config file's context.model_limit, or leave the context unbounded if that is unset too.")
	// Run 017 / handoff 041: --max-turns <n> flag. Default
	// is 8 per the GOAL §2 deliverable 6 default. The value
	// flows into loop.Config.MaxTurns; the run-mode
	// invocation switches from RunOne to RunAgent in this
	// handoff so the limit is enforced end-to-end. The
	// loop's MaxTurnsError maps to exit 1 (SCOPE §28
	// generic failure, since the task did not complete);
	// PermissionError maps to exit 4 (SCOPE §28 permission
	// violation). The wiring is via the canonical
	// r.cfg.Tools.Dispatch(ctx, call, ws, pol,
	// perm.Authorize) invocation in loop.RunAgent (handoff
	// 040's plumbing), so the permission check is in the
	// dispatch seam per the V1 Dispatch contract.
	//
	// A --max-turns value <= 0 is a configuration error
	// (exit 2): the limit must be a positive integer. The
	// loop itself defaults MaxTurns to 8 when zero, but
	// the cmd-side wiring treats 0 as "use the default 8"
	// for direct backward compatibility with the existing
	// run-mode surface (the flag's default value is 8 per
	// the flag declaration; an explicit --max-turns 0 is
	// rejected with exit 2).
	maxTurns := fs.Int("max-turns", 8, "upper bound on the agent's model-request/tool-execution cycles (default: 8). Exceeding the limit emits an explicit overflow reason and completed(exit_code: 1) per SCOPE §3 'exceeding a limit must produce an explicit observable result'. 0 means the default; a negative value is a configuration error.")

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

	// --system / --system-file: optional, mutually exclusive.
	// --system is the inline-text sibling of --system-file; both
	// flow into loop.Config.SystemExternal. If both are set, exit
	// 2 (config error). --system-file additionally requires the
	// file to exist and be readable; its content is read here so
	// runModeExecute receives the bytes (a TOCTOU read failure
	// between validateReadableFile and os.ReadFile is exit 1,
	// not 2 — the config WAS valid at parse time).
	var systemFileContent string
	if *systemText != "" && *systemFile != "" {
		fmt.Fprintf(os.Stderr, "config error: --system and --system-file are mutually exclusive\n")
		return 2
	}
	if *systemFile != "" {
		if err := validateReadableFile(*systemFile); err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot read system-file %q: %v\n", *systemFile, err)
			return 2
		}
		data, err := os.ReadFile(*systemFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: read system-file %q: %v\n", *systemFile, err)
			return 2
		}
		systemFileContent = string(data)
	}

	// --skill + --skills-dir: optional. --skills-dir is the
	// test-only override (when non-empty, REPLACES BOTH search
	// roots); --skill is the skill name to load. An unknown
	// --skill name is a configuration error (SCOPE §15: "V1
	// skills should primarily inject reusable instructions/context")
	// and exits 2 per GOAL §2 and TG1. The validation runs
	// BEFORE the --prompt-file check so a missing prompt file
	// doesn't mask a missing skill (and so the error message is
	// about the skill, not the prompt). The loaded *Skill is
	// RETAINED here (handoff 032 discarded the return value
	// because composition was deferred; handoff 033 threads it
	// through to runModeExecute for the SCOPE §14 composition).
	var loadedSkill *skill.Skill
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
		s, err := skill.Load(*skillName, skill.LoadOptions{
			SkillsDir:    resolvedSkillsDir,
			WorkspaceDir: *workspace,
			HomeDir:      resolvedHome,
		})
		if err != nil {
			if errors.Is(err, skill.ErrSkillNotFound) {
				fmt.Fprintf(os.Stderr, "config error: unknown skill %q\n", *skillName)
				return 2
			}
			fmt.Fprintf(os.Stderr, "config error: load skill %q: %v\n", *skillName, err)
			return 2
		}
		loadedSkill = s
	}

	// Run 017 / handoff 041: --max-turns validation.
	// Negative values are a SCOPE §28 configuration error
	// (the limit must be a positive integer or zero per
	// the help text above; the loop itself defaults
	// MaxTurns to 8 when the cmd leaves it at zero). The
	// validation runs BEFORE the --prompt-file check so
	// a missing prompt file doesn't mask an invalid
	// --max-turns value (and so the error message is
	// about --max-turns, not the prompt).
	if *maxTurns < 0 {
		fmt.Fprintf(os.Stderr, "config error: --max-turns must be >= 0, got %d\n", *maxTurns)
		return 2
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
	var prompt []byte
	if *promptFile == "-" {
		// The prompt comes from stdin (SCOPE §5: `cat task.md |
		// simple-harness run ...`). This used to return 0 without
		// reading anything — a documented no-op reported as success.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "internal error: read prompt from stdin: %v\n", err)
			return 1
		}
		prompt = data
	} else {
		if err := validateReadableFile(*promptFile); err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot read prompt-file %q: %v\n", *promptFile, err)
			return 2
		}
		// Errors that escape the validateReadableFile check (a
		// TOCTOU race where the file disappears between validation
		// and read) are exit 1: the config WAS valid at parse time.
		data, err := os.ReadFile(*promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "internal error: read prompt-file: %v\n", err)
			return 1
		}
		prompt = data
	}

	return runModeExecute(
		string(prompt),
		*baseURL,
		*model,
		*workspace,
		*output,
		*stateDir,
		*systemText,
		systemFileContent,
		loadedSkill,
		*limit,
		*maxTurns,
		*contextLimitFlag,
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
// Handoff 033 adds three parameters for SCOPE §14 composition:
//
//   - systemText: the value of --system (or "" if unset)
//   - systemFileContent: the file's bytes read in runRun (or ""
//     if --system-file was not set)
//   - loadedSkill: the *Skill returned by skill.Load (or nil if
//     --skill was not set)
//
// Handoff 038 adds one more parameter:
//
//   - limit: the value of --limit <n> (or 0 if unset). The
//     value flows into the per-Run ledger via
//     r.Ledger().Limit = limit after loop.New returns; the
//     overflow check fires AFTER RunOne returns success.
//
// systemText and systemFileContent are mutually exclusive
// (enforced in runRun with exit 2); the inner executor receives
// exactly one or the other and threads the resolved value into
// loop.Config.SystemExternal. loadedSkill is dereferenced into a
// []skill.Skill so loop.Config.Skills carries the SCOPE §14
// composition shape (a list of skills; the V1 wire allows only
// one but the type is the seam for future multi-skill runs).
//
// The function:
//  1. Loads the resolved config (config.Load() — existing API).
//  2. Generates a session ID (newSessionID() — existing helper in
//     main.go).
//  3. Decides the emitter writer (stdout for jsonl, sidecar for
//     terminal).
//  4. Decides the loop's r.out writer (io.Discard for jsonl,
//     stdout for terminal — the TG3 stdout-purity contract).
//  5. Constructs loop.Config + model.NewClient + loop.New with
//     the SCOPE §14 composition wired into the loop.Config.
//  6. Calls r.RunOne(context.Background(), prompt).
//  7. Maps the returned error to a SCOPE §28 exit code and emits
//     Completed(exit_code) if the error path didn't already emit
//     Completed (the loop's success path emits Completed(0) from
//     inside RunOne).
//  8. Checks r.Ledger().Overflow() if limit > 0 (handoff 038).
//
// The function returns the SCOPE §28 exit code.
func runModeExecute(prompt, baseURL, modelName, workspace, outputMode, stateDir, systemText, systemFileContent string, loadedSkill *skill.Skill, limit, maxTurns, contextLimit int) (exitCode int) {
	// Defensive double-check on the mutual-exclusion of --system
	// and --system-file. runRun already rejects this with exit 2
	// before this function is reached; the inner check covers any
	// test that bypasses the outer parser.
	if systemText != "" && systemFileContent != "" {
		fmt.Fprintf(os.Stderr, "config error: --system and --system-file are mutually exclusive\n")
		return 2
	}
	systemExternal := systemText + systemFileContent

	var skills []skill.Skill
	if loadedSkill != nil {
		skills = []skill.Skill{*loadedSkill}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}
	// The shell tool's default deadline is configuration, applied here
	// because the tool registry is process-global and config is loaded
	// per subcommand.
	builtins.DefaultTimeout = cfg.ShellTimeout

	// MCP servers are wired here, after the flags are parsed and the
	// workspace is known: doing it before flag parsing made
	// `run --help` fail with exit 2 when a declared server was down,
	// and gave the adapters an empty workspace to authorize against.
	ws, err := path.New(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: workspace %q: %v\n", workspace, err)
		return 2
	}
	if mgr, _, mcpErr := cmdMcpInit(context.Background(), &cfg, activePermissionMode, globalRegistry, ws); mcpErr != nil {
		fmt.Fprintf(os.Stderr, "simple-harness: mcp server unreachable: %v\n", mcpErr)
		return 2
	} else if mgr != nil {
		defer mgr.Close()
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
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	var interrupted atomic.Bool
	go func() {
		if _, ok := <-sigCh; ok {
			interrupted.Store(true)
			cancel()
		}
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
		BaseURL:         normalizedBase,
		Model:           modelName,
		Workspace:       workspace,
		Permission:      permissionStr,
		OutputMode:      outputMode,
		MaxOutputTokens: cfg.Model.MaxOutputTokens,
		ReasoningEffort: cfg.Model.ReasoningEffort,
		EnableThinking:  cfg.Model.EnableThinking,
		ThinkingBudget:  cfg.Model.ThinkingBudget,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: cannot open session writer: %v\n", err)
		return 1
	}
	// session.json records the exit code the process actually
	// returns. It used to re-derive one from the error, and said 1
	// for a permission violation the process exited 4 on, and
	// completed/0 for a --limit overflow that exited 2.
	defer func() {
		finalStatus := session.StatusFailed
		switch {
		case interrupted.Load():
			finalStatus = session.StatusInterrupted
		case exitCode == 0:
			finalStatus = session.StatusCompleted
		}
		if werr := sessWriter.Write(finalStatus, exitCode); werr != nil {
			fmt.Fprintf(os.Stderr, "warning: session.json not written: %v\n", werr)
		}
	}()

	modelOpts := model.Options{
		BaseURL:         normalizedBase,
		Model:           modelName,
		APIKey:          cfg.Model.APIKey,
		Temperature:     cfg.Model.Temperature,
		MaxOutputTokens: cfg.Model.MaxOutputTokens,
		ReasoningEffort: cfg.Model.ReasoningEffort,
		EnableThinking:  cfg.Model.EnableThinking,
		ThinkingBudget:  cfg.Model.ThinkingBudget,
		RequestTimeout:  cfg.Model.RequestTimeout,
	}
	client := model.NewClient(modelOpts)
	// Run 017 / handoff 041: registry wiring into
	// loop.New(loop.Config{Tools: ...}) per the GOAL §2
	// deliverable 4. The registry is the FROZEN
	// `*tools.Registry` instance populated by
	// builtins.RegisterBuiltins(globalRegistry) in main()
	// (cmd/simple-harness/main.go:1044); wiring it into
	// loop.Config.Tools enables loop.RunAgent (handoff
	// 040) to dispatch tool calls. MaxTurns is the
	// --max-turns flag value (0 means "use the loop's
	// default 8", which is the documented cmd-side
	// behavior per the flag's help text above).
	r := loop.New(loop.Config{
		Model:          modelOpts,
		Workspace:      workspace,
		Permission:     permissionStr,
		System:         loop.HarnessSystem,
		SystemExternal: systemExternal,
		Skills:         skills,
		Tools:          globalRegistry,
		MaxTurns:       maxTurns,
		ContextPolicy:  contextPolicyFrom(cfg.Context, contextLimit),
		OnMessage:      persistLoopMessage(sessWriter),
	}, client, em, loopOut)

	// Run 010 / handoff 038: --limit <n> overflow wiring on the
	// per-Run ledger. Set Limit immediately after loop.New
	// returns so RunOne's internal PopulateLedger call sees the
	// configured value. Limit <= 0 disables the check (the
	// existing Ledger.Overflow() semantics at
	// internal/context/context.go:196-197). Setting Limit here is
	// the cmd-side binding seam (no loop.Config field added).
	r.Ledger().Limit = limit

	// Run 008 (handoff 030): record the user message in
	// messages.jsonl before the model call.
	if err := sessWriter.AppendMessage("user", prompt); err != nil {
		fmt.Fprintf(os.Stderr, "warning: messages.jsonl: %v\n", err)
	}

	// Run 017 / handoff 041: switch the run-mode invocation
	// from r.RunOne(ctx, prompt) to r.RunAgent(ctx, prompt)
	// so the --max-turns <n> flag is enforced end-to-end
	// (the GOAL §2 deliverable 6 "exceeding the limit must
	// produce an explicit observable result" discipline).
	// The single-turn happy path (model returns text, no
	// tool calls) has identical observable behavior
	// between RunOne and RunAgent — both emit the same
	// sequence of started + model_request +
	// status(STREAMING) + assistant_stream deltas +
	// status(COMPLETED) + completed(exit_code: 0).
	//
	// The unreachable-endpoint failure path is also
	// identical: the model client returns *model.ModelError
	// before any tool-call accumulation, so the existing
	// mapping (ErrHTTP|ErrParse|ErrUpstream -> exit 3,
	// ErrTimeout -> exit 6) in the error-handling block
	// below continues to work without modification.
	//
	// The permission-violation path is NEW: RunAgent's
	// loop calls r.cfg.Tools.Dispatch with the loop's
	// canonical perm.Authorize pipeline; when the first
	// dispatched call is a permission_denied the loop
	// emits status(FAILED) + completed(exit_code: 4) and
	// returns *loop.PermissionError. The new mapping
	// below handles the sentinel error BEFORE the
	// *model.ModelError mapping so the exit code is 4
	// (permission violation), not 1 (generic failure).
	response, err := r.RunAgent(ctx, prompt)

	// If the signal fired, the SCOPE §26 sequence is: emit
	// `interrupted` event -> flush sidecar (if terminal) -> exit 6.
	// The success path (err == nil) and the existing handoff-024
	// error mapping (errors.As -> SCOPE §28 exit codes) are
	// unchanged; only the NEW `interrupted` branch is added.
	if interrupted.Load() {
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
		// Run 010 / handoff 038: SCOPE §18 overflow check. The
		// ledger has been populated by RunOne's internal
		// PopulateLedger call. If --limit <n> is set (Limit > 0)
		// and Total() exceeds it, fail predictably with exit 2.
		// The model call was already made and the response was
		// appended to the session, but the final exit code is 2
		// so the operator notices the misconfiguration. Per
		// SCOPE §18: "fail predictably if the request cannot fit
		// rather than silently corrupting the conversation."
		if limit > 0 {
			if overflowErr := r.Ledger().Overflow(); overflowErr != nil {
				fmt.Fprintf(os.Stderr, "config error: %v\n", overflowErr)
				return 2
			}
		}
		// The assistant messages were persisted by the loop's
		// OnMessage callback as they were produced.
		_ = response
		return 0
	}

	// Error path: RunOne emitted a Status(FAILED|INTERRUPTED) but
	// NOT a Completed event (loop.go lines 142-159). Emit
	// Completed(exit_code) here so the wire has the terminal event
	// the SCOPE §21 protocol requires. NOTE: close the sidecar
	// AFTER the terminal event so the events.jsonl in state-dir
	// captures the terminal Completed event (the MultiWriter in
	// jsonl mode writes to both sidecar + stdout).
	//
	// Run 017 / handoff 041: loop sentinel error →
	// SCOPE §28 exit-code mapping. The mappings run
	// BEFORE the *model.ModelError mapping because
	// RunAgent's PermissionError wraps a tool dispatch
	// error (not a model error) and must map to exit 4
	// (permission violation), not exit 1 (generic
	// failure). The loop's RunAgent already emits
	// Completed(exit_code) on the failure path
	// (Completed(4) for *PermissionError,
	// Completed(1) for *MaxTurnsError, Completed(2) for
	// *ConfigError) — run-mode does NOT re-emit
	// Completed; it just maps the sentinel to the
	// SCOPE §28 exit code and returns.
	if err != nil {
		var permErr *loop.PermissionError
		var maxTurnsErr *loop.MaxTurnsError
		var cfgErr *loop.ConfigError
		var ctxErr *loop.ContextBudgetError
		switch {
		case errors.As(err, &ctxErr):
			// Addendum §21: the failure must identify its cause.
			// This one goes to stderr as well as the sidecar,
			// unlike the other sentinels, because it is a
			// configuration problem the operator has to act on —
			// the limit is too small, the instructions too large,
			// or the tool surface too wide — and a silent exit
			// leaves them without any of that.
			fmt.Fprintf(os.Stderr, "config error: %v\n", ctxErr.Underlying)
			_ = sidecar.Sync()
			_ = sidecar.Close()
			return 2
		case errors.As(err, &permErr):
			_ = sidecar.Sync()
			_ = sidecar.Close()
			return 4
		case errors.As(err, &maxTurnsErr):
			_ = sidecar.Sync()
			_ = sidecar.Close()
			return 1
		case errors.As(err, &cfgErr):
			_ = sidecar.Sync()
			_ = sidecar.Close()
			return 2
		}
	}
	var me *model.ModelError
	if errors.As(err, &me) {
		switch me.Kind {
		case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
			// Surface the model/API error so exit 3 is diagnosable. It was
			// previously swallowed here — visible in neither the pane,
			// events.jsonl, nor session.json — which cost three diagnoses of
			// the DeepSeek strict-schema 400 on 2026-09-06.
			fmt.Fprintln(os.Stderr, "model/API error (exit 3):", err)
			_ = em.Completed(3)
			if sidecar != nil {
				_ = sidecar.Sync()
				_ = sidecar.Close()
			}
			return 3
		case model.ErrTimeout:
			fmt.Fprintln(os.Stderr, "model/API timeout (exit 6):", err)
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

// contextPolicyFrom translates the config file's context section into
// the loop's policy (addendum §17).
//
// flagLimit is --context-limit, which is the model's context window.
// It is deliberately NOT --limit: that flag is an accounting check
// ("tell me if the composition exceeds n", verified after the call
// and exiting 2), and this is a budget the harness plans against
// before the call. Treating them as one made `--limit 100` mean "this
// model has a hundred-token window", which failed the run for the
// wrong reason with the wrong exit code.
//
// The flag wins over the config file when set: a flag is a deliberate
// act for this run, a config file is a standing preference. When
// neither says anything the limit is unknown, and the harness
// accounts and reports without bounding rather than guessing a limit
// it would be unsafe to be wrong about.
func contextPolicyFrom(cc config.ContextConfig, flagLimit int) loop.ContextPolicy {
	limit := cc.ModelLimit
	if flagLimit > 0 {
		limit = flagLimit
	}
	return loop.ContextPolicy{
		Disabled:                 !cc.Bounded(),
		ModelLimit:               limit,
		GenerationReserve:        cc.GenerationReserve,
		SafetyReserve:            cc.SafetyReserve,
		KeepRecentTurns:          cc.KeepRecentTurns,
		DisableToolResultPruning: !cc.PruningEnabled(),
		DisableCompaction:        !cc.CompactionEnabled(),
	}
}

// persistLoopMessage returns the loop.Config.OnMessage callback that
// appends every message the agent loop produces — assistant messages
// with their tool calls, tool results, the final answer — to
// messages.jsonl, so the durable record is the execution history and
// not only the prompt and a concatenation of the assistant's text.
func persistLoopMessage(w *session.Writer) func(model.Message) {
	return func(m model.Message) {
		rec := session.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			rec.ToolCalls = append(rec.ToolCalls, session.ToolCallRecord{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		if err := w.AppendRecord(rec); err != nil {
			fmt.Fprintf(os.Stderr, "warning: messages.jsonl: %v\n", err)
		}
	}
}
