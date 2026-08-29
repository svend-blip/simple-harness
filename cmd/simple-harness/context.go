// Package main's context subcommand: read-only inspection of the
// SCOPE §18 context accounting ledger (Run 010 / handoff 036, the
// ACCOUNTING REPORT work slot). The data source is the per-Run
// ledger that handoff 035 introduced (internal/context.Ledger +
// loop.Run.Ledger() accessor + loop.Run.PopulateLedger(prompt)
// helper). This file is the cmd-side consumer of that data.
//
// Dispatch entry: `runContext(args) int`. It parses the verb
// ("show" / "doctor") and delegates. Unknown / missing verbs
// print contextUsage and exit 1.
//
// V1 verbs are "show" (SCOPE §19 accounting report, lands in
// handoff 036/037) and "doctor" (SCOPE §20 doctor diagnostics,
// lands in handoff 038). Both surfaces populate the ledger via
// r.PopulateLedger(prompt) and render the snapshot to stdout
// without invoking the model client.
//
// Surface contract (GOAL §2 bound decision 1): "One inspection
// engine, two faces: interactive `/context` and headless
// `simple-harness context show` (same flags as `run` so a
// composition can be inspected without executing it)." The
// headless path here mirrors `run`'s flag set (--base-url,
// --model, --workspace, --state-dir, --skills-dir, --skill,
// --prompt-file, --system, --system-file) so a composition can
// be inspected without actually invoking the model. The
// --limit <n> flag lands in handoff 038 (cmd-side seam via
// r.Ledger().Limit = *limit after loop.New; the overflow check
// uses the existing internal/context.Ledger.Overflow() method
// from handoff 035).
//
// Run 010 / handoff 038 also adds the `formatDoctorFindings`
// helper (shared between the headless `context doctor` surface
// and the `/context-doctor` REPL command in main.go) and the
// `--limit <n>` overflow-integration on `runContextShow` +
// `runContextDoctor` + `runRun` + interactive mode.
//
// The functions do NOT call r.RunOne(...) — they populate the
// ledger via r.PopulateLedger(prompt) and either print
// r.Ledger().Report() (the SCOPE §19 accounting report) or
// formatDoctorFindings(r.Ledger().Doctor()) (the SCOPE §20
// doctor diagnostics). The unreachable --base-url is a
// deterministic handle that proves the surfaces do NOT call the
// model client.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/loop"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/skill"
)

// contextUsage is the per-subcommand usage line printed by
// `simple-harness context show --help` and on unknown-verb /
// missing-verb errors. It mirrors runUsage (run.go) and
// sessionsUsage (sessions.go) in style. The text enumerates every
// flag the surface accepts (same flags as `run`, excluding the
// --limit <n> which lands in handoff 037), the SCOPE §28 exit
// codes the surface can return, and the composed-model-context
// wire shape (SCOPE §14 ordering).
const contextUsage = `Usage: simple-harness context show [flags]
       simple-harness context doctor [flags]

Inspect the SCOPE §19 accounting report (verb "show") or the
SCOPE §20 doctor diagnostics (verb "doctor") for the flag-parsed
composition WITHOUT invoking the model client. The surface's
contract is "inspect a composition without executing it" (GOAL
§2 bound decision 1). The data source is the per-Run
internal/context.Ledger that handoff 035 introduced; the
helper r.PopulateLedger(prompt) populates the ledger from the
flag-parsed config + the prompt file's content, then
r.Ledger().Report() renders the accounting report or
r.Ledger().Doctor() returns the findings that
formatDoctorFindings renders.

Flags:
  --base-url <url>        base URL of the OpenAI-compatible endpoint
                          (required, non-empty; the value is treated
                          as an opaque string by this surface — no
                          HTTP call is made)
  --model <name>          model name (required, non-empty; opaque
                          string for this surface)
  --workspace <dir>       workspace directory (defaults to cwd)
  --state-dir <dir>       state directory for future integrations
                          (defaults to ~/.simple-harness/sessions;
                          not consulted by this surface)
  --skills-dir <dir>      skills directory override (defaults to
                          ~/.simple-harness/skills + <workspace>/.simple-harness/skills;
                          the test-only deterministic handle per
                          GOAL §2)
  --skill <name>          skill name to load; SKILL.md is read from
                          the resolved skills dir; an unknown name
                          is a configuration error (exit 2). The
                          skill's content is composed into the
                          ledger at the SCOPE §14 step 3 position.
  --prompt-file <path>    path to the prompt file (required,
                          non-empty; use a readable file path; the
                          "-" stdin sentinel is NOT supported by
                          ` + "`context show`" + ` — the surface
                          inspects a composition without executing
                          it, and stdin prompting is a future
                          ` + "`run`" + ` feature)
  --system <text>         inline external system/governance prompt
                          (mutually exclusive with --system-file;
                          one of the two is allowed)
  --system-file <path>    optional path to a system prompt file;
                          the file must exist and be readable; the
                          file's content is composed into the
                          ledger at the SCOPE §14 step 2 position.
                          Mutually exclusive with --system.
  --limit <n>             configured context limit in tokens
                          (default: 0 = unknown, no overflow
                          check). When set to a positive integer
                          and the populated ledger's Total()
                          exceeds it, the surface exits 2 with the
                          SCOPE §18 overflow error
                          ("config error: context overflow: ...").
                          When 0 (or unset), the overflow check
                          is skipped. SCOPE §18.

Exit codes (SCOPE §28):
  0  clean exit (the ledger populated and the report / doctor
     findings printed)
  1  generic failure (flag parse error, runtime I/O error)
  2  configuration error (missing/invalid flag, --prompt-file is
     "-", unknown skill, --system and --system-file both set,
     unreadable system-file or prompt-file, context overflow
     when --limit <n> is set and the composition exceeds the
     configured limit)
  3  NOT returned by this surface (no model call is made)

The composed model context is SCOPE §14 ordered: minimal harness
system -> external system/governance (--system or --system-file)
-> loaded skills (--skill NAME) -> user task (--prompt-file).
The ` + "`context show`" + ` surface renders this composition as
a per-entry token estimate + a Total line; the
` + "`context doctor`" + ` surface renders the SCOPE §20
findings (large contributors, duplicates, schema overhead)
instead. Neither surface includes the model's response (no
model call is made).

Verb: doctor
  Print the SCOPE §20 doctor diagnostics (large contributors,
  duplicates, schema overhead) for the flag-parsed composition
  WITHOUT invoking the model client. The data source is the same
  per-Run internal/context.Ledger that the ` + "`show`" + ` verb
  consumes; ` + "`r.PopulateLedger(prompt)`" + ` populates the
  ledger, then ` + "`r.Ledger().Doctor()`" + ` returns the
  findings. The ` + "`--limit <n>`" + ` flag, when set, is
  enforced BEFORE the doctor render path: an overflow returns
  exit 2 with the SCOPE §18 overflow error.
`

// runContext dispatches the "context" subcommand. It mirrors
// runSessions' verb-dispatch shape (sessions.go:72): parse the
// verb, route to the verb-specific helper, return the verb's
// exit code.
//
// Known verbs: "show" (SCOPE §19 accounting report, handoff
// 036/037) and "doctor" (SCOPE §20 doctor diagnostics, handoff
// 038). Unknown verbs print contextUsage + exit 1. Missing verb
// prints contextUsage + exits 1. SCOPE §28 mapping:
//   0 = clean (success)
//   1 = generic failure (parse error, runtime I/O error)
//   2 = configuration error (missing/invalid flag, context overflow)
func runContext(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, contextUsage)
		return 1
	}
	switch args[0] {
	case "show":
		return runContextShow(args[1:])
	case "doctor":
		return runContextDoctor(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown context verb %q\n%s", args[0], contextUsage)
		return 1
	}
}

// runContextShow prints the SCOPE §19 accounting report for the
// flag-parsed composition WITHOUT invoking the model client.
// The function builds a *loop.Run via loop.New(...), populates
// the ledger via r.PopulateLedger(prompt), then prints
// r.Ledger().Report() to stdout.
//
// The flag-parsed config is validated against the same rules
// runRun enforces (per GOAL §2 bound decision 1 — same flags as
// `run`). The function does NOT call config.Load() — the
// --base-url + --model flag values are accepted as opaque strings
// for the report-render path (an unreachable --base-url
// http://127.0.0.1:9 is the determinism handle that proves the
// surface does NOT actually call the model).
//
// --prompt-file "-" is rejected with exit 2 because the surface
// inspects a composition without executing it, and stdin
// prompting is a future `run` feature, not a `context show`
// feature.
//
// Run 010 / handoff 038 adds the `--limit <n>` flag wiring on
// the `context show` surface. The flag value flows CLI →
// `r.Ledger().Limit = *limit` (post-`loop.New` assignment via
// the existing Ledger() accessor + the existing Limit field from
// handoff 035) → `r.Ledger().Overflow()` check. The overflow
// check fires BEFORE the report-render path so no model call is
// made; an overflow returns exit 2 with the SCOPE §18 overflow
// error. Negative values are treated as 0 per the
// `Ledger.Overflow()` semantics at
// internal/context/context.go:196-197.
func runContextShow(args []string) int {
	fs := flag.NewFlagSet("simple-harness context show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	help := fs.Bool("help", false, "print this help and exit 0")
	version := fs.Bool("version", false, "print the runtime version and exit 0")
	baseURL := fs.String("base-url", "", "base URL of the OpenAI-compatible endpoint (required, non-empty)")
	modelName := fs.String("model", "", "model name (required, non-empty; opaque string for this surface)")
	workspace := fs.String("workspace", "", "workspace directory (defaults to cwd)")
	stateDir := fs.String("state-dir", "", "state directory (defaults to ~/.simple-harness/sessions; not consulted by this surface)")
	skillsDir := fs.String("skills-dir", "", "skills directory override (defaults to ~/.simple-harness/skills + <workspace>/.simple-harness/skills; the test-only deterministic handle per GOAL §2)")
	skillName := fs.String("skill", "", "skill name to load; SKILL.md is read from the resolved skills dir; an unknown name is a configuration error (exit 2). Composition into the ledger lands in step (iv) below. SCOPE §15.")
	promptFile := fs.String("prompt-file", "", "path to the prompt file (required, non-empty; '-' is NOT supported by `context show`)")
	systemText := fs.String("system", "", "inline external system/governance prompt (mutually exclusive with --system-file; one of the two is allowed). SCOPE §14.")
	systemFile := fs.String("system-file", "", "optional path to a system prompt file (mutually exclusive with --system). SCOPE §14.")
	limit := fs.Int("limit", 0, "configured context limit in tokens (default: 0 = unknown, no overflow check). When set to a positive integer and the populated ledger's Total() exceeds it, the surface exits 2 with the SCOPE §18 overflow error. SCOPE §18.")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *help {
		fmt.Print(contextUsage)
		return 0
	}
	if *version {
		fmt.Println(Version)
		return 0
	}

	// Reject trailing positional arguments. The surface takes
	// flags only — a stray "extra" arg (e.g. from
	// `context show extra`) is a usage error so the operator
	// notices the typo instead of silently running with a
	// different prompt file (the prompt-file is required, so
	// the missing --prompt-file check would catch it, but the
	// error message would be confusing — "extra" looks like a
	// verb in the dispatcher's eyes).
	if positional := fs.Args(); len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "Usage: simple-harness context show [flags]\nerror: unexpected positional argument(s): %v\n", positional)
		return 1
	}

	if *baseURL == "" {
		fmt.Fprintf(os.Stderr, "config error: --base-url is required\n")
		return 2
	}
	if *modelName == "" {
		fmt.Fprintf(os.Stderr, "config error: --model is required\n")
		return 2
	}
	if *workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine cwd: %v\n", err)
			return 2
		}
		*workspace = cwd
	}
	if *stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine home directory: %v\n", err)
			return 2
		}
		*stateDir = filepath.Join(home, ".simple-harness", "sessions")
	}

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

	if *promptFile == "" {
		fmt.Fprintf(os.Stderr, "config error: --prompt-file is required\n")
		return 2
	}
	if *promptFile == "-" {
		// `context show` does NOT support stdin — the surface's
		// contract is "inspect a composition without executing
		// it", and stdin prompting is a future `run` feature,
		// not a `context show` feature.
		fmt.Fprintf(os.Stderr, "config error: --prompt-file '-' is not supported by `context show`; provide a path to a readable file\n")
		return 2
	}
	if err := validateReadableFile(*promptFile); err != nil {
		fmt.Fprintf(os.Stderr, "config error: cannot read prompt-file %q: %v\n", *promptFile, err)
		return 2
	}
	promptData, err := os.ReadFile(*promptFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: read prompt-file: %v\n", err)
		return 1
	}
	promptText := string(promptData)

	// Build the model client + emitter + loop. The model client is
	// constructed so loop.New accepts it (the type seam), but it
	// is NEVER INVOKED. RequestTimeout: 1s is a safety belt: if a
	// future regression accidentally calls the model client, the
	// timeout fires fast and the test surface catches the
	// regression via a non-zero exit code or a "context deadline
	// exceeded" stderr line.
	client := model.NewClient(model.Options{
		BaseURL:        loop.NormalizeBaseURL(*baseURL),
		Model:          *modelName,
		RequestTimeout: 1 * time.Second,
	})
	em := event.NewEmitter(io.Discard, "sess-context-show")
	var skills []skill.Skill
	if loadedSkill != nil {
		skills = []skill.Skill{*loadedSkill}
	}
	r := loop.New(loop.Config{
		Model: model.Options{
			BaseURL:        loop.NormalizeBaseURL(*baseURL),
			Model:          *modelName,
			RequestTimeout: 1 * time.Second,
		},
		Workspace:      *workspace,
		Permission:     modeToLoopString(activePermissionMode),
		System:         loop.HarnessSystem,
		SystemExternal: *systemText + systemFileContent,
		Skills:         skills,
	}, client, em, io.Discard)

	// Run 010 / handoff 038: --limit <n> overflow wiring on the
	// per-Run ledger. Set Limit immediately after loop.New
	// returns so PopulateLedger + Overflow see the configured
	// value. Limit <= 0 disables the check (the existing
	// Ledger.Overflow() semantics at
	// internal/context/context.go:196-197). Setting Limit here
	// is the cmd-side binding seam (no loop.Config field added).
	r.Ledger().Limit = *limit

	r.PopulateLedger(promptText)

	// Run 010 / handoff 038: SCOPE §18 overflow check BEFORE the
	// report-render path. The check fires BEFORE the report prints
	// so the failure is clean (no model call is made at all by
	// this surface). An overflow returns exit 2 with the SCOPE §18
	// overflow error.
	if overflowErr := r.Ledger().Overflow(); overflowErr != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", overflowErr)
		return 2
	}

	fmt.Print(r.Ledger().Report())
	return 0
}

// runContextDoctor prints the SCOPE §20 doctor diagnostics for the
// flag-parsed composition WITHOUT invoking the model client. The
// function mirrors runContextShow's flag-parsing + validation
// sequence (same 11 flags plus the new --limit <n> flag) but
// renders the Doctor() findings via formatDoctorFindings instead
// of the Report() string. The flow is:
//
//  1. Parse + validate the flag set.
//  2. Read --prompt-file + --system-file + load --skill (the
//     same path runContextShow takes).
//  3. Construct loop.Run with the SCOPE §14 composition wired
//     in.
//  4. Set r.Ledger().Limit = *limit (the cmd-side binding seam,
//     identical to runContextShow).
//  5. Populate the ledger via r.PopulateLedger(prompt).
//  6. Check r.Ledger().Overflow() BEFORE the doctor render path.
//     An overflow returns exit 2 with the SCOPE §18 overflow
//     error (no model call is made at all by this surface).
//  7. Render the Doctor() findings via formatDoctorFindings to
//     stdout.
//
// The function does NOT call r.RunOne(...) — the model client is
// constructed for the type seam but NEVER INVOKED. The
// unreachable --base-url is the determinism handle that proves
// the surface does NOT actually call the model client.
func runContextDoctor(args []string) int {
	fs := flag.NewFlagSet("simple-harness context doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	help := fs.Bool("help", false, "print this help and exit 0")
	version := fs.Bool("version", false, "print the runtime version and exit 0")
	baseURL := fs.String("base-url", "", "base URL of the OpenAI-compatible endpoint (required, non-empty)")
	modelName := fs.String("model", "", "model name (required, non-empty; opaque string for this surface)")
	workspace := fs.String("workspace", "", "workspace directory (defaults to cwd)")
	stateDir := fs.String("state-dir", "", "state directory (defaults to ~/.simple-harness/sessions; not consulted by this surface)")
	skillsDir := fs.String("skills-dir", "", "skills directory override (defaults to ~/.simple-harness/skills + <workspace>/.simple-harness/skills; the test-only deterministic handle per GOAL §2)")
	skillName := fs.String("skill", "", "skill name to load; SKILL.md is read from the resolved skills dir; an unknown name is a configuration error (exit 2). Composition into the ledger lands in step (iv) below. SCOPE §15.")
	promptFile := fs.String("prompt-file", "", "path to the prompt file (required, non-empty)")
	systemText := fs.String("system", "", "inline external system/governance prompt (mutually exclusive with --system-file; one of the two is allowed). SCOPE §14.")
	systemFile := fs.String("system-file", "", "optional path to a system prompt file (mutually exclusive with --system). SCOPE §14.")
	limit := fs.Int("limit", 0, "configured context limit in tokens (default: 0 = unknown, no overflow check). When set to a positive integer and the populated ledger's Total() exceeds it, the surface exits 2 with the SCOPE §18 overflow error. SCOPE §18.")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *help {
		fmt.Print(contextUsage)
		return 0
	}
	if *version {
		fmt.Println(Version)
		return 0
	}

	if positional := fs.Args(); len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "Usage: simple-harness context doctor [flags]\nerror: unexpected positional argument(s): %v\n", positional)
		return 1
	}

	if *baseURL == "" {
		fmt.Fprintf(os.Stderr, "config error: --base-url is required\n")
		return 2
	}
	if *modelName == "" {
		fmt.Fprintf(os.Stderr, "config error: --model is required\n")
		return 2
	}
	if *workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine cwd: %v\n", err)
			return 2
		}
		*workspace = cwd
	}
	if *stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "config error: cannot determine home directory: %v\n", err)
			return 2
		}
		*stateDir = filepath.Join(home, ".simple-harness", "sessions")
	}

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

	if *promptFile == "" {
		fmt.Fprintf(os.Stderr, "config error: --prompt-file is required\n")
		return 2
	}
	if *promptFile == "-" {
		fmt.Fprintf(os.Stderr, "config error: --prompt-file '-' is not supported by `context doctor`; provide a path to a readable file\n")
		return 2
	}
	if err := validateReadableFile(*promptFile); err != nil {
		fmt.Fprintf(os.Stderr, "config error: cannot read prompt-file %q: %v\n", *promptFile, err)
		return 2
	}
	promptData, err := os.ReadFile(*promptFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal error: read prompt-file: %v\n", err)
		return 1
	}
	promptText := string(promptData)

	client := model.NewClient(model.Options{
		BaseURL:        loop.NormalizeBaseURL(*baseURL),
		Model:          *modelName,
		RequestTimeout: 1 * time.Second,
	})
	em := event.NewEmitter(io.Discard, "sess-context-doctor")
	var skills []skill.Skill
	if loadedSkill != nil {
		skills = []skill.Skill{*loadedSkill}
	}
	r := loop.New(loop.Config{
		Model: model.Options{
			BaseURL:        loop.NormalizeBaseURL(*baseURL),
			Model:          *modelName,
			RequestTimeout: 1 * time.Second,
		},
		Workspace:      *workspace,
		Permission:     modeToLoopString(activePermissionMode),
		System:         loop.HarnessSystem,
		SystemExternal: *systemText + systemFileContent,
		Skills:         skills,
	}, client, em, io.Discard)

	// Run 010 / handoff 038: --limit <n> overflow wiring on the
	// per-Run ledger. Same cmd-side binding seam as
	// runContextShow: set Limit immediately after loop.New
	// returns so PopulateLedger + Overflow see the configured
	// value.
	r.Ledger().Limit = *limit

	r.PopulateLedger(promptText)

	// Run 010 / handoff 038: SCOPE §18 overflow check BEFORE the
	// doctor render path. The check fires BEFORE the doctor
	// findings print so the failure is clean (no model call is
	// made at all by this surface).
	if overflowErr := r.Ledger().Overflow(); overflowErr != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", overflowErr)
		return 2
	}

	fmt.Print(formatDoctorFindings(r.Ledger().Doctor()))
	return 0
}

// formatDoctorFindings renders a []context.Finding as a
// multi-line human-readable string for the doctor surface output.
// The function is shared between the headless `context doctor`
// surface (runContextDoctor) and the `/context-doctor` REPL
// command in main.go. The format:
//
//	doctor findings (<N>):
//
//	  <type>:      <category>: <name> — <detail>      (for large + schema findings)
//	  <type>:      <detail>                            (for duplicate + overflow findings)
//
// For zero findings: "doctor findings (0):\n\n  no findings.\n".
// The format must be stable enough that binding tests can grep
// for "large", "task", the contributor name, and "no findings."
// substrings (per handoff 038's GOAL §5 reviewer duty 2).
func formatDoctorFindings(findings []contextpkg.Finding) string {
	if len(findings) == 0 {
		return "doctor findings (0):\n\n  no findings.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "doctor findings (%d):\n\n", len(findings))
	for _, f := range findings {
		switch f.Type {
		case "large", "schema":
			label := fmt.Sprintf("%s: %s", f.Category, f.Name)
			fmt.Fprintf(&b, "  %-12s %-30s — %s\n", f.Type+":", label, f.Detail)
		case "duplicate", "overflow":
			fmt.Fprintf(&b, "  %-12s %s\n", f.Type+":", f.Detail)
		default:
			fmt.Fprintf(&b, "  %-12s %s\n", f.Type+":", f.Detail)
		}
	}
	return b.String()
}
