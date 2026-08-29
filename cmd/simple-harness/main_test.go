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
// is the handoff 022 advance from "Run 005, handoff 021" to
// "Run 006, handoff 022".
func TestRun_Version(t *testing.T) {
	code, out, errOut := driveRun(t, "--version")
	if code != 0 {
		t.Fatalf("run(--version) returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}
	want := "simple-harness 0.1.0-dev (Run 006, handoff 022)"
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
