// Command simple-harness is the CLI entry point for the Simple Harness
// project. Run 002 ships the buildable skeleton, the config-show
// subcommand, the model client, and — in handoff 010 — the
// interactive REPL with a minimal single-turn model loop and the
// versioned JSONL event sidecar.
//
// The bare invocation (no args) enters interactive mode: it prints
// a banner showing model / endpoint / workspace / permission /
// session_id, reads prompts from stdin one at a time, streams the
// model's response to stdout, emits the JSONL sidecar to
// <workspace>/sessions/<session-id>/events.jsonl, and honours
// /exit, /quit, /help, /version built-in commands plus SIGINT/
// SIGTERM as exit-6 interruptions per SCOPE §§25, 26, 28.
//
// No tools, no permission enforcement, no multi-turn, no sessions
// persistence beyond the events.jsonl sidecar, no headless
// `simple-harness run` mode. Each of those is a future Run per the
// architecture.
//
// Architectural boundary: this is a Simple Harness component. It does not
// import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/svend-blip/simple-harness/internal/config"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/loop"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/tools"
	"github.com/svend-blip/simple-harness/internal/tools/builtins"
)

// Version is the runtime version literal. It is a package-level const so
// the test in main_test.go can pin the exact bytes --version prints
// without shelling out or reading the binary itself. The format is a
// single line, project-name first, so an external parser does not need to
// interpret it to extract the version.
const Version = "simple-harness 0.1.0-dev (Run 005, handoff 021)"

// globalRegistry is the tool registry the `simple-harness tools`
// subcommand lists. Handoff 013 leaves it EMPTY; Run 014 / Run 015 will
// populate it at startup with the read/search tools.
//
// The variable lives at package scope (not inside run()) so it survives
// across invocations and so a future init() / RegisterAll helper can
// populate it before run() is called.
var globalRegistry = tools.NewRegistry()

// activePermissionMode is the resolved permission mode for the current
// harness execution. Set by the global --permission flag parser in
// run(); read by runConfig() to emit the "permission" field in its JSON
// output. Default is READ_ONLY (SCOPE §12: never silent escalation).
//
// Tests in main_test.go save and restore this var around calls that
// set it, mirroring the globalRegistry snapshot+restore pattern in
// TestToolsSubcommand_EmptyRegistry.
var activePermissionMode = perm.READ_ONLY

// usage is the brief usage summary printed by --help and by the
// interactive-mode /help command. Kept short on purpose; later
// handoffs expand it as the public CLI surface grows.
const usage = `Usage: simple-harness [flags] [subcommand]

Simple Harness — a small, deterministic, terminal-first execution kernel
for one AI role.

Flags:
  --version             print the runtime version and exit 0
  --help                print this usage summary and exit 0
  --workspace <dir>     workspace directory (interactive mode; default: cwd)
  --permission <mode>   permission mode (one of read_only,
                        workspace_write, full_access; default: read_only).
                        Global flag — applies to every subcommand and the
                        interactive mode. SCOPE §12.

Subcommands:
  config show           print the resolved configuration (secrets redacted)

Interactive mode (the default when no flags or subcommands are given)
reads prompts from stdin and streams responses to stdout. Built-in
commands at the prompt: /help, /version, /exit, /quit. EOF on stdin
or Ctrl+C exits cleanly.

Exit codes (SCOPE §28):
  0  clean exit
  1  generic failure
  2  configuration error
  3  model/API failure
  6  interrupted (SIGINT/SIGTERM)

See docs/ARCHITECTURE.md §"Distribution shape" for the full contract.
`

// interactiveOpts holds the parsed flag values that control
// interactive mode. The struct is a thin seam so runInteractive's
// variadic-args signature can stay unchanged across handoffs (the
// zero-args path passes nothing; the flag-parsed path passes one).
//
// Permission is intentionally NOT here in Run 004 — the active
// mode is set by the global --permission parser (parsePermissionGlobal)
// and read directly from the package-level activePermissionMode var
// inside runInteractive. Exposing permission via opts would require
// either a separate flag at the subcommand level or duplicating the
// parser; the global-flag approach is the simpler and correct one
// per SCOPE §12.
type interactiveOpts struct {
	workspace string
}

// run is the testable inner entry point. It returns the process
// exit code rather than calling os.Exit directly so the unit tests
// in main_test.go can drive the same code path the CLI uses without
// forking the binary.
//
// Flag-parsing order (binding per handoff 016):
//
//  1. The --permission flag is a GLOBAL flag: it parses BEFORE
//     subcommand dispatch. SCOPE §12 binds the mode to every
//     subcommand (config show surfaces the resolved mode; interactive
//     mode applies it). Unknown values abort with exit 2 (configuration
//     error).
//  2. Subcommand dispatch ("config" / "tools").
//  3. Interactive-mode flag parsing (the remaining --workspace /
//     --version / --help).
func run(args []string) int {
	mode, args, err := parsePermissionGlobal(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "simple-harness: %v\n", err)
		return 2
	}
	activePermissionMode = mode

	// Subcommand dispatch: "config show" and "tools" are the V1
	// subcommands; everything else falls through to flag parsing.
	if len(args) > 0 && args[0] == "config" {
		return runConfig(args[1:])
	}
	if len(args) > 0 && args[0] == "tools" {
		return runTools(args[1:])
	}

	// No args: enter interactive mode (deliverable 4). The REPL
	// prints the banner, reads from stdin, exits 0 on EOF.
	if len(args) == 0 {
		return runInteractive(os.Stdin, os.Stdout, os.Stderr)
	}

	fs := flag.NewFlagSet("simple-harness", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	version := fs.Bool("version", false, "print the runtime version and exit 0")
	help := fs.Bool("help", false, "print the usage summary and exit 0")
	// --workspace is parsed here for interactive mode. --permission
	// was already consumed by parsePermissionGlobal; if it slipped
	// through (e.g. a test that bypassed the global parser) the
	// inner flag parser would reject it via TG4 (exit 1), which is
	// also acceptable per SCOPE §28 generic-failure.
	workspace := fs.String("workspace", "", "workspace directory (interactive mode only; defaults to cwd)")

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

	// Flags parsed, no --version/--help. Enter interactive mode
	// with --workspace; the permission mode was set at the top.
	return runInteractive(os.Stdin, os.Stdout, os.Stderr,
		interactiveOpts{
			workspace: *workspace,
		})
}

// parsePermissionGlobal extracts --permission <value> (or
// --permission=<value>) from args and returns the resolved mode plus
// the remaining args. The flag is GLOBAL: it's consumed here so
// subcommand dispatch and the inner flag parser never see it.
//
// An unknown value (anything other than read_only / workspace_write
// / full_access, per SCOPE §12) returns an error; the caller in run()
// converts the error to exit 2. An absent flag defaults to
// perm.READ_ONLY (SCOPE §12: never silent escalation).
func parsePermissionGlobal(args []string) (perm.Mode, []string, error) {
	for i := 0; i < len(args); i++ {
		var (
			value    string
			consumed int
		)
		switch {
		case args[i] == "--permission":
			if i+1 >= len(args) {
				return perm.READ_ONLY, args, fmt.Errorf("--permission requires a value")
			}
			value = args[i+1]
			consumed = 2
		case strings.HasPrefix(args[i], "--permission="):
			value = strings.TrimPrefix(args[i], "--permission=")
			consumed = 1
		default:
			continue
		}
		mode, err := perm.ParseMode(value)
		if err != nil {
			return perm.READ_ONLY, args, err
		}
		rest := make([]string, 0, len(args)-consumed)
		rest = append(rest, args[:i]...)
		rest = append(rest, args[i+consumed:]...)
		return mode, rest, nil
	}
	return perm.READ_ONLY, args, nil
}

// runConfig handles the "config" subcommand. In V1 the only verb is
// "show"; everything else is rejected with usage + exit 1.
//
// The resolved-configuration JSON output now surfaces the active
// permission mode as a top-level "permission" field (SCOPE §13:
// "The active effective permission level must always be externally
// observable"). The implementation renders config.Config via the
// existing Render() (which handles api_key redaction per SCOPE §30)
// into a bytes.Buffer, parses that buffer, adds the "permission"
// field, and re-emits with json.MarshalIndent so the field ordering
// is stable.
//
// Why the round-trip instead of a Config struct field? internal/config
// is a Run 002 deliverable and is read-only for Run 004 (per the
// scope fence). The post-hoc merge is the smallest change that
// satisfies TG2 + TG3 without touching internal/config.
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

	// Render the redacted config into a buffer (preserves the
	// "<redacted>" substitution for non-empty api_key per SCOPE §30).
	var buf bytes.Buffer
	if err := cfg.Render(&buf); err != nil {
		fmt.Fprintf(os.Stderr, "config render error: %v\n", err)
		return 1 // SCOPE §28, generic failure
	}

	// Parse the rendered JSON so we can add the "permission" field
	// on top, then re-emit with stable indentation. The renderView
	// types inside internal/config define the JSON shape; parsing
	// into map[string]any is enough to add fields without a struct
	// change in this package.
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		fmt.Fprintf(os.Stderr, "config parse error: %v\n", err)
		return 1
	}
	doc["permission"] = activePermissionMode.String()

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config marshal error: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

// runInteractive is the testable inner entry point for interactive
// mode. The signature takes stdin/stdout/stderr as parameters so
// tests can drive the loop end-to-end against os.Pipe readers /
// writers, and the variadic opts lets the bare-invocation path pass
// nothing (so the existing tests stay simple). When opts is empty,
// the defaults are applied: workspace=cwd, permission=READ_ONLY
// (matching the flag defaults).
//
// The permission mode is read from the package-level
// activePermissionMode var (set by parsePermissionGlobal in run()).
// The loop's Permission field uses the upper-case form
// ("READ_ONLY" / "WORKSPACE_WRITE" / "FULL_ACCESS") to keep the
// Run-002 sidecar contract intact; the config-show JSON and the
// --permission CLI surface use the Mode.String() lower-case form.
func runInteractive(stdin io.Reader, stdout, stderr io.Writer, opts ...interactiveOpts) int {
	var o interactiveOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	// Validate the workspace. Default to cwd when empty; reject
	// anything that isn't a directory (SCOPE §28 exit code 2 for
	// config errors).
	if o.workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "config error: cannot determine cwd: %v\n", err)
			return 2
		}
		o.workspace = cwd
	}
	if info, err := os.Stat(o.workspace); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "config error: workspace %q is not a directory\n", o.workspace)
		return 2
	}

	// The mode was set by parsePermissionGlobal. Convert to the
	// upper-case string form the loop expects (the loop's
	// Permission field predates the Mode type and is part of the
	// Run-002 sidecar contract; not modifying internal/loop/ in
	// this Run).
	permissionStr := modeToLoopString(activePermissionMode)

	// Load resolved config (model, endpoint, api_key, etc.).
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 2
	}

	// Build session identity.
	sessionID, err := newSessionID()
	if err != nil {
		fmt.Fprintf(stderr, "internal error: cannot generate session id: %v\n", err)
		return 1
	}

	// Open the sidecar events.jsonl.
	sidecarDir := filepath.Join(o.workspace, "sessions", sessionID)
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "internal error: cannot create %s: %v\n", sidecarDir, err)
		return 1
	}
	sidecarPath := filepath.Join(sidecarDir, "events.jsonl")
	sidecar, err := os.Create(sidecarPath)
	if err != nil {
		fmt.Fprintf(stderr, "internal error: cannot create %s: %v\n", sidecarPath, err)
		return 1
	}
	defer sidecar.Close()

	em := event.NewEmitter(sidecar, sessionID)

	// Normalize the BaseURL — the config carries "/v1" (SCOPE §29),
	// the model client appends "/v1/chat/completions" itself.
	normalizedBase := loop.NormalizeBaseURL(cfg.Model.BaseURL)

	// Banner to stderr so stdout stays clean for the streamed text.
	fmt.Fprintf(stderr, "session_id: %s\n", sessionID)
	fmt.Fprintf(stderr, "model:      %s\n", cfg.Model.Model)
	fmt.Fprintf(stderr, "endpoint:   %s\n", normalizedBase)
	fmt.Fprintf(stderr, "workspace:  %s\n", o.workspace)
	fmt.Fprintf(stderr, "permission: %s\n", permissionStr)
	fmt.Fprintf(stderr, "events:     %s\n", sidecarPath)
	fmt.Fprintf(stderr, "(type /help for built-in commands, /exit to quit, Ctrl+D to exit)\n")
	fmt.Fprintln(stderr)

	// Wire SIGINT/SIGTERM to exit 6 (SCOPE §28 interrupted). The
	// goroutine emits the two-status sequence to the sidecar before
	// exiting so an external controller sees the interruption state
	// per SCOPE §21 invariant.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		_ = em.Status("INTERRUPTING")
		_ = em.Status("INTERRUPTED")
		fmt.Fprintln(stderr, "interrupted")
		os.Exit(6)
	}()

	// Build the loop.
	client := model.NewClient(model.Options{
		BaseURL:         normalizedBase,
		Model:           cfg.Model.Model,
		APIKey:          cfg.Model.APIKey,
		Temperature:     cfg.Model.Temperature,
		MaxOutputTokens: cfg.Model.MaxOutputTokens,
		RequestTimeout:  cfg.Model.RequestTimeout,
	})
	r := loop.New(loop.Config{
		Model: model.Options{
			BaseURL:         normalizedBase,
			Model:           cfg.Model.Model,
			APIKey:          cfg.Model.APIKey,
			Temperature:     cfg.Model.Temperature,
			MaxOutputTokens: cfg.Model.MaxOutputTokens,
			RequestTimeout:  cfg.Model.RequestTimeout,
		},
		Workspace:  o.workspace,
		Permission: permissionStr,
	}, client, em, stdout)

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var accum strings.Builder
	for {
		if accum.Len() > 0 {
			fmt.Fprint(stderr, "> ")
		} else {
			fmt.Fprint(stderr, "simple-harness> ")
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				fmt.Fprintf(stderr, "stdin error: %v\n", err)
				return 1
			}
			return 0 // clean EOF
		}
		line := scanner.Text()
		if accum.Len() == 0 {
			// First line of a (possibly multi-line) prompt —
			// check for built-in commands and empty-line skip.
			trimmed := strings.TrimSpace(line)
			switch trimmed {
			case "":
				continue
			case "/exit", "/quit":
				return 0
			case "/help":
				fmt.Fprint(stderr, usage)
				continue
			case "/version":
				fmt.Fprintln(stderr, Version)
				continue
			}
		}
		// Multi-line continuation: a trailing `\` means the next
		// line is part of this prompt, joined with `\n`. SCOPE §4.
		if strings.HasSuffix(line, "\\") {
			accum.WriteString(strings.TrimSuffix(line, "\\"))
			accum.WriteByte('\n')
			continue
		}
		accum.WriteString(line)
		prompt := accum.String()
		accum.Reset()

		if _, err := r.RunOne(context.Background(), prompt); err != nil {
			var me *model.ModelError
			if errors.As(err, &me) {
				switch me.Kind {
				case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
					fmt.Fprintf(stderr, "\nmodel error: %v\n", err)
					return 3
				case model.ErrTimeout:
					fmt.Fprintln(stderr, "\ninterrupted")
					return 6
				}
			}
			fmt.Fprintf(stderr, "\nerror: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout) // newline after the streamed response
	}
}

// newSessionID generates a UUIDv7 string (sortable by creation
// time, low collision risk) per ARCHITECTURE.md §"Session identity".
// Layout (16 bytes, RFC 9562 §5.7):
//
//	0                   1                   2                   3
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                           unix_ts_ms                          |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|          unix_ts_ms           |  ver  |       rand_a          |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|var|                        rand_b                             |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
func newSessionID() (string, error) {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0F) | 0x70 // version 7
	b[8] = (b[8] & 0x3F) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// runTools prints the registered tool names, one per line, sorted, to
// stdout. Exit 0 on success.
//
// The handoff 013 contract accepts either "empty stdout" or the literal
// "(no tools registered)" line for the empty-registry case. This Run's
// implementer chose empty stdout (no output) for the empty case so the
// subcommand produces zero output when the registry is empty; Run 014 /
// Run 015 will register tools and the listing will be one tool name per
// line. The choice is recorded in the handoff 013 result's "Tool
// inventory state" subsection.
//
// args is reserved for future flags (e.g. `--json`); the V1 surface
// ignores it.
func runTools(args []string) int {
	_ = args // future: filter / json / etc.

	names := globalRegistry.Names()
	if len(names) == 0 {
		// Empty registry — exit 0 with no output. The choice (vs the
		// literal "(no tools registered)" line) is documented in the
		// handoff 013 result file's "Tool inventory state" subsection.
		return 0
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return 0
}

// modeToLoopString maps a perm.Mode to the upper-case string form the
// internal/loop package expects (loop.Config.Permission predates the
// perm.Mode type and is part of the Run-002 sidecar contract). Unknown
// modes default to "READ_ONLY" per SCOPE §12's never-silent-escalation
// rule.
//
// NOTE: the loop-side string is the canonical upper-case form ("READ_ONLY"
// etc.) because that's what the existing sidecar JSONL events carry.
// internal/config/ is read-only for Run 004, so the canonical Mode is
// communicated to runConfig via Mode.String() (the lower-case wire form
// per SCOPE §12) rather than to internal/loop/ via this helper. The two
// surfaces are independent: the JSON-L wire (config-show JSON) uses
// lower-case; the JSONL sidecar (loop events) uses upper-case. A future
// Run may unify the two by adding a Permission field to internal/config
// (out of scope for Run 004).
func modeToLoopString(m perm.Mode) string {
	switch m {
	case perm.READ_ONLY:
		return "READ_ONLY"
	case perm.WORKSPACE_WRITE:
		return "WORKSPACE_WRITE"
	case perm.FULL_ACCESS:
		return "FULL_ACCESS"
	}
	return "READ_ONLY"
}

func main() {
	builtins.RegisterBuiltins(globalRegistry)
	os.Exit(run(os.Args[1:]))
}
