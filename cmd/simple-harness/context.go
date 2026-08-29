// Package main's context subcommand: read-only inspection of the
// SCOPE §18 context accounting ledger (Run 010 / handoff 036, the
// ACCOUNTING REPORT work slot). The data source is the per-Run
// ledger that handoff 035 introduced (internal/context.Ledger +
// loop.Run.Ledger() accessor + loop.Run.PopulateLedger(prompt)
// helper). This file is the cmd-side consumer of that data.
//
// Dispatch entry: `runContext(args) int`. It parses the verb
// ("show" in V1) and delegates. Unknown / missing verbs print
// contextUsage and exit 1.
//
// V1 verb is "show" — print the SCOPE §19 accounting report for
// the flag-parsed composition WITHOUT invoking the model client.
// The "doctor" verb lands in handoff 037 (the doctor-diagnostics
// + overflow-integration slot).
//
// Surface contract (GOAL §2 bound decision 1): "One inspection
// engine, two faces: interactive `/context` and headless
// `simple-harness context show` (same flags as `run` so a
// composition can be inspected without executing it)." The
// headless path here mirrors `run`'s flag set (--base-url,
// --model, --workspace, --state-dir, --skills-dir, --skill,
// --prompt-file, --system, --system-file) so a composition can
// be inspected without actually invoking the model. The
// --limit <n> flag is intentionally deferred to handoff 037
// (lands in lockstep with the overflow enforcement per
// internal/context.Ledger.Overflow()).
//
// The function does NOT call r.RunOne(...) — it populates the
// ledger via r.PopulateLedger(prompt) and prints r.Ledger().Report()
// to stdout. The unreachable --base-url is a deterministic
// handle that proves the surface does NOT call the model client.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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

Inspect the SCOPE §19 accounting report for the flag-parsed
composition WITHOUT invoking the model client. The surface's
contract is "inspect a composition without executing it" (GOAL
§2 bound decision 1). The data source is the per-Run
internal/context.Ledger that handoff 035 introduced; the
helper r.PopulateLedger(prompt) populates the ledger from the
flag-parsed config + the prompt file's content, and
r.Ledger().Report() renders the report.

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

Exit codes (SCOPE §28):
  0  clean exit (the ledger populated and the report printed)
  1  generic failure (flag parse error, runtime I/O error)
  2  configuration error (missing/invalid flag, --prompt-file is
     "-", unknown skill, --system and --system-file both set,
     unreadable system-file or prompt-file)
  3  NOT returned by this surface (no model call is made)

The composed model context is SCOPE §14 ordered: minimal harness
system -> external system/governance (--system or --system-file)
-> loaded skills (--skill NAME) -> user task (--prompt-file).
The ` + "`context show`" + ` surface renders this composition as
a per-entry token estimate + a Total line; the report does NOT
include the model's response (no model call is made).
`

// runContext dispatches the "context" subcommand. It mirrors
// runSessions' verb-dispatch shape (sessions.go:72): parse the
// verb, route to the verb-specific helper, return the verb's
// exit code.
//
// Known verbs: "show" (V1). The "doctor" verb lands in handoff
// 037. Unknown verbs print contextUsage + exit 1. Missing verb
// prints contextUsage + exits 1. SCOPE §28 mapping:
//   0 = clean (success)
//   1 = generic failure (parse error, runtime I/O error)
//   2 = configuration error (missing/invalid flag)
func runContext(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, contextUsage)
		return 1
	}
	switch args[0] {
	case "show":
		return runContextShow(args[1:])
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
// NO --limit <n> flag in this handoff. The flag's enforcement
// (overflow detection after the model call returns via
// Ledger.Overflow()) lands in handoff 037 along with the
// --limit <n> plumbing on runRun + runInteractive + runContextShow.
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

	r.PopulateLedger(promptText)
	fmt.Print(r.Ledger().Report())
	return 0
}
