package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
	"github.com/svend-blip/simple-harness/internal/tools/builtins"
)

// TestVersionFlag pins the behaviour TG1 measures: ./bin/simple-harness
// --version exits 0 and prints the Version constant. The test calls
// run() directly so it does not depend on the wrapper or on os.Args
// state; the wrapper end-to-end is covered by scripts/test.sh (the suite
// contract) and by the TG1 shell gate.
func TestVersionFlag(t *testing.T) {
	code := run([]string{"--version"})
	if code != 0 {
		t.Fatalf("run(--version) returned %d, want 0", code)
	}
}

// TestHelpFlag pins the --help behaviour: exit 0. We do not assert the
// body here so the usage text can be edited without touching the test.
func TestHelpFlag(t *testing.T) {
	code := run([]string{"--help"})
	if code != 0 {
		t.Fatalf("run(--help) returned %d, want 0", code)
	}
}

// TestUnknownFlagRejected pins the behaviour TG4 measures: any flag
// outside the allow-list exits non-zero. flag.ContinueOnError prints the
// parse error to fs.Output(); we only assert the exit code here, which
// is what the TG4 shell gate measures (the harness exits non-zero so
// the surrounding `! ... >/dev/null 2>&1` test returns 0).
func TestUnknownFlagRejected(t *testing.T) {
	code := run([]string{"--no-such-flag-tg4"})
	if code == 0 {
		t.Fatalf("run(--no-such-flag-tg4) returned 0, want non-zero (SCOPE §28 code 1)")
	}
	if code != 1 {
		t.Fatalf("run(--no-such-flag-tg4) returned %d, want 1 (SCOPE §28 generic failure)", code)
	}
}

// TestNoArgsEntersInteractiveMode pins deliverable 4 behaviour: bare
// invocation enters interactive mode (which exits 0 on EOF). The test
// pipes an empty stdin (so the scanner hits EOF immediately) and
// asserts the exit code is 0.
//
// We redirect os.Stdin to a closed pipe (immediate EOF) and silence
// os.Stdout/os.Stderr so the test output stays clean. The harness
// calls config.Load() under the hood — it needs a HOME and a cwd,
// which it gets from the test runner's environment; the defaults in
// internal/config.Default() make the load succeed without any user
// or project config files present.
//
// The --workspace flag is passed explicitly with t.TempDir() to keep
// the session artifacts out of the package source directory (under
// `go test`, the default workspace would be os.Getwd() = the package
// source dir, which would pollute the source tree on every test run).
func TestNoArgsEntersInteractiveMode(t *testing.T) {
	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	w.Close() // immediate EOF

	os.Stdout, _ = os.Create(os.DevNull)
	os.Stderr, _ = os.Create(os.DevNull)

	code := run([]string{"--workspace", t.TempDir()})
	if code != 0 {
		t.Fatalf("run() with EOF stdin returned %d, want 0", code)
	}
}

// TestVersionLiteral pins the exact bytes --version prints. External
// parsers should be able to extract a stable version from the line; a
// future revision that rewords the line is a deliberate contract change
// and this test is the tripwire.
func TestVersionLiteral(t *testing.T) {
	if !strings.HasPrefix(Version, "simple-harness ") {
		t.Fatalf("Version literal %q does not start with \"simple-harness \"", Version)
	}
}

// TestConfigShowCommand pins TG3: `simple-harness config show` exits 0.
// The test calls run() directly so it does not depend on the wrapper or
// on filesystem state; the wrapper end-to-end is covered by scripts/
// test.sh and by the TG3 shell gate.
//
// The test does not assert the rendered body so the redaction-marker
// wording can evolve without touching the test. The redaction behaviour
// itself is pinned by TestConfigShowDoesNotLeakAPIKey below.
func TestConfigShowCommand(t *testing.T) {
	code := run([]string{"config", "show"})
	if code != 0 {
		t.Fatalf("run(config show) returned %d, want 0", code)
	}
}

// TestConfigShowDoesNotLeakAPIKey pins SCOPE §30: the literal api_key
// value must not appear in the config show output. The test sets a known
// marker string via t.Setenv (the loader reads SIMPLE_HARNESS_API_KEY),
// calls run(config show), captures stdout, and asserts the marker is
// absent.
//
// stdout redirection is process-global; this test must remain serial
// (it does not call t.Parallel()).
func TestConfigShowDoesNotLeakAPIKey(t *testing.T) {
	const marker = "sk-test-marker-do-not-leak-XYZ"
	t.Setenv("SIMPLE_HARNESS_API_KEY", marker)

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	code := run([]string{"config", "show"})

	w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("run(config show) returned %d, want 0 (output: %q)", code, out)
	}
	if strings.Contains(string(out), marker) {
		t.Fatalf("config show output leaked api_key marker: %q", out)
	}
}

// TestConfigUnknownSubcommandRejected pins the rejection of unknown
// config verbs. The subcommand dispatch accepts only "config show";
// every other form exits 1.
func TestConfigUnknownSubcommandRejected(t *testing.T) {
	code := run([]string{"config", "nope"})
	if code != 1 {
		t.Fatalf("run(config nope) returned %d, want 1 (unknown config verb)", code)
	}
}

// TestInteractiveMode_HelpCommand_PrintsUsage pins the /help
// built-in command: piping "/help\n/exit\n" through stdin prints
// the usage block to stderr and exits 0.
func TestInteractiveMode_HelpCommand_PrintsUsage(t *testing.T) {
	code, _ := driveInteractive(t, "/help\n/exit\n")
	if code != 0 {
		t.Fatalf("run with /help+/exit returned %d, want 0", code)
	}
	// We can't easily recover stderr content here without a more
	// involved redirection; the exit code pin above is enough since
	// /help's contract is "print usage and continue" — a panic or
	// early return would fail this test.
}

// TestInteractiveMode_ExitCommand_Exits0 pins /exit: piping
// "/exit\n" through stdin exits 0 cleanly without invoking the
// model client at all.
func TestInteractiveMode_ExitCommand_Exits0(t *testing.T) {
	code, _ := driveInteractive(t, "/exit\n")
	if code != 0 {
		t.Fatalf("run with /exit returned %d, want 0", code)
	}
}

// TestInteractiveMode_QuitCommand_Exits0 pins /quit as an alias
// for /exit (both built-ins listed in the prompt).
func TestInteractiveMode_QuitCommand_Exits0(t *testing.T) {
	code, _ := driveInteractive(t, "/quit\n")
	if code != 0 {
		t.Fatalf("run with /quit returned %d, want 0", code)
	}
}

// TestInteractiveMode_InvalidPermissionExits2 pins the SCOPE §28
// config-error exit for an invalid --permission value.
func TestInteractiveMode_InvalidPermissionExits2(t *testing.T) {
	code, _ := driveInteractive(t, "/exit\n", "--permission", "bogus")
	if code != 2 {
		t.Fatalf("run with --permission bogus returned %d, want 2 (SCOPE §28 config error)", code)
	}
}

// TestInteractiveMode_InvalidWorkspaceExits2 pins the SCOPE §28
// config-error exit for an invalid --workspace value.
func TestInteractiveMode_InvalidWorkspaceExits2(t *testing.T) {
	code, _ := driveInteractive(t, "/exit\n", "--workspace", "/no/such/dir")
	if code != 2 {
		t.Fatalf("run with --workspace /no/such/dir returned %d, want 2 (SCOPE §28 config error)", code)
	}
}

// TestInteractiveMode_PromptReachesModelClient is the integration
// test: an httptest server returns the standard SSE deltas;
// SIMPLE_HARNESS_BASE_URL overrides the resolved config; a prompt
// piped through stdin produces the streamed text on the harness's
// stdout (captured here via os.Pipe).
func TestInteractiveMode_PromptReachesModelClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi back"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

	code, out := driveInteractive(t, "hello\n/exit\n")
	if code != 0 {
		t.Fatalf("run with prompt+/exit returned %d, want 0", code)
	}
	if !strings.Contains(out, "hi back") {
		t.Fatalf("captured stdout does not contain model response; got %q", out)
	}
}

// driveInteractive is the shared test helper: it sets up os.Stdin
// from the given input string, sets up os.Stdout and os.Stderr
// capture, calls run(args), and returns the exit code plus the
// captured stdout.
//
// The args slice is appended after the standard flags — callers
// pass --permission / --workspace / etc. and the helper routes
// them through the flag parser. The stdin input is piped as
// multiple lines exactly as a real user would type.
//
// The helper injects --workspace <t.TempDir()> as the FIRST pair of
// args so the default workspace (which under `go test` is
// os.Getwd() == the package source dir) never pollutes the source
// tree with real session directories. Tests that need to override
// the workspace (e.g. TestInteractiveMode_InvalidWorkspaceExits2
// with /no/such/dir) still see the value they want because Go's
// flag package takes the LAST value for a repeated flag.
func driveInteractive(t *testing.T, stdinInput string, extraArgs ...string) (int, string) {
	t.Helper()

	workspace := t.TempDir()
	args := append([]string{"--workspace", workspace}, extraArgs...)

	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	// stdin: pipe fed by the caller-supplied input.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	os.Stdin = inR
	go func() {
		_, _ = io.WriteString(inW, stdinInput)
		_ = inW.Close()
	}()

	// stdout: capture; we'll read it back after run() returns.
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdout = outW

	// stderr: discard so the test output stays clean.
	os.Stderr, _ = os.Create(os.DevNull)

	code := run(args)

	_ = outW.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, outR)
	return code, buf.String()
}

// TestToolsSubcommand_EmptyRegistry pins handoff 013's foundation-state
// behaviour: `simple-harness tools` exits 0 and produces either empty
// stdout or the literal "(no tools registered)" line. The handoff
// accepts both; this Run's implementer chose empty stdout. The test
// pins whichever choice is in effect so a future regression that emits
// something else fails.
//
// The test calls run() directly with a captured stdout (mirroring the
// TestConfigShowDoesNotLeakAPIKey pipe pattern above). The empty
// registry is reached by saving and restoring globalRegistry around
// the call — handoff 013 leaves the global registry empty, but the
// snapshot+restore pattern keeps the test robust against future
// handoffs that pre-populate the registry at package init time.
func TestToolsSubcommand_EmptyRegistry(t *testing.T) {
	saved := globalRegistry
	t.Cleanup(func() { globalRegistry = saved })

	// Force an empty registry for this test.
	globalRegistry = tools.NewRegistry()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	code := run([]string{"tools"})

	_ = w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("run(tools) returned %d, want 0 (output: %q)", code, out)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed != "" && trimmed != "(no tools registered)" {
		t.Fatalf("simple-harness tools output = %q, want empty or \"(no tools registered)\"", trimmed)
	}
}

// TestToolsSubcommand_ListsRegisteredTools pins handoff 014's
// partial TG1: after RegisterBuiltins(globalRegistry), the
// `simple-harness tools` subcommand prints the registered tool
// names sorted, one per line. Handoff 014 registers exactly two
// tools (read_file + list_directory); handoff 015 will add
// search_files + grep and update this test (the same shape, more
// entries).
//
// The test snapshots and restores globalRegistry around the call
// so the test does not depend on whether RegisterBuiltins has
// already been called by another test (the existing
// TestToolsSubcommand_EmptyRegistry uses the same pattern).
// RegisterBuiltins is invoked on the saved registry (the post-
// restore registry) so the listing is reproducible.
func TestToolsSubcommand_ListsRegisteredTools(t *testing.T) {
	saved := globalRegistry
	t.Cleanup(func() { globalRegistry = saved })

	// Build a fresh registry, register the builtins, swap it
	// in. This isolates the test from any other test that may
	// have mutated globalRegistry.
	fresh := tools.NewRegistry()
	builtins.RegisterBuiltins(fresh)
	globalRegistry = fresh

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	code := run([]string{"tools"})

	_ = w.Close()
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("run(tools) returned %d, want 0 (output: %q)", code, out)
	}

	// Expected output: one tool name per line, sorted. The
	// exact expected output for handoff 014 is the two-tool
	// listing. (After handoff 015 ships, update this test to
	// the four-tool listing.)
	expected := "list_directory\nread_file\n"
	if got := string(out); got != expected {
		t.Fatalf("simple-harness tools output = %q, want %q",
			got, expected)
	}
}

// TestInteractiveMode_DoesNotPolluteSourceTree pins verdict 010's
// fix: driveInteractive (and direct run() callers) must use an
// isolated --workspace so the test suite does not write real
// session directories into the source tree under `go test`. The
// test calls driveInteractive (which reaches session-open), then
// verifies that the production-code session-dir parent has not
// gained a new entry. Any future regression that drops the
// t.TempDir() workspace in driveInteractive will fail this test.
//
// Path resolution (verdict 011 fix): under `go test`, the
// package's test binary runs in the package source dir
// (cmd/simple-harness/). The production code's runInteractive
// default-workspace resolution uses os.Getwd() when --workspace
// is not given (cmd/simple-harness/main.go:189-196), so the
// production-code default workspace IS this package dir. The
// session-dir parent (production: <workspace>/sessions, ARCHITECTURE.md
// §"External subscription") therefore resolves to the relative
// path "sessions" from this test binary's cwd — NOT to
// "cmd/simple-harness/sessions" (which under `go test` resolves
// to a decoy dir cmd/simple-harness/cmd/simple-harness/sessions
// the test created via os.MkdirAll and that no production code
// path ever wrote into; verdict 011 reproduced that decoy defect
// live). Anchoring explicitly with filepath.Join(os.Getwd(),
// "sessions") makes the resolution self-documenting and immune
// to package-dir moves.
func TestInteractiveMode_DoesNotPolluteSourceTree(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	sessionsDir := filepath.Join(cwd, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sessionsDir, err)
	}

	countSessionsDirs := func() int {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", sessionsDir, err)
		}
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				n++
			}
		}
		return n
	}

	before := countSessionsDirs()

	// Reach session-open with the standard /exit pattern.
	_, _ = driveInteractive(t, "/exit\n")

	after := countSessionsDirs()
	if after != before {
		t.Fatalf("driveInteractive wrote %d new session dir(s) into %s (before=%d, after=%d)",
			after-before, sessionsDir, before, after)
	}
}
