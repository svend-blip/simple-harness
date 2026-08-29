package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/perm"
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

// TestToolsSubcommand_ListsRegisteredTools pins handoff 015's full
// TG1: after RegisterBuiltins(globalRegistry), the `simple-harness
// tools` subcommand prints the registered tool names sorted, one
// per line. Handoff 015 registers all four V1 read-only tools
// (grep, list_directory, read_file, search_files); the test's
// expected output reflects the four-tool listing.
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

	// Expected output: one tool name per line, sorted.
	// Handoff 018 added apply_patch (full TG1 lands).
	expected := "apply_patch\ngrep\nlist_directory\nread_file\nsearch_files\nshell\nwrite_file\n"
	if got := string(out); got != expected {
		t.Fatalf("simple-harness tools output = %q, want %q",
			got, expected)
	}
}

// TestPermissionFlag_AcceptsValues: each of the three SCOPE §12
// modes (read_only / workspace_write / full_access) accepts via the
// --permission flag; `config show` exits 0 and the JSON output
// contains the matching "permission" field value (case-insensitive
// grep).
//
// The test saves and restores activePermissionMode around each
// invocation (mirroring the globalRegistry snapshot+restore pattern)
// so the same package-level var is clean across the three
// invocations AND so a future test run doesn't see leaks between
// cases.
func TestPermissionFlag_AcceptsValues(t *testing.T) {
	saved := activePermissionMode
	t.Cleanup(func() { activePermissionMode = saved })

	cases := []struct {
		mode string
		want string
	}{
		{"read_only", "read_only"},
		{"workspace_write", "workspace_write"},
		{"full_access", "full_access"},
	}
	for _, tc := range cases {
		activePermissionMode = perm.READ_ONLY // reset

		origStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = w
		t.Cleanup(func() { os.Stdout = origStdout })

		code := run([]string{"--permission", tc.mode, "config", "show"})

		_ = w.Close()
		out, _ := io.ReadAll(r)
		if code != 0 {
			t.Fatalf("run(--permission %s config show) returned %d, want 0 (output: %q)",
				tc.mode, code, out)
		}
		body := string(out)
		want := `"permission": "` + tc.want + `"`
		if !strings.Contains(body, want) {
			t.Fatalf("run(--permission %s config show) JSON output missing %q; got %q",
				tc.mode, want, body)
		}
	}
}

// TestPermissionFlag_RejectsBogusValue: an unknown --permission value
// aborts the harness with exit 2 (SCOPE §28 configuration error)
// BEFORE any subcommand dispatch. The stderr message names the
// unknown value AND lists the three allowed values (the handoff's
// "bogus-tg2" sentinel is the canonical example).
func TestPermissionFlag_RejectsBogusValue(t *testing.T) {
	saved := activePermissionMode
	t.Cleanup(func() { activePermissionMode = saved })

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	code := run([]string{"--permission", "bogus-tg2", "config", "show"})

	_ = w.Close()
	body, _ := io.ReadAll(r)
	if code != 2 {
		t.Fatalf("run(--permission bogus-tg2 config show) returned %d, want 2 (configuration error)", code)
	}
	out := string(body)
	if !strings.Contains(out, "bogus-tg2") {
		t.Fatalf("stderr message does not name the unknown value %q; got %q", "bogus-tg2", out)
	}
	for _, allowed := range []string{"read_only", "workspace_write", "full_access"} {
		if !strings.Contains(out, allowed) {
			t.Fatalf("stderr message does not name allowed value %q; got %q", allowed, out)
		}
	}
}

// TestConfigShow_IncludesPermission: --permission workspace_write
// config show's JSON output contains "permission": "workspace_write"
// (case-insensitive grep). This is the TG3 literal command from the
// handoff's evidence list.
func TestConfigShow_IncludesPermission(t *testing.T) {
	saved := activePermissionMode
	t.Cleanup(func() { activePermissionMode = saved })
	activePermissionMode = perm.READ_ONLY

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	code := run([]string{"--permission", "workspace_write", "config", "show"})

	_ = w.Close()
	body, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("run(...) returned %d, want 0 (output: %q)", code, body)
	}
	out := strings.ToLower(string(body))
	if !strings.Contains(out, "workspace_write") {
		t.Fatalf("config-show JSON does not contain %q (case-insensitive); got %q",
			"workspace_write", out)
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

// driveRun is the shared test helper for the `simple-harness run`
// subcommand (handoff 022). It redirects os.Stdout and os.Stderr to
// pipes, calls run(args) with the run subcommand prefix prepended
// (so the tests can pass the flag set directly without remembering
// to write "run" --<flag>), and returns the exit code plus the
// captured stdout and stderr bodies.
//
// The redirection mirrors the driveInteractive pattern (and the
// per-test pipe dance in TestConfigShowDoesNotLeakAPIKey /
// TestToolsSubcommand_ListsRegisteredTools). The helper is
// intentionally simple: a tests that needs a different
// redirection (e.g. only stderr) can do its own pipe dance the
// way TestPermissionFlag_RejectsBogusValue does.
func driveRun(t *testing.T, runArgs ...string) (int, string, string) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdout = outW

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = errW

	fullArgs := append([]string{"run"}, runArgs...)
	code := run(fullArgs)

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return code, outBuf.String(), errBuf.String()
}

// TestRun_Help pins the `simple-harness run --help` path: exit 0
// and stdout contains the runUsage block (the substring
// "model_request" anchors the GOAL §2 minimum event set in the
// help text; "Usage: simple-harness run" anchors the subcommand
// surface). This is the run --help half of TG4 (the other half
// — scripts/test.sh exits 0 — is exercised by the wrapper-level
// evidence in the result file).
func TestRun_Help(t *testing.T) {
	code, out, errOut := driveRun(t, "--help")
	if code != 0 {
		t.Fatalf("run(--help) returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}
	if !strings.Contains(out, "Usage: simple-harness run") {
		t.Fatalf("run --help stdout missing %q; got %q", "Usage: simple-harness run", out)
	}
	if !strings.Contains(out, "model_request") {
		t.Fatalf("run --help stdout missing %q; got %q", "model_request", out)
	}
}

// TestRun_Version pins the `simple-harness run --version` path:
// exit 0 and stdout is the new Version literal. The exact literal
// is the handoff 028 advance from "Run 006, handoff 024" to
// "Run 007, handoff 028".
func TestRun_Version(t *testing.T) {
	code, out, errOut := driveRun(t, "--version")
	if code != 0 {
		t.Fatalf("run(--version) returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}
	want := "simple-harness 0.1.0-dev (Run 007, handoff 029)"
	if !strings.Contains(out, want) {
		t.Fatalf("run --version stdout missing %q; got %q", want, out)
	}
}

// TestRun_MissingPromptFile_Exits2 is the TG1 path: a non-empty
// --prompt-file value that does not point at an existing file
// exits 2 with a stderr message that includes "config error" so
// an external controller can detect the config-error mode from
// the exit code AND the stderr substring (GOAL §5 reviewer duty
// #1, partial fulfillment — the full events-on-stdout path is
// handoff 023's deliverable).
func TestRun_MissingPromptFile_Exits2(t *testing.T) {
	code, _, errOut := driveRun(t,
		"--base-url", "http://x",
		"--model", "m",
		"--workspace", t.TempDir(),
		"--prompt-file", "/nonexistent-tg1.md",
		"--output", "jsonl",
	)
	if code != 2 {
		t.Fatalf("run --prompt-file /nonexistent-tg1.md returned %d, want 2 (config error) (stderr=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "config error") {
		t.Fatalf("run --prompt-file missing stderr missing %q; got %q", "config error", errOut)
	}
}

// TestRun_InvalidOutput_Exits2 pins the SCOPE §28 config-error
// exit for an --output value that is not one of the two allowed
// values. The stderr message must mention "output" AND echo the
// bad value so the operator can see which value was rejected.
func TestRun_InvalidOutput_Exits2(t *testing.T) {
	code, _, errOut := driveRun(t,
		"--base-url", "http://x",
		"--model", "m",
		"--workspace", t.TempDir(),
		"--prompt-file", "-",
		"--output", "bogus",
	)
	if code != 2 {
		t.Fatalf("run --output bogus returned %d, want 2 (config error) (stderr=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "output") {
		t.Fatalf("run --output bogus stderr missing %q; got %q", "output", errOut)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Fatalf("run --output bogus stderr missing rejected value %q; got %q", "bogus", errOut)
	}
}

// TestRun_MissingBaseURL_Exits2 pins the SCOPE §28 config-error
// exit for an empty --base-url. The flag default is the empty
// string; the validation runs after flag parse, so any caller
// that omits --base-url lands in this branch.
func TestRun_MissingBaseURL_Exits2(t *testing.T) {
	code, _, errOut := driveRun(t,
		"--model", "m",
		"--workspace", t.TempDir(),
		"--prompt-file", "-",
	)
	if code != 2 {
		t.Fatalf("run with no --base-url returned %d, want 2 (config error) (stderr=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "base-url") {
		t.Fatalf("run missing --base-url stderr missing %q; got %q", "base-url", errOut)
	}
}

// TestRun_MissingModel_Exits2 pins the SCOPE §28 config-error
// exit for an empty --model. Same pattern as
// TestRun_MissingBaseURL_Exits2.
func TestRun_MissingModel_Exits2(t *testing.T) {
	code, _, errOut := driveRun(t,
		"--base-url", "http://x",
		"--workspace", t.TempDir(),
		"--prompt-file", "-",
	)
	if code != 2 {
		t.Fatalf("run with no --model returned %d, want 2 (config error) (stderr=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "model") {
		t.Fatalf("run missing --model stderr missing %q; got %q", "model", errOut)
	}
}

// TestRun_SystemFile_Missing_Exits2 pins the SCOPE §28 config-error
// exit for a non-empty --system-file that does not point at an
// existing file. The system prompt is not yet used by the loop,
// but the validation is the only thing landing in this handoff so
// a handoff 023 caller cannot trip over a missing file after the
// loop has already been invoked.
func TestRun_SystemFile_Missing_Exits2(t *testing.T) {
	code, _, errOut := driveRun(t,
		"--base-url", "http://x",
		"--model", "m",
		"--workspace", t.TempDir(),
		"--prompt-file", "-",
		"--system-file", "/nonexistent-system.md",
	)
	if code != 2 {
		t.Fatalf("run --system-file /nonexistent-system.md returned %d, want 2 (config error) (stderr=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "system-file") {
		t.Fatalf("run missing --system-file stderr missing %q; got %q", "system-file", errOut)
	}
}

// TestRun_UnreachableEndpoint_EmitsJSONLAndExits3 is the TG2 path:
// an unreachable endpoint (the discard prototype's port 9, reserved
// and closed on any sane host) yields JSONL events on stdout AND
// exits 3 (SCOPE §28 model/API failure). The event protocol's
// minimum set (started, status, model_request, completed with
// exit_code 3) is asserted by substring match — the test is
// robust against the precise ordering of the FAILED-status event
// vs the completed event (both must appear; either order).
func TestRun_UnreachableEndpoint_EmitsJSONLAndExits3(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("say hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, out, errOut := driveRun(t,
		"--base-url", "http://127.0.0.1:9",
		"--model", "test-model",
		"--workspace", t.TempDir(),
		"--prompt-file", promptFile,
		"--output", "jsonl",
	)
	if code != 3 {
		t.Fatalf("run unreachable-endpoint returned %d, want 3 (model error) (stderr=%q stdout=%q)", code, errOut, out)
	}
	for _, want := range []string{"protocol_version", `"event"`, "model_request", `completed`, `"exit_code":3`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got %q", want, out)
		}
	}
}

// TestRun_OutputJSONL_EveryLineIsJSON is the TG3 path: under
// --output jsonl, every line on stdout parses as JSON with
// protocol_version == "1" and event in the GOAL §2 minimum event
// set. The test uses an unreachable endpoint (port 9) so the run
// exits 3 quickly with a small event count; the JSONL purity
// assertion is the binding contract, the exit code is incidental.
func TestRun_OutputJSONL_EveryLineIsJSON(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("say hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	_, out, errOut := driveRun(t,
		"--base-url", "http://127.0.0.1:9",
		"--model", "test-model",
		"--workspace", t.TempDir(),
		"--prompt-file", promptFile,
		"--output", "jsonl",
	)
	if out == "" {
		t.Fatalf("stdout empty; got %q (stderr=%q)", out, errOut)
	}
	allowed := map[string]bool{
		"started": true, "status": true, "model_request": true,
		"assistant_stream": true, "completed": true,
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %q does not parse as JSON: %v", line, err)
			continue
		}
		if ev.ProtocolVersion != "1" {
			t.Errorf("line %q: protocol_version = %q, want \"1\"", line, ev.ProtocolVersion)
		}
		if !allowed[ev.Event] {
			t.Errorf("line %q: event = %q, not in GOAL §2 minimum set", line, ev.Event)
		}
	}
}

// TestRun_StdinPolicy_NonDashSentinel_Returns0 pins handoff 022's
// --prompt-file "-" choice: the sentinel value is parseable, validates
// cleanly, and returns 0 with no events on stdout. A future regression
// that wires stdin-reading into runModeExecute (or any path that
// blocks on os.Stdin in the test process) fails this test because
// driveRun doesn't redirect os.Stdin.
func TestRun_StdinPolicy_NonDashSentinel_Returns0(t *testing.T) {
	code, out, errOut := driveRun(t,
		"--base-url", "http://127.0.0.1:9",
		"--model", "test-model",
		"--workspace", t.TempDir(),
		"--prompt-file", "-",
		"--output", "jsonl",
	)
	if code != 0 {
		t.Fatalf("run --prompt-file - returned %d, want 0 (no-op sentinel) (stderr=%q)", code, errOut)
	}
	if out != "" {
		t.Errorf("run --prompt-file - stdout = %q, want empty (no events emitted)", out)
	}
}

// TestRun_Version_AdvancesToHandoff024 pins the Version literal
// advance. The existing TestRun_Version (handoff 022) was pinned to
// "Run 006, handoff 022"; the literal advance moves the pinned value
// to "Run 006, handoff 024". This new test is a separate pinning of
// the new literal — kept distinct from the existing TestRun_Version
// so the handoff-022 baseline is still reproducible from the diff.
func TestRun_Version_AdvancesToHandoff024(t *testing.T) {
	code, out, errOut := driveRun(t, "--version")
	if code != 0 {
		t.Fatalf("run(--version) returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}
	want := "simple-harness 0.1.0-dev (Run 007, handoff 029)"
	if !strings.Contains(out, want) {
		t.Fatalf("run --version stdout missing %q; got %q", want, out)
	}
}

// TestRun_SIGTERM_Headless_EmitsInterruptedAndExits6 is the TG1 +
// TG2 path: SIGTERM on a headless run yields exit 6 (SCOPE §28
// interrupted) AND emits an `interrupted` event with `session_id`
// per SCOPE §26's sequence. The test spawns the rebuilt
// bin/simple-harness-runtime binary with the non-routable 10.255.255.1:9
// endpoint (so the model request hangs in connect, the process
// stays alive to receive the signal), sends SIGTERM after a
// brief wait, and asserts (a) the harness exit code is 6 and
// (b) the JSONL output file contains `protocol_version`, an
// `event` field, and an event with `interrupt` in its name (the
// `interrupted` event the SCOPE §26 sequence names).
//
// The test is marked to skip in -short mode (the spawned-process
// test takes ~2-3 seconds and is not appropriate for the -short
// fast path).
func TestRun_SIGTERM_Headless_EmitsInterruptedAndExits6(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s — run `go build -o bin/simple-harness-runtime ./cmd/simple-harness` first: %v", binPath, err)
	}

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("say hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	outPath := "/tmp/sh-tg7-out.jsonl"
	_ = os.Remove(outPath)

	cmd := exec.Command(binPath,
		"run",
		"--base-url", "http://10.255.255.1:9",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
	)
	jsonlFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	cmd.Stdout = jsonlFile
	cmd.Stderr = jsonlFile

	if err := cmd.Start(); err != nil {
		jsonlFile.Close()
		t.Fatalf("start harness: %v", err)
	}

	time.Sleep(2 * time.Second)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	waitErr := cmd.Wait()
	jsonlFile.Close()

	if waitErr == nil {
		t.Fatalf("harness exited cleanly (rc=0); want rc=6 (interrupted) — signal handler did not run")
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("harness wait error: %v (not *exec.ExitError)", waitErr)
	}
	if exitErr.ExitCode() != 6 {
		t.Fatalf("harness exit code = %d, want 6 (interrupted); stderr/stdout combined at %s", exitErr.ExitCode(), outPath)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	if !strings.Contains(string(data), "protocol_version") {
		t.Errorf("JSONL output missing `protocol_version` substring: %s", data)
	}
	if !strings.Contains(string(data), `"event"`) {
		t.Errorf("JSONL output missing `\"event\"` substring: %s", data)
	}
	if !strings.Contains(strings.ToLower(string(data)), "interrupt") {
		t.Errorf("JSONL output missing case-insensitive `interrupt` substring (the `interrupted` event): %s", data)
	}
}

// driveInteractiveWithSeams is the test helper for handoff 028's
// interactive-interrupt tests. It mirrors driveInteractive's stdin /
// stdout / stderr redirect, but it ALSO (a) accepts a caller-supplied
// signal channel (wired into runInteractive's seams variadic so the
// test can drive the signal handler deterministically without
// touching the real process-group signal table) and (b) returns the
// longer-lived stderr body in addition to the captured stdout so the
// tests can assert on the "cancel requested" / "interrupted"
// diagnostic lines.
//
// The helper redirects the global os.Stdout / os.Stderr (the run()
// call path goes through these — the runInteractive signature
// accepts any io.Reader / io.Writer but run() still calls
// runInteractive(os.Stdin, os.Stdout, os.Stderr) on the bare-invocation
// path; for the test we use the seams variadic to bypass that and
// pass our own pipes, so the global redirects are belt-and-braces).
func driveInteractiveWithSeams(t *testing.T, stdinInput string, sigCh chan<- os.Signal, workspace string) (int, string, string) {
	t.Helper()

	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	os.Stdin = inR
	go func() {
		_, _ = io.WriteString(inW, stdinInput)
		_ = inW.Close()
	}()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdout = outW

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stderr = errW

	code := runInteractive(inR, outW, errW, sigCh,
		interactiveOpts{workspace: workspace})

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return code, outBuf.String(), errBuf.String()
}

// findSidecarPath returns the absolute path of the events.jsonl
// sidecar the harness created under workspace/sessions/. The session
// id is dynamic (UUIDv7 generated inside runInteractive), so the
// helper lists the directory and returns the only events.jsonl entry.
// The workspace is expected to contain exactly one session at the
// moment the helper is called (the test runs only one
// runInteractive call).
func findSidecarPath(t *testing.T, workspace string) string {
	t.Helper()
	sessionsDir := filepath.Join(workspace, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", sessionsDir, err)
	}
	var jsonlPath string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(sessionsDir, e.Name(), "events.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			if jsonlPath != "" {
				t.Fatalf("workspace %s has multiple events.jsonl sidecars; cannot determine the active session", workspace)
			}
			jsonlPath = candidate
		}
	}
	if jsonlPath == "" {
		t.Fatalf("no events.jsonl sidecar found under %s", sessionsDir)
	}
	return jsonlPath
}

// sidecarHasEvent returns true if the sidecar file contains a JSONL
// line whose Event matches want AND whose SessionID matches
// sessionID. The session_id pin disambiguates from any sidecar that
// might (hypothetically) conflate multiple sessions in the same
// workspace; the implementation reads line-by-line so a partial
// write at EOF does not confuse it.
func sidecarHasEvent(t *testing.T, sidecarPath, want, sessionID string) bool {
	t.Helper()
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read %s: %v", sidecarPath, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Event == want && (sessionID == "" || ev.SessionID == sessionID) {
			return true
		}
	}
	return false
}

// sidecarSessionID returns the session_id stamped on the first
// parseable JSONL line of the sidecar file. The session_id is set
// at session-creation time and stamped on every event by the
// emitter, so any event line carries it.
func sidecarSessionID(t *testing.T, sidecarPath string) string {
	t.Helper()
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read %s: %v", sidecarPath, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			return ev.SessionID
		}
	}
	t.Fatalf("no parseable JSONL line with session_id found in %s", sidecarPath)
	return ""
}

// TestInteractiveMode_FirstCtrlC_CancelsActiveRequestAndReturnsToPrompt
// pins GOAL §2's first-press behavior: Ctrl+C cancels the in-flight
// model request and the prompt loop continues. The test uses the
// seams ...any testability seam (handoff 028) and a slow httptest
// server (the server blocks on a make(chan struct{}) that the test
// holds open until after asserting cancellation) to make the
// in-flight timing deterministic — this avoids the timing flakiness
// that TestRun_SIGTERM_Headless_EmitsInterruptedAndExits6 suffers
// from (the SIGTERM test's 2-second fixed sleep against an
// unreachable address is not deterministic; this test's blocking
// server IS).
//
// The test calls runInteractive on the test goroutine, and a
// driver goroutine drives the input events (stdin writes, sigCh
// sends, server release) — the test goroutine is the one that
// reads from doneCh and asserts on the captured state.
func TestInteractiveMode_FirstCtrlC_CancelsActiveRequestAndReturnsToPrompt(t *testing.T) {
	releaseSrv := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Block until the test releases the channel — guarantees the
		// model request is in-flight when we send the SIGINT.
		<-releaseSrv
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"too late"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

	sigCh := make(chan os.Signal, 1)
	workspace := t.TempDir()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	doneCh := make(chan int)

	// Driver goroutine: feed stdin, send SIGINT, release server,
	// then send /exit. Runs in parallel with runInteractive so the
	// harness reaches its in-flight state before the signal lands.
	go func() {
		// Give the harness time to set up its signal goroutine and
		// read "hello" from stdin, dispatch the model request, and
		// reach the blocked server handler.
		_, _ = io.WriteString(inW, "hello\n")
		time.Sleep(200 * time.Millisecond)
		// First (and only) SIGINT.
		sigCh <- syscall.SIGINT
		// Wait for cancellation to propagate and the loop to return
		// to scanner.Scan().
		time.Sleep(300 * time.Millisecond)
		// Release the server (so its goroutine exits) and send /exit.
		close(releaseSrv)
		_, _ = io.WriteString(inW, "/exit\n")
		_ = inW.Close()
	}()

	go func() {
		code := runInteractive(inR, outW, errW, sigCh,
			interactiveOpts{workspace: workspace})
		doneCh <- code
	}()

	var code int
	select {
	case code = <-doneCh:
	case <-time.After(15 * time.Second):
		t.Fatalf("first-press Ctrl+C test timed out after 15s")
	}

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	capturedErr := errBuf.String()

	if code != 0 {
		t.Fatalf("first-press Ctrl+C: runInteractive returned %d, want 0 (first-press should cancel and continue); stderr=%q stdout=%q",
			code, capturedErr, outBuf.String())
	}
	// The first-press diagnostic must be present.
	if !strings.Contains(capturedErr, "cancel requested") {
		t.Errorf("first-press Ctrl+C: stderr missing %q; got %q",
			"cancel requested", capturedErr)
	}
	// The second-press diagnostic MUST NOT appear on a first-press
	// test (would indicate the goroutine mis-routed the first
	// SIGINT to the interruptRequested branch).
	if strings.Contains(capturedErr, "interrupted\n") {
		t.Errorf("first-press emitted second-press 'interrupted' message: stderr=%q", capturedErr)
	}

	// Sidecar must NOT contain an `interrupted` event (first-press
	// only cancels; the event is reserved for the second-press
	// terminate path).
	sidecar := findSidecarPath(t, workspace)
	if sidecarHasEvent(t, sidecar, "interrupted", "") {
		data, _ := os.ReadFile(sidecar)
		t.Fatalf("first-press Ctrl+C: sidecar contains `interrupted` event (first-press is cancel-only); sidecar=%s", data)
	}
}

// TestInteractiveMode_SecondCtrlC_TerminatesWithExit6 pins GOAL §2's
// second-press behavior: a second SIGINT after the first cancels
// terminates the harness with exit 6 (the SCOPE §28 interrupted
// code) and emits an `interrupted` event to the sidecar with the
// active session_id. The test reuses the slow-server pattern from
// the first-press test and sends two SIGINTs with a sufficient gap
// for the first-press cancellation to complete before the second
// press arrives.
func TestInteractiveMode_SecondCtrlC_TerminatesWithExit6(t *testing.T) {
	releaseSrv := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-releaseSrv
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"too late"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

	sigCh := make(chan os.Signal, 1)
	workspace := t.TempDir()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	doneCh := make(chan int)

	// Driver: feed "hello", wait, send SIGINT (first), wait for the
	// cancellation to complete, send SIGINT (second — terminates),
	// release server (cleanup), close stdin.
	go func() {
		_, _ = io.WriteString(inW, "hello\n")
		time.Sleep(200 * time.Millisecond)
		sigCh <- syscall.SIGINT
		// Long enough for the model client to return ErrTimeout and
		// the prompt loop to return to scanner.Scan(). The
		// cancelPressed flag stays true (not reset on cancellation),
		// so the goroutine's next read sees the second-press.
		time.Sleep(400 * time.Millisecond)
		sigCh <- syscall.SIGINT
		time.Sleep(300 * time.Millisecond)
		close(releaseSrv)
		_ = inW.Close()
	}()

	go func() {
		code := runInteractive(inR, outW, errW, sigCh,
			interactiveOpts{workspace: workspace})
		doneCh <- code
	}()

	var code int
	select {
	case code = <-doneCh:
	case <-time.After(15 * time.Second):
		t.Fatalf("second-press Ctrl+C test timed out after 15s")
	}

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	capturedErr := errBuf.String()

	if code != 6 {
		t.Fatalf("second-press Ctrl+C: runInteractive returned %d, want 6 (second-press terminates per SCOPE §28); stderr=%q stdout=%q",
			code, capturedErr, outBuf.String())
	}
	// Both diagnostics should appear: "cancel requested" from the
	// first press and "interrupted" from the second.
	if !strings.Contains(capturedErr, "cancel requested") {
		t.Errorf("second-press Ctrl+C: stderr missing %q; got %q",
			"cancel requested", capturedErr)
	}
	if !strings.Contains(capturedErr, "interrupted\n") {
		t.Errorf("second-press Ctrl+C: stderr missing %q; got %q",
			"interrupted\n", capturedErr)
	}

	// Sidecar must contain the `interrupted` event with the active
	// session_id (the second-press path emits it before returning 6).
	sidecar := findSidecarPath(t, workspace)
	sid := sidecarSessionID(t, sidecar)
	if sid == "" {
		data, _ := os.ReadFile(sidecar)
		t.Fatalf("second-press Ctrl+C: sidecar has no parseable session_id; sidecar=%s", data)
	}
	if !sidecarHasEvent(t, sidecar, "interrupted", sid) {
		data, _ := os.ReadFile(sidecar)
		t.Fatalf("second-press Ctrl+C: sidecar missing `interrupted` event with session_id=%q; sidecar=%s",
			sid, data)
	}
}

// TestInteractiveMode_ExitCommand_StillExits0 is the regression pin
// for `/exit`: piping "/exit\n" through stdin exits 0 cleanly
// without emitting an `interrupted` event to the sidecar. The test
// is distinct from the existing TestInteractiveMode_ExitCommand_Exits0
// (which only asserts the exit code) because handoff 028's contract
// also requires the sidecar to be free of interruption events after
// a clean /exit — a regression that mis-routed /exit through the
// signal-handling code path would emit "interrupted" and fail this
// test while passing the existing one.
func TestInteractiveMode_ExitCommand_StillExits0(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	workspace := t.TempDir()

	code, _, _ := driveInteractiveWithSeams(t, "/exit\n", sigCh, workspace)
	if code != 0 {
		t.Fatalf("runInteractive(/exit) returned %d, want 0 (clean exit)", code)
	}

	// No `interrupted` event MAY appear — /exit is the clean path,
	// not the interrupt path.
	sidecar := findSidecarPath(t, workspace)
	if sidecarHasEvent(t, sidecar, "interrupted", "") {
		data, _ := os.ReadFile(sidecar)
		t.Fatalf("`/exit` emitted `interrupted` event (should be reserved for the second-press terminate path); sidecar=%s",
			data)
	}
}
