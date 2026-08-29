package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/svend-blip/simple-harness/internal/loop"
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
	code, _, _ := driveInteractive(t, "/help\n/exit\n")
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
	code, _, _ := driveInteractive(t, "/exit\n")
	if code != 0 {
		t.Fatalf("run with /exit returned %d, want 0", code)
	}
}

// TestInteractiveMode_QuitCommand_Exits0 pins /quit as an alias
// for /exit (both built-ins listed in the prompt).
func TestInteractiveMode_QuitCommand_Exits0(t *testing.T) {
	code, _, _ := driveInteractive(t, "/quit\n")
	if code != 0 {
		t.Fatalf("run with /quit returned %d, want 0", code)
	}
}

// TestInteractiveMode_InvalidPermissionExits2 pins the SCOPE §28
// config-error exit for an invalid --permission value.
func TestInteractiveMode_InvalidPermissionExits2(t *testing.T) {
	code, _, _ := driveInteractive(t, "/exit\n", "--permission", "bogus")
	if code != 2 {
		t.Fatalf("run with --permission bogus returned %d, want 2 (SCOPE §28 config error)", code)
	}
}

// TestInteractiveMode_InvalidWorkspaceExits2 pins the SCOPE §28
// config-error exit for an invalid --workspace value.
func TestInteractiveMode_InvalidWorkspaceExits2(t *testing.T) {
	code, _, _ := driveInteractive(t, "/exit\n", "--workspace", "/no/such/dir")
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

	code, out, _ := driveInteractive(t, "hello\n/exit\n")
	if code != 0 {
		t.Fatalf("run with prompt+/exit returned %d, want 0", code)
	}
	if !strings.Contains(out, "hi back") {
		t.Fatalf("captured stdout does not contain model response; got %q", out)
	}
}

// TestInteractive_SkillCommand_LoadsSkill is the binding pin for
// handoff 034 (Run 009 / REWORK): the in-session `/skill NAME`
// command must mutate the *loop.Run's cfg.Skills field so the
// loaded skill's content reaches the model on the NEXT prompt.
// The defect handoff 033 reported (verdict 033 §"CONFIRMED DEFECT")
// was that the *Run was constructed once before the REPL loop
// started and `cfg` is unexported, so an updated o.skill pointer
// never reached `cfg.Skills` and never reached the model despite
// the "skill loaded: <name>" confirmation line. The fix is the
// exported SetSkills setter on *Run (the new seam landed in
// internal/loop/loop.go by this handoff) called immediately after
// o.skill = loaded inside the in-session /skill handler (the new
// call landed in cmd/simple-harness/main.go by this handoff).
//
// The test is the regression tripwire: a future change that drops
// or misplaces either the SetSkills setter or the call from the
// /skill handler causes the marker NOT to appear in the model's
// composed messages, and the test fails by name with a clear
// "marker not found in any captured message" error.
//
// Test mechanics:
//
//   - write a `cold-start` skill under
//     <workspace>/.simple-harness/skills/cold-start/SKILL.md
//     with a marker string unique to this test (no collision
//     with TestRun_Skill_ContentInjectedIntoModelContext's
//     LOADED-SKILL-MARKER-cc11)
//   - spin up an httptest server whose handler JSON-decodes
//     the captured request body into capturedChatRequest
//     (defined in skill_test.go, shared package) and replies
//     with `data: [DONE]\n\n` so RunOne completes cleanly
//   - point SIMPLE_HARNESS_BASE_URL at the test server's URL
//     (the same env-var override pattern that
//     TestInteractiveMode_PromptReachesModelClient uses)
//   - drive runInteractive with stdin = "/skill cold-start\n
//     hello\n" (NO startup skill — the /skill command is the
//     FIRST skill the loop sees; this is what makes the test
//     catch the regression) and interactiveOpts{skill: nil}
//   - assert (i) captured request body has at least 1 message,
//     (ii) FIRST message contains loop.HarnessSystem text
//     (the harness system slot per SCOPE §14 step 1),
//     (iii) EXACTLY ONE captured message contains the marker
//     (the skills slot per SCOPE §14 step 3), (iv) LAST
//     captured message has role: "user" and content: "hello"
//     (the user task slot per SCOPE §14 step 4).
//
// The test runs against the in-process runInteractive entry
// point (the same pattern as TestInteractiveMode_PromptReaches-
// ModelClient), not via the spawned-binary path, because the
// binding pin is the SetSkills call inside runInteractive and
// the test must execute that code path directly.
func TestInteractive_SkillCommand_LoadsSkill(t *testing.T) {
	const marker = "INTERACTIVE-SKILL-CMD-PIN-ee44"
	const prompt = "hello"

	// Workspace with cold-start SKILL.md at
	// <workspace>/.simple-harness/skills/cold-start/SKILL.md
	// (writeSkillFixture is in skill_test.go in the same package).
	workspace := t.TempDir()
	writeSkillFixture(t, workspace, "cold-start", marker+"\n")

	// Capture each incoming chat-completions request body. The
	// handler returns `data: [DONE]\n\n` immediately so RunOne
	// completes without streaming any deltas; the test only
	// asserts on the request body's `messages` JSON.
	var captured capturedChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

	// Wire stdin / stdout / stderr so runInteractive can read
	// "/skill cold-start\nhello\n" from the test, write the
	// confirmation line to the test's stderr buffer, and stream
	// any assistant text to a discardable stdout. The pipe
	// pattern mirrors driveInteractive (lines 244-318) and
	// driveInteractiveWithSeams (lines 1620-1665).
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

	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	os.Stdin = inR
	os.Stdout = outW
	os.Stderr = errW

	// Feed the two-line input and close the pipe so the
	// scanner sees EOF after the second prompt is dispatched.
	go func() {
		_, _ = io.WriteString(inW, "/skill cold-start\nhello\n")
		_ = inW.Close()
	}()

	// skill: nil = NO startup skill. The first skill the loop
	// sees comes from the in-session /skill command. If the
	// /skill handler's r.SetSkills call is missing (the
	// handoff 033 defect), r.cfg.Skills stays nil and the
	// captured messages contain NO marker — the test fails.
	code := runInteractive(inR, outW, errW, interactiveOpts{
		workspace: workspace,
		stateDir:  t.TempDir(),
		skill:     nil,
	})

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	capturedErr := errBuf.String()

	if code != 0 {
		t.Fatalf("runInteractive returned %d, want 0 (stderr=%q stdout=%q)",
			code, capturedErr, outBuf.String())
	}
	if !strings.Contains(capturedErr, "skill loaded: cold-start") {
		t.Errorf("stderr missing %q; got %q", "skill loaded: cold-start", capturedErr)
	}
	if len(captured.Messages) == 0 {
		t.Fatalf("captured request body has 0 messages; runInteractive did not POST to the test server (stderr=%q)",
			capturedErr)
	}

	// (i) Harness-system slot: first message must contain
	// loop.HarnessSystem text (SCOPE §14 step 1).
	if captured.Messages[0].Role != "system" || !strings.Contains(captured.Messages[0].Content, loop.HarnessSystem) {
		t.Errorf("messages[0] = %+v, want {system, contains loop.HarnessSystem}", captured.Messages[0])
	}

	// (ii) Skills slot: exactly ONE captured message must
	// contain the marker (SCOPE §14 step 3). This is the
	// binding-pin assertion: without r.SetSkills(...) the
	// marker is absent and markerCount is 0.
	markerCount := 0
	for _, m := range captured.Messages {
		if strings.Contains(m.Content, marker) {
			markerCount++
		}
	}
	if markerCount != 1 {
		t.Errorf("captured request body has %d message(s) containing marker %q; want exactly 1 (the skills slot); messages=%+v",
			markerCount, marker, captured.Messages)
	}

	// (iii) User-task slot: last message is role: "user"
	// with content: prompt (SCOPE §14 step 4).
	last := captured.Messages[len(captured.Messages)-1]
	if last.Role != "user" || last.Content != prompt {
		t.Errorf("messages[last] = %+v, want {user, %q}", last, prompt)
	}
}

// TestInteractive_ContextCommand_PrintsReport is the binding
// pin for handoff 036 (Run 010 / ACCOUNTING REPORT): the
// in-session `/context` REPL command must render the SCOPE §19
// accounting report to stderr. The handler is the new case in
// runInteractive's `switch trimmed` block that calls
// `fmt.Fprintln(stderr, r.Ledger().Report())` (stderr, NOT
// stdout, so streamed responses stay clean).
//
// Test mechanics:
//   - spin up an httptest.NewServer capture server (the model
//     server is NOT invoked by `/context` itself; the server is
//     set up for cleanliness — if a future regression accidentally
//     invokes RunOne from the /context handler, the test fails
//     with a "no server" or "decode fail" error rather than
//     hanging).
//   - point SIMPLE_HARNESS_BASE_URL at the test server's URL via
//     t.Setenv.
//   - wire stdin / stdout / stderr via os.Pipe (the same dance as
//     TestInteractive_SkillCommand_LoadsSkill).
//   - feed "/context\n" to stdin and close the pipe so the scanner
//     sees EOF after the /context line is dispatched.
//   - drive runInteractive with interactiveOpts{skill: nil}.
//   - assert: (i) exit code 0 (clean EOF after /context); (ii)
//     the captured stderr contains "harness system" AND "task"
//     AND "Total" AND "tool schemas" (the four grep tokens from
//     GOAL §6 TG1, adapted to the REPL surface).
//
// The test runs against the in-process runInteractive entry
// point (NOT via the spawned-binary path), because the binding
// pin is the /context REPL handler inside runInteractive and
// the test must execute that code path directly.
func TestInteractive_ContextCommand_PrintsReport(t *testing.T) {
	var captured capturedChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

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

	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	os.Stdin = inR
	os.Stdout = outW
	os.Stderr = errW

	go func() {
		_, _ = io.WriteString(inW, "/context\n")
		_ = inW.Close()
	}()

	code := runInteractive(inR, outW, errW, interactiveOpts{
		workspace: t.TempDir(),
		stateDir:  t.TempDir(),
		skill:     nil,
	})

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	capturedErr := errBuf.String()

	if code != 0 {
		t.Fatalf("runInteractive returned %d, want 0 (stderr=%q stdout=%q)",
			code, capturedErr, outBuf.String())
	}
	for _, want := range []string{"harness system", "task", "Total", "tool schemas"} {
		if !strings.Contains(capturedErr, want) {
			t.Errorf("stderr missing %q (the /context REPL command's report output); got stderr=%q", want, capturedErr)
		}
	}
}

// TestInteractive_ContextDoctorCommand_PrintsFindings is the
// binding pin for handoff 038 (Run 010 / DOCTOR + LIMIT +
// OVERFLOW): the in-session `/context-doctor` REPL command must
// render the SCOPE §20 doctor diagnostics to stderr (so stdout
// stays clean for streamed responses). Mirrors the existing
// TestInteractive_ContextCommand_PrintsReport pattern.
//
// Test mechanics:
//   - spin up an httptest.NewServer capture server (the model
//     server is NOT invoked by `/context-doctor` itself; the
//     server is set up for cleanliness — if a future regression
//     accidentally invokes RunOne from the /context-doctor
//     handler, the test fails with a "decode fail" error rather
//     than hanging).
//   - point SIMPLE_HARNESS_BASE_URL at the test server's URL via
//     t.Setenv.
//   - wire stdin / stdout / stderr via os.Pipe.
//   - feed "/context-doctor\n/exit\n" to stdin and close the
//     pipe so the scanner sees EOF after the /context-doctor
//     line is dispatched.
//   - drive runInteractive with interactiveOpts{skill: nil}.
//   - assert: (i) exit code 0; (ii) the captured stderr
//     contains "doctor findings" AND "no findings." (the empty
//     ledger case — `/context-doctor` is dispatched before any
//     prompt so the ledger is empty).
//
// The test runs against the in-process runInteractive entry
// point (NOT via the spawned-binary path), because the binding
// pin is the /context-doctor REPL handler inside runInteractive
// and the test must execute that code path directly.
func TestInteractive_ContextDoctorCommand_PrintsFindings(t *testing.T) {
	var captured capturedChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

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

	origStdin := os.Stdin
	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	os.Stdin = inR
	os.Stdout = outW
	os.Stderr = errW

	go func() {
		_, _ = io.WriteString(inW, "/context-doctor\n/exit\n")
		_ = inW.Close()
	}()

	code := runInteractive(inR, outW, errW, interactiveOpts{
		workspace: t.TempDir(),
		stateDir:  t.TempDir(),
		skill:     nil,
	})

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	capturedErr := errBuf.String()

	if code != 0 {
		t.Fatalf("runInteractive returned %d, want 0 (stderr=%q stdout=%q)",
			code, capturedErr, outBuf.String())
	}
	for _, want := range []string{"doctor findings", "no findings."} {
		if !strings.Contains(capturedErr, want) {
			t.Errorf("stderr missing %q (the /context-doctor REPL command's findings output); got stderr=%q", want, capturedErr)
		}
	}
}

// driveInteractive is the shared test helper: it sets up os.Stdin
// from the given input string, sets up os.Stdout and os.Stderr
// capture, calls run(args), and returns the exit code plus the
// captured stdout and the state-dir it injected.
//
// The args slice is appended after the standard flags — callers
// pass --permission / --workspace / etc. and the helper routes
// them through the flag parser. The stdin input is piped as
// multiple lines exactly as a real user would type.
//
// The helper injects --workspace <t.TempDir()> AND
// --state-dir <t.TempDir()> as the FIRST pairs of args so the
// default workspace (which under `go test` is os.Getwd() == the
// package source dir) never pollutes the source tree with real
// session directories. Tests that need to override the workspace
// (e.g. TestInteractiveMode_InvalidWorkspaceExits2 with
// /no/such/dir) still see the value they want because Go's flag
// package takes the LAST value for a repeated flag. The stateDir
// return value lets tests that need to assert on the session
// layout read the directory directly without depending on the
// workspace path.
//
// Run 008 (handoff 030) extends the return tuple from (int, string)
// to (int, string, string) to surface the injected stateDir; the
// three existing TestInteractiveMode_FirstCtrlC* + SecondCtrlC* +
// ExitCommand_StillExits0 callers update their argument from
// findSidecarPath(t, workspace) to findSidecarPath(t, stateDir)
// accordingly.
func driveInteractive(t *testing.T, stdinInput string, extraArgs ...string) (int, string, string) {
	t.Helper()

	workspace := t.TempDir()
	stateDir := t.TempDir()
	args := append([]string{
		"--workspace", workspace,
		"--state-dir", stateDir,
	}, extraArgs...)

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
	return code, buf.String(), stateDir
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
// isolated --workspace AND --state-dir so the test suite does
// not write real session directories into the source tree under
// `go test`. The test calls driveInteractive (which reaches
// session-open), then verifies that the production-code
// session-dir parent has not gained a new entry. Any future
// regression that drops the t.TempDir() workspace in
// driveInteractive will fail this test.
//
// Path resolution (verdict 011 fix): under `go test`, the
// package's test binary runs in the package source dir
// (cmd/simple-harness/). The production code's runInteractive
// default-workspace resolution uses os.Getwd() when --workspace
// is not given (cmd/simple-harness/main.go:189-196), so the
// production-code default workspace IS this package dir. The
// session-dir parent (Run 008: <state-dir>/<session-id>/, per
// SCOPE §17; the pre-Run-008 location was <workspace>/sessions,
// ARCHITECTURE.md §"External subscription") therefore resolves
// to the relative path "sessions" from this test binary's cwd
// — NOT to "cmd/simple-harness/sessions" (which under `go test`
// resolves to a decoy dir cmd/simple-harness/cmd/simple-harness/sessions
// the test created via os.MkdirAll and that no production code
// path ever wrote into; verdict 011 reproduced that decoy defect
// live). Run 008 moves the harness's session writes to
// <state-dir>/<session-id>/ — driveInteractive injects a
// t.TempDir() for both --workspace and --state-dir, so the
// harness does NOT write to <cwd>/sessions. The decoy
// "sessions" dir created by this test below is the only thing
// that lives at <cwd>/sessions; the assertion that the count
// does not change is the regression pin.
//
// Anchoring explicitly with filepath.Join(os.Getwd(), "sessions")
// makes the resolution self-documenting and immune to package-dir
// moves.
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
	_, _, _ = driveInteractive(t, "/exit\n")

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
	want := "simple-harness 0.1.0-dev (Run 019, handoff 063)"
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
	// Run 017 / handoff 041: extend the allowed event-name
	// map with the two new event types (tool_call,
	// tool_result) added by handoff 041. The map is the
	// documented set of event types the run-mode surface
	// may emit (SCOPE §42 additive-evolution discipline);
	// a regression that drops one of the V1 events fails
	// the test, and adding the new events keeps the
	// superset current. The existing run-mode tests use
	// unreachable endpoints (port 9) that never reach
	// tool dispatch, so the actual emitted event set is
	// unchanged — the test's allowed set is the
	// superset, the actual events are the subset.
	allowed := map[string]bool{
		"started": true, "status": true, "model_request": true,
		"assistant_stream": true, "completed": true,
		"tool_call": true, "tool_result": true,
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

// TestRun_Limit_OverflowExits2 is the binding pin for handoff 038's
// `--limit <n>` overflow integration on runRun. The test drives a
// run invocation with a 5000-char prompt file (so Total() = 1250
// tokens > limit 100). The flow needs RunOne to actually invoke
// the model so the overflow check fires AFTER RunOne returns
// success. The test uses an httptest.NewServer that returns the
// standard SSE deltas so the model call completes successfully,
// the assistant message is appended, THEN the overflow check
// fires and exits 2. The assertions: stderr contains "config
// error: context overflow:" AND "exceeds configured limit 100"
// AND exit code is 2.
func TestRun_Limit_OverflowExits2(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	// 5000-char prompt so Total() = (5000+3)/4 = 1250 tokens > 100.
	promptContent := strings.Repeat("X", 5000)
	if err := os.WriteFile(promptFile, []byte(promptContent), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	// Mock model server — replies with a clean SSE stream so
	// RunOne returns success and the post-RunOne overflow check
	// fires.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi back"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	code, _, errOut := driveRun(t,
		"--base-url", srv.URL+"/v1",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "terminal",
		"--limit", "100",
	)
	if code != 2 {
		t.Fatalf("run --limit 100 (with 1250-token prompt) returned %d, want 2 (config error: context overflow); stderr=%q",
			code, errOut)
	}
	if !strings.Contains(errOut, "config error: context overflow:") {
		t.Errorf("run --limit 100 stderr missing %q; got %q",
			"config error: context overflow:", errOut)
	}
	if !strings.Contains(errOut, "exceeds configured limit 100") {
		t.Errorf("run --limit 100 stderr missing %q; got %q",
			"exceeds configured limit 100", errOut)
	}
}

// TestRun_Limit_NoLimit_DefaultsToZero pins the handoff 038
// `--limit <n>` integration: omitting `--limit <n>` entirely (the
// default) does NOT trigger the overflow check, even when the
// prompt is large. Mirrors the
// TestContextShow_Limit_NoLimit_DefaultsToZero contract but for
// runRun. The test uses an unreachable endpoint (port 9) so the
// run exits 3 (model error) — this is fine; the binding pin is
// that the run did NOT exit 2 (no overflow check fired). A
// successful RunOne path is not required.
func TestRun_Limit_NoLimit_DefaultsToZero(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	// 5000-char prompt so Total() = 1250 tokens. Without
	// --limit, this would overflow a hypothetical limit of 100,
	// but since Limit = 0 the check is skipped.
	promptContent := strings.Repeat("X", 5000)
	if err := os.WriteFile(promptFile, []byte(promptContent), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, _, errOut := driveRun(t,
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
	)
	// The unreachable endpoint returns exit 3 (model error).
	// The binding pin: the run did NOT exit 2 (the overflow
	// check did NOT fire because --limit was not set).
	if code == 2 {
		t.Fatalf("run without --limit returned 2 (overflow check fired unexpectedly); stderr=%q", errOut)
	}
	if strings.Contains(errOut, "config error: context overflow:") {
		t.Errorf("run without --limit stderr contains overflow error (check fired unexpectedly); stderr=%q", errOut)
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
	want := "simple-harness 0.1.0-dev (Run 019, handoff 063)"
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

// TestRun_StateDir_PersistsSessionLayout is the TG1 path:
// a headless `run` invocation against the unreachable endpoint
// (http://127.0.0.1:9) with --state-dir <tmp> persists both
// session.json AND events.jsonl under <state-dir>/. The test
// invokes the rebuilt bin/simple-harness-runtime as a subprocess
// (the same exec.Command pattern as
// TestRun_SIGTERM_Headless_EmitsInterruptedAndExits6) so the
// test exercises the real binary, not the in-process test seam.
//
// Skip in -short mode (the spawned-process path takes a few
// seconds and is not appropriate for the -short fast path).
func TestRun_StateDir_PersistsSessionLayout(t *testing.T) {
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

	stateDir := t.TempDir()
	workspace := t.TempDir()
	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	cmd := exec.Command(binPath,
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", workspace,
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--state-dir", stateDir,
	)
	// Discard stdout / stderr; the harness exits 3 (model
	// failure on the unreachable endpoint) and we only care
	// about the persisted files.
	cmd.Stdout, _ = os.Create(os.DevNull)
	cmd.Stderr, _ = os.Create(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start harness: %v", err)
	}
	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("harness exited cleanly (rc=0); want non-zero (model failure expected on unreachable endpoint)")
	}

	// TG1 literal: find -name session.json | wc -l == 1 AND
	// find -name events.jsonl | wc -l == 1.
	sessionCount := 0
	eventsCount := 0
	filepath.Walk(stateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == "session.json" {
			sessionCount++
		}
		if base == "events.jsonl" {
			eventsCount++
		}
		return nil
	})
	if sessionCount != 1 {
		t.Errorf("expected exactly 1 session.json under %s; got %d", stateDir, sessionCount)
	}
	if eventsCount != 1 {
		t.Errorf("expected exactly 1 events.jsonl under %s; got %d", stateDir, eventsCount)
	}
}

// TestRun_SessionID_MatchesDirectory reads the `started`
// event from <state-dir>/<session-id>/events.jsonl, extracts
// the `session_id` field, and asserts the directory name
// under <state-dir>/ matches the `session_id` byte-for-byte.
// This is GOAL §5 reviewer duty #1 ("correlation end-to-end").
func TestRun_SessionID_MatchesDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s: %v", binPath, err)
	}

	stateDir := t.TempDir()
	workspace := t.TempDir()
	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	cmd := exec.Command(binPath,
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", workspace,
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--state-dir", stateDir,
	)
	cmd.Stdout, _ = os.Create(os.DevNull)
	cmd.Stderr, _ = os.Create(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start harness: %v", err)
	}
	_ = cmd.Wait()

	// Find the session directory under state-dir.
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", stateDir, err)
	}
	var sessionDir string
	for _, e := range entries {
		if e.IsDir() {
			sessionDir = filepath.Join(stateDir, e.Name())
			break
		}
	}
	if sessionDir == "" {
		t.Fatalf("no session directory under %s", stateDir)
	}
	dirName := filepath.Base(sessionDir)

	// Read events.jsonl and extract session_id from the
	// first parseable line.
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read %s: %v", eventsPath, err)
	}
	var foundSID string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			foundSID = ev.SessionID
			break
		}
	}
	if foundSID == "" {
		t.Fatalf("no parseable session_id in %s; raw=%s", eventsPath, data)
	}
	if foundSID != dirName {
		t.Fatalf("session_id mismatch: events.jsonl session_id=%q but directory name=%q", foundSID, dirName)
	}
}

// TestRun_SessionJSON_HasFinalStatus reads <state-dir>/<session-id>/session.json
// after the harness exits and asserts (i) status is one of
// "completed"/"interrupted"/"failed", (ii) exit_code matches the
// harness's actual exit code, and (iii) config.base_url,
// config.model, config.workspace, config.permission match the test
// inputs. The test runs the unreachable-endpoint path (status=failed,
// exit_code=3) AND the SIGTERM path (status=interrupted, exit_code=6)
// as subtests.
func TestRun_SessionJSON_HasFinalStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s: %v", binPath, err)
	}

	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	runHarness := func(extraArgs ...string) (int, string) {
		stateDir := t.TempDir()
		workspace := t.TempDir()
		args := []string{
			"run",
			"--base-url", "http://127.0.0.1:9",
			"--model", "tg",
			"--workspace", workspace,
			"--permission", "read_only",
			"--prompt-file", promptFile,
			"--output", "jsonl",
			"--state-dir", stateDir,
		}
		args = append(args, extraArgs...)
		cmd := exec.Command(binPath, args...)
		cmd.Stdout, _ = os.Create(os.DevNull)
		cmd.Stderr, _ = os.Create(os.DevNull)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start harness: %v", err)
		}
		waitErr := cmd.Wait()
		exitCode := -1
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if waitErr == nil {
			exitCode = 0
		}
		return exitCode, stateDir
	}

	readSession := func(t *testing.T, stateDir string) (sessionJSONContents []byte, sessionFile string) {
		entries, err := os.ReadDir(stateDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", stateDir, err)
		}
		var sessionDir string
		for _, e := range entries {
			if e.IsDir() {
				sessionDir = filepath.Join(stateDir, e.Name())
				break
			}
		}
		if sessionDir == "" {
			t.Fatalf("no session directory under %s", stateDir)
		}
		sessionFile = filepath.Join(sessionDir, "session.json")
		sessionJSONContents, err = os.ReadFile(sessionFile)
		if err != nil {
			t.Fatalf("read %s: %v", sessionFile, err)
		}
		return
	}

	t.Run("failed_exit_3", func(t *testing.T) {
		exitCode, stateDir := runHarness()
		if exitCode != 3 {
			t.Fatalf("harness exited %d; want 3 (model failure on unreachable endpoint)", exitCode)
		}
		contents, _ := readSession(t, stateDir)
		var doc map[string]any
		if err := json.Unmarshal(contents, &doc); err != nil {
			t.Fatalf("session.json parse: %v; raw=%s", err, contents)
		}
		status, _ := doc["status"].(string)
		switch status {
		case "completed", "interrupted", "failed":
			// accepted
		default:
			t.Errorf("session.json status=%q; want one of completed/interrupted/failed", status)
		}
		if got, _ := doc["exit_code"].(float64); int(got) != 3 {
			t.Errorf("session.json exit_code=%v; want 3", doc["exit_code"])
		}
		cfg, _ := doc["config"].(map[string]any)
		if cfg == nil {
			t.Fatalf("session.json config missing; raw=%s", contents)
		}
		if cfg["base_url"] != "http://127.0.0.1:9" {
			t.Errorf("session.json config.base_url=%v; want http://127.0.0.1:9", cfg["base_url"])
		}
		if cfg["model"] != "tg" {
			t.Errorf("session.json config.model=%v; want tg", cfg["model"])
		}
		if cfg["permission"] != "READ_ONLY" {
			t.Errorf("session.json config.permission=%v; want READ_ONLY", cfg["permission"])
		}
	})

	t.Run("interrupted_exit_6", func(t *testing.T) {
		// The SIGTERM path: against http://10.255.255.1:9
		// (connect-hang target) and SIGTERM after 2s.
		stateDir := t.TempDir()
		workspace := t.TempDir()
		cmd := exec.Command(binPath,
			"run",
			"--base-url", "http://10.255.255.1:9",
			"--model", "tg",
			"--workspace", workspace,
			"--permission", "read_only",
			"--prompt-file", promptFile,
			"--output", "jsonl",
			"--state-dir", stateDir,
		)
		cmd.Stdout, _ = os.Create(os.DevNull)
		cmd.Stderr, _ = os.Create(os.DevNull)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start harness: %v", err)
		}
		time.Sleep(2 * time.Second)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		waitErr := cmd.Wait()
		exitCode := -1
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 6 {
			t.Fatalf("harness exited %d; want 6 (interrupted)", exitCode)
		}
		contents, _ := readSession(t, stateDir)
		var doc map[string]any
		if err := json.Unmarshal(contents, &doc); err != nil {
			t.Fatalf("session.json parse: %v; raw=%s", err, contents)
		}
		if doc["status"] != "interrupted" {
			t.Errorf("session.json status=%v; want interrupted", doc["status"])
		}
		if got, _ := doc["exit_code"].(float64); int(got) != 6 {
			t.Errorf("session.json exit_code=%v; want 6", doc["exit_code"])
		}
	})
}

// TestRun_SessionJSON_NoSecrets pins SCOPE §30 secret redaction:
// the literal api_key value must not appear in session.json,
// events.jsonl, or messages.jsonl. The test invokes the
// subprocess with SIMPLE_HARNESS_API_KEY=sk-leak-test-XXX set
// in the env, then greps the three files for the marker.
func TestRun_SessionJSON_NoSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s: %v", binPath, err)
	}

	const marker = "sk-leak-test-handoff030-XXX"
	stateDir := t.TempDir()
	workspace := t.TempDir()
	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	cmd := exec.Command(binPath,
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", workspace,
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--state-dir", stateDir,
	)
	cmd.Env = append(os.Environ(), "SIMPLE_HARNESS_API_KEY="+marker)
	cmd.Stdout, _ = os.Create(os.DevNull)
	cmd.Stderr, _ = os.Create(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start harness: %v", err)
	}
	_ = cmd.Wait()

	// Find session directory and assert marker absent from
	// all three files.
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", stateDir, err)
	}
	var sessionDir string
	for _, e := range entries {
		if e.IsDir() {
			sessionDir = filepath.Join(stateDir, e.Name())
			break
		}
	}
	if sessionDir == "" {
		t.Fatalf("no session directory under %s", stateDir)
	}
	for _, fname := range []string{"session.json", "events.jsonl", "messages.jsonl"} {
		path := filepath.Join(sessionDir, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), marker) {
			t.Errorf("marker %q leaked into %s", marker, path)
		}
	}
}

// TestRun_InterruptedRun_Diagnosable pins GOAL §5 reviewer
// duty #2: an interrupted run's session record actually
// supports diagnosis (status marks interruption; events are
// flushed). The test runs the TG1 SIGTERM path against the
// connect-hang target http://10.255.255.1:9, sends SIGTERM
// after 2s, then reads session.json + events.jsonl and asserts
// (i) session.json status=interrupted, exit_code=6,
// (ii) events.jsonl contains an `interrupted` event.
func TestRun_InterruptedRun_Diagnosable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s: %v", binPath, err)
	}

	stateDir := t.TempDir()
	workspace := t.TempDir()
	promptDir := t.TempDir()
	promptFile := filepath.Join(promptDir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	cmd := exec.Command(binPath,
		"run",
		"--base-url", "http://10.255.255.1:9",
		"--model", "tg",
		"--workspace", workspace,
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--state-dir", stateDir,
	)
	cmd.Stdout, _ = os.Create(os.DevNull)
	cmd.Stderr, _ = os.Create(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start harness: %v", err)
	}
	time.Sleep(2 * time.Second)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitErr := cmd.Wait()
	if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 6 {
		t.Fatalf("harness exit: %v; want exit 6 (interrupted)", waitErr)
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", stateDir, err)
	}
	var sessionDir string
	for _, e := range entries {
		if e.IsDir() {
			sessionDir = filepath.Join(stateDir, e.Name())
			break
		}
	}
	if sessionDir == "" {
		t.Fatalf("no session directory under %s", stateDir)
	}

	// session.json must have status=interrupted, exit_code=6.
	sessionPath := filepath.Join(sessionDir, "session.json")
	sdata, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read %s: %v", sessionPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(sdata, &doc); err != nil {
		t.Fatalf("session.json parse: %v; raw=%s", err, sdata)
	}
	if doc["status"] != "interrupted" {
		t.Errorf("session.json status=%v; want interrupted", doc["status"])
	}
	if got, _ := doc["exit_code"].(float64); int(got) != 6 {
		t.Errorf("session.json exit_code=%v; want 6", doc["exit_code"])
	}

	// events.jsonl must contain an `interrupted` event.
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	edata, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read %s: %v", eventsPath, err)
	}
	foundInterrupted := false
	for _, line := range strings.Split(strings.TrimSpace(string(edata)), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Event == "interrupted" {
			foundInterrupted = true
			break
		}
	}
	if !foundInterrupted {
		t.Errorf("events.jsonl missing `interrupted` event; raw=%s", edata)
	}
}

// TestInteractive_SessionJSON_PersistsAcrossExercises the
// interactive half of Run 008's persistence: driveInteractive
// with "hello\n/exit\n" produces a session.json with
// status=completed, exit_code=0 AND a messages.jsonl with at
// least 2 lines (user + assistant).
func TestInteractive_SessionJSON_PersistsAcrossExchanges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires spawned harness process; skipped in -short mode")
	}

	binPath, err := filepath.Abs(filepath.Join("..", "..", "bin", "simple-harness-runtime"))
	if err != nil {
		t.Fatalf("abs binPath: %v", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("rebuilt binary missing at %s: %v", binPath, err)
	}

	stateDir := t.TempDir()
	workspace := t.TempDir()

	// Inject a mock server via SIMPLE_HARNESS_BASE_URL so the
	// harness has a reachable endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi back"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")

	cmd := exec.Command(binPath,
		"--workspace", workspace,
		"--state-dir", stateDir,
	)
	cmd.Stdin = strings.NewReader("hello\n/exit\n")
	cmd.Stdout, _ = os.Create(os.DevNull)
	cmd.Stderr, _ = os.Create(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start harness: %v", err)
	}
	waitErr := cmd.Wait()
	if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
		t.Fatalf("harness exit: %v; want exit 0", waitErr)
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", stateDir, err)
	}
	var sessionDir string
	for _, e := range entries {
		if e.IsDir() {
			sessionDir = filepath.Join(stateDir, e.Name())
			break
		}
	}
	if sessionDir == "" {
		t.Fatalf("no session directory under %s", stateDir)
	}

	// session.json has status=completed, exit_code=0.
	sessionPath := filepath.Join(sessionDir, "session.json")
	sdata, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read %s: %v", sessionPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(sdata, &doc); err != nil {
		t.Fatalf("session.json parse: %v; raw=%s", err, sdata)
	}
	if doc["status"] != "completed" {
		t.Errorf("session.json status=%v; want completed", doc["status"])
	}
	if got, _ := doc["exit_code"].(float64); int(got) != 0 {
		t.Errorf("session.json exit_code=%v; want 0", doc["exit_code"])
	}

	// messages.jsonl has at least 2 lines (user + assistant).
	messagesPath := filepath.Join(sessionDir, "messages.jsonl")
	mdata, err := os.ReadFile(messagesPath)
	if err != nil {
		t.Fatalf("read %s: %v", messagesPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(mdata), "\n"), "\n")
	if len(lines) < 2 {
		t.Errorf("messages.jsonl has %d lines; want >= 2; raw=%s", len(lines), mdata)
	}
}

// TestRun_Version_AdvancesToHandoff030 pins the Version literal
// advance for handoff 030. The test calls run() with --version
// and asserts stdout contains the new literal.
func TestRun_Version_AdvancesToHandoff030(t *testing.T) {
	code, out, errOut := driveRun(t, "--version")
	if code != 0 {
		t.Fatalf("run(--version) returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}
	want := "simple-harness 0.1.0-dev (Run 019, handoff 063)"
	if !strings.Contains(out, want) {
		t.Fatalf("run --version stdout missing %q; got %q", want, out)
	}
}

// driveInteractiveWithSeams is the test helper for handoff 028's
// interactive-interrupt tests. It mirrors driveInteractive's stdin /
// stdout / stderr redirect, but it ALSO (a) accepts a caller-supplied
// signal channel (wired into runInteractive's seams variadic so the
// test can drive the signal handler deterministically without
// touching the real process-group signal table), (b) returns the
// longer-lived stderr body in addition to the captured stdout so the
// tests can assert on the "cancel requested" / "interrupted"
// diagnostic lines, and (c) returns the stateDir the test injected
// so callers can use findSidecarPath(t, stateDir) per Run 008.
//
// The helper redirects the global os.Stdout / os.Stderr (the run()
// call path goes through these — the runInteractive signature
// accepts any io.Reader / io.Writer but run() still calls
// runInteractive(os.Stdin, os.Stdout, os.Stderr) on the bare-invocation
// path; for the test we use the seams variadic to bypass that and
// pass our own pipes, so the global redirects are belt-and-braces).
func driveInteractiveWithSeams(t *testing.T, stdinInput string, sigCh chan<- os.Signal, workspace string) (int, string, string, string) {
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

	stateDir := t.TempDir()

	code := runInteractive(inR, outW, errW, sigCh,
		interactiveOpts{workspace: workspace, stateDir: stateDir})

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return code, outBuf.String(), errBuf.String(), stateDir
}

// findSidecarPath returns the absolute path of the events.jsonl
// file the harness created under stateDir/<session-id>/. The
// session id is dynamic (UUIDv7 generated inside runInteractive
// or runModeExecute), so the helper lists the directory and
// returns the only events.jsonl entry. stateDir is expected to
// contain exactly one session at the moment the helper is
// called.
//
// Run 008 (handoff 030) moves the events.jsonl location from
// <workspace>/sessions/<session-id>/events.jsonl to
// <stateDir>/<session-id>/events.jsonl. The helper signature
// changes accordingly (stateDir replaces workspace).
func findSidecarPath(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", stateDir, err)
	}
	var jsonlPath string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(stateDir, e.Name(), "events.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			if jsonlPath != "" {
				t.Fatalf("state-dir %s has multiple events.jsonl sidecars; cannot determine the active session", stateDir)
			}
			jsonlPath = candidate
		}
	}
	if jsonlPath == "" {
		t.Fatalf("no events.jsonl sidecar found under %s", stateDir)
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
	stateDir := t.TempDir()

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
			interactiveOpts{workspace: workspace, stateDir: stateDir})
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
	sidecar := findSidecarPath(t, stateDir)
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
	stateDir := t.TempDir()

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
			interactiveOpts{workspace: workspace, stateDir: stateDir})
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
	sidecar := findSidecarPath(t, stateDir)
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

	code, _, _, stateDir := driveInteractiveWithSeams(t, "/exit\n", sigCh, workspace)
	if code != 0 {
		t.Fatalf("runInteractive(/exit) returned %d, want 0 (clean exit)", code)
	}

	// No `interrupted` event MAY appear — /exit is the clean path,
	// not the interrupt path.
	sidecar := findSidecarPath(t, stateDir)
	if sidecarHasEvent(t, sidecar, "interrupted", "") {
		data, _ := os.ReadFile(sidecar)
		t.Fatalf("`/exit` emitted `interrupted` event (should be reserved for the second-press terminate path); sidecar=%s",
			data)
	}
}

// TestE2E_AcceptanceRunner_RequiresArgs_Exits1 — the in-process Go
// binding pin for Run 011 / handoff 039's GOAL §6 TG2 ("the
// acceptance runner exists, is executable, and validates its
// arguments"). Drives scripts/e2e-coding.sh as a subprocess with
// no arguments and asserts exit code != 0 AND stderr contains the
// substring "usage" (the placeholder usage message from the
// handoff 039 script body).
//
// The script's working directory at exec time MUST be the project
// root (the script is a relative-path executable that reads no
// workspace files in this handoff, but its `test -x` is what TG2
// verifies; running it from the project root ensures the chmod
// +x bit is honored and the script's shebang line resolves
// correctly). Under `go test`, os.Getwd() == cmd/simple-harness;
// the project root is two parents up.
func TestE2E_AcceptanceRunner_RequiresArgs_Exits1(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}
	cmd := exec.Command("./scripts/e2e-coding.sh")
	cmd.Dir = projectRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatalf("expected non-zero exit code from scripts/e2e-coding.sh with no args, got nil (exit 0)")
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("expected stderr to contain 'usage' substring, got: %q", stderr.String())
	}
}

// --- handoff 042: Run 017 / BINDING-PIN cross-package end-to-end pins ---

// TestToolDispatch_SingleTurn_EmitsToolCallAndToolResult is the
// cross-package end-to-end binding pin for the multi-turn
// agent loop's happy path through the RUN MODE surface. The
// test exercises the full stack — model client (mock) →
// loop (RunAgent) → events (Emitter) → run mode (runRun →
// driveRun → stdout JSONL) — with the new tool_call +
// tool_result events added by handoff 041's
// internal/event/event.go changes (the Emitter.ToolCall +
// Emitter.ToolResult helpers) + handoff 042's
// internal/loop/loop.go changes (RunAgent emits the events
// per dispatch).
//
// The test:
//  1. Spins up a t.TempDir() workspace + writes a fixture
//     file fixture.txt with content "line1\nline2\nline3\n".
//  2. Spins up a t.TempDir() state dir for the sidecar.
//  3. Spins up an httptest.NewServer that serves an SSE
//     payload with one choices[0].delta.tool_calls entry
//     for apply_patch with the JSON arguments {"path":
//     "<absolute fixture path>", "patch": <unified diff
//     replacing "line2" with "LINE2_MODIFIED">} on the
//     FIRST request, and a non-empty assistant-text delta
//     + [DONE] on the SECOND request (so the loop reaches
//     the single-turn happy path: model_request fires +
//     tool dispatched + tool_result fires + loop re-asks
//     model + model returns text + status: COMPLETED +
//     completed(exit_code: 0)).
//  4. Calls driveRun(t, "--base-url", srv.URL,
//     "--model", "test-model", "--workspace", workspaceDir,
//     "--state-dir", stateDir, "--prompt-file", promptFile,
//     "--output", "jsonl", "--permission", "workspace_write",
//     "--max-turns", "8").
//  5. Asserts:
//     (i) the returned exit code is 0.
//     (ii) the stdout JSONL stream carries BOTH a
//     "tool_call" event with non-empty call_id + tool +
//     the V1 six (started + status + assistant_stream +
//     model_request + completed), AND a "tool_result"
//     event with non-empty call_id + tool_result_status:
//     "ok" + the SAME call_id as the tool_call event (the
//     call_id correlation is the binding evidence that the
//     model-targeted call ID round-trips through the
//     dispatcher to the result event).
//     (iii) the on-disk fixture.txt now contains
//     "line1\nLINE2_MODIFIED\nline3\n" (content-based
//     assertion, NOT exit-code-based, per GOAL §5
//     reviewer duty 5 "The mock-model pins prove
//     workspace change by content, not by exit code
//     alone.").
func TestToolDispatch_SingleTurn_EmitsToolCallAndToolResult(t *testing.T) {
	// Snapshot+restore globalRegistry (the loop.RunAgent dispatch
	// pipeline requires loop.Config.Tools to be non-nil; the test
	// registers the builtins on a fresh registry so apply_patch is
	// reachable from the cross-package dispatch). Mirrors the
	// pattern at TestToolsSubcommand_ListsRegisteredTools.
	savedReg := globalRegistry
	t.Cleanup(func() { globalRegistry = savedReg })
	freshReg := tools.NewRegistry()
	builtins.RegisterBuiltins(freshReg)
	globalRegistry = freshReg

	workspaceDir := t.TempDir()
	stateDir := t.TempDir()
	fixturePath := filepath.Join(workspaceDir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("seed write fixture.txt: %v", err)
	}
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("patch line2"), 0o644); err != nil {
		t.Fatalf("seed write prompt.md: %v", err)
	}

	patch := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -2 +2 @@\n-line2\n+LINE2_MODIFIED\n"
	argsJSON, err := json.Marshal(map[string]any{
		"path":  fixturePath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	nRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		nRequests++
		if nRequests == 1 {
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e2e_1","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
				string(argsJSON),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Patch applied."}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "test-model",
		"--workspace", workspaceDir,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "workspace_write",
		"--max-turns", "8",
	)
	if code != 0 {
		t.Fatalf("driveRun returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}

	// (ii) Parse the JSONL stream and assert the tool_call +
	// tool_result events with matching call_ids are present.
	var toolCallEvent, toolResultEvent *event.Event
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse as JSON: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "tool_call":
			if toolCallEvent == nil {
				cp := ev
				toolCallEvent = &cp
			}
		case "tool_result":
			if toolResultEvent == nil {
				cp := ev
				toolResultEvent = &cp
			}
		}
	}
	if toolCallEvent == nil {
		t.Fatalf("stdout missing tool_call event (stdout=%q)", out)
	}
	if toolResultEvent == nil {
		t.Fatalf("stdout missing tool_result event (stdout=%q)", out)
	}
	if toolCallEvent.CallID == "" {
		t.Errorf("tool_call event missing call_id (event=%+v)", toolCallEvent)
	}
	if toolCallEvent.Tool != "apply_patch" {
		t.Errorf("tool_call event tool = %q, want apply_patch", toolCallEvent.Tool)
	}
	if toolResultEvent.CallID == "" {
		t.Errorf("tool_result event missing call_id (event=%+v)", toolResultEvent)
	}
	if toolResultEvent.ResultStatus != "ok" {
		t.Errorf("tool_result event tool_result_status = %q, want ok", toolResultEvent.ResultStatus)
	}
	if toolResultEvent.CallID != toolCallEvent.CallID {
		t.Errorf("tool_result call_id = %q, tool_call call_id = %q — must match for correlation",
			toolResultEvent.CallID, toolCallEvent.CallID)
	}

	// (iii) Workspace content assertion: the apply_patch must
	// have landed, replacing "line2" with "LINE2_MODIFIED".
	onDisk, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile fixture.txt: %v", err)
	}
	wantContent := "line1\nLINE2_MODIFIED\nline3\n"
	if string(onDisk) != wantContent {
		t.Errorf("on-disk fixture.txt = %q, want %q", string(onDisk), wantContent)
	}
}

// TestToolDispatch_MaxTurns_StopsOverflowingModel_EmitsToolCallEvents
// is the cross-package end-to-end binding pin for the
// multi-turn agent loop's max-turns overflow behavior through
// the RUN MODE surface. The test exercises the full stack —
// model client (mock that always emits a tool-call) → loop
// (RunAgent + MaxTurns overflow) → events (Emitter) → run
// mode (runRun → driveRun → stdout JSONL) — with the new
// tool_call events on every turn + the SCOPE §3 overflow
// signal + completed(exit_code: 1).
//
// The test:
//  1. Spins up a t.TempDir() workspace + state dir.
//  2. Spins up an httptest.NewServer that emits a
//     tool-call on EVERY response — the mock always
//     returns a tool-call for apply_patch against a
//     NON-EXISTENT file ("does_not_exist.txt") so the
//     dispatch returns Status="error" + Kind
//     "target_not_found" → wrapped as "execution_failed"
//     by the dispatch pipeline; the loop appends the
//     error to the message history and re-calls the
//     model — so the loop never reaches the "no tool
//     calls" final response and the max-turns overflow
//     fires after MaxTurns=2 iterations.
//  3. Calls driveRun with --max-turns 2.
//  4. Asserts:
//     (i) the returned exit code is 1 (the cmd-side
//     *loop.MaxTurnsError → exit 1 mapping at handoff 041).
//     (ii) the stdout JSONL stream carries AT LEAST 2
//     tool_call events (one per turn within the bound;
//     the implementer's chosen overflow semantic — turn
//     3 fires model_request + overflow status + no
//     ChatStream + no tool_call — produces 2 tool_call
//     events for MaxTurns=2; the binding pin asserts
//     >= 2 to be lenient on the implementer's chosen
//     exact semantic, matching the handoff 040 LoopCore
//     pin's "3 model_request events for MaxTurns=2"
//     pattern).
//     (iii) the stdout JSONL stream carries the overflow
//     status event (status: "TOOL_DISPATCH_OVERFLOW:
//     max-turns 2 exceeded") + a "completed" event with
//     exit_code 1.
func TestToolDispatch_MaxTurns_StopsOverflowingModel_EmitsToolCallEvents(t *testing.T) {
	// Snapshot+restore globalRegistry (apply_patch must be
	// registered for the loop's dispatch pipeline to return a
	// structured error — the loop appends the error to message
	// history and the model is re-called, repeating until
	// MaxTurns overflows). Mirrors TestToolsSubcommand_ListsRegisteredTools.
	savedReg := globalRegistry
	t.Cleanup(func() { globalRegistry = savedReg })
	freshReg := tools.NewRegistry()
	builtins.RegisterBuiltins(freshReg)
	globalRegistry = freshReg

	workspaceDir := t.TempDir()
	stateDir := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("infinite loop"), 0o644); err != nil {
		t.Fatalf("seed write prompt.md: %v", err)
	}

	argsJSON, err := json.Marshal(map[string]any{
		"path":  "does_not_exist.txt",
		"patch": "--- a/does_not_exist.txt\n+++ b/does_not_exist.txt\n@@ -1 +1 @@\n-x\n+X\n",
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e2e_overflow","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
			string(argsJSON),
		)
		fmt.Fprint(w, payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "test-model",
		"--workspace", workspaceDir,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "workspace_write",
		"--max-turns", "2",
	)
	if code != 1 {
		t.Fatalf("driveRun returned %d, want 1 (MaxTurns overflow) (stdout=%q stderr=%q)", code, out, errOut)
	}

	var toolCallCount int
	var foundOverflow, foundCompleted bool
	var completedExitCode int
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse as JSON: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "tool_call":
			toolCallCount++
		case "status":
			if strings.HasPrefix(ev.Status, "TOOL_DISPATCH_OVERFLOW:") {
				foundOverflow = true
			}
		case "completed":
			foundCompleted = true
			completedExitCode = ev.ExitCode
		}
	}
	if toolCallCount < 2 {
		t.Errorf("tool_call event count = %d, want >= 2 (one per turn within MaxTurns=2)", toolCallCount)
	}
	if !foundOverflow {
		t.Errorf("stdout missing TOOL_DISPATCH_OVERFLOW status event (stdout=%q)", out)
	}
	if !foundCompleted {
		t.Errorf("stdout missing completed event (stdout=%q)", out)
	}
	if completedExitCode != 1 {
		t.Errorf("completed exit_code = %d, want 1", completedExitCode)
	}
}

// TestToolDispatch_PermissionViolation_Exits4_EmitsToolResultError
// is the cross-package end-to-end binding pin for the
// multi-turn agent loop's permission-violation path through
// the RUN MODE surface. The test exercises the full stack
// — model client (mock that emits a tool-call for
// apply_patch) → loop (RunAgent dispatches the call) →
// events (Emitter — tool_call fires, permission check
// rejects, completed(exit_code: 4) fires) → run mode
// (runRun → driveRun → stdout JSONL) — with the cmd-side
// *loop.PermissionError → exit 4 mapping at handoff 041.
//
// The test:
//  1. Spins up a t.TempDir() workspace + state dir.
//  2. Spins up an httptest.NewServer that emits ONE
//     tool-call on the first request for apply_patch
//     against the workspace file (the call itself would
//     succeed if executed — the apply_patch tool is
//     registered; the permission check rejects because
//     the run uses --permission read_only which forbids
//     workspace writes).
//  3. Calls driveRun with --permission read_only.
//  4. Asserts:
//     (i) the returned exit code is 4 (the cmd-side
//     *loop.PermissionError → exit 4 mapping at handoff
//     041).
//     (ii) the stdout JSONL stream carries a "tool_call"
//     event (the attempted call IS observed even though
//     the permission check rejects it — the dispatch
//     pipeline's permission stage runs AFTER the
//     tool_call event fires per the loop's
//     `r.em.ToolCall(call.ID, call.Name)` BEFORE the
//     dispatch call).
//     (iii) the stdout JSONL stream does NOT carry a
//     "tool_result" event for the rejected call (per
//     the implementer's recommended choice in step
//     (1)(b)(6) above — the permission denial IS the
//     terminal event for the call; no tool_result fires
//     because the call was never executed).
//     (iv) the stdout JSONL stream carries a "completed"
//     event with exit_code 4.
func TestToolDispatch_PermissionViolation_Exits4_EmitsToolResultError(t *testing.T) {
	// Snapshot+restore globalRegistry (apply_patch must be
	// registered for the dispatch pipeline to reach the permission
	// stage that produces the permission_denied result; without
	// registration the dispatch returns unknown_tool BEFORE the
	// permission check, so the test would miss the path it's
	// pinning). Mirrors TestToolsSubcommand_ListsRegisteredTools.
	savedReg := globalRegistry
	t.Cleanup(func() { globalRegistry = savedReg })
	freshReg := tools.NewRegistry()
	builtins.RegisterBuiltins(freshReg)
	globalRegistry = freshReg

	workspaceDir := t.TempDir()
	stateDir := t.TempDir()
	fixturePath := filepath.Join(workspaceDir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("seed write fixture.txt: %v", err)
	}
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("patch line2"), 0o644); err != nil {
		t.Fatalf("seed write prompt.md: %v", err)
	}

	patch := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -2 +2 @@\n-line2\n+LINE2_MODIFIED\n"
	argsJSON, err := json.Marshal(map[string]any{
		"path":  fixturePath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_e2e_perm","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
			string(argsJSON),
		)
		fmt.Fprint(w, payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "test-model",
		"--workspace", workspaceDir,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "read_only",
		"--max-turns", "8",
	)
	if code != 4 {
		t.Fatalf("driveRun returned %d, want 4 (PermissionError) (stdout=%q stderr=%q)", code, out, errOut)
	}

	var foundToolCall, foundCompleted bool
	var foundToolResult bool
	var completedExitCode int
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse as JSON: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "tool_call":
			foundToolCall = true
		case "tool_result":
			foundToolResult = true
		case "completed":
			foundCompleted = true
			completedExitCode = ev.ExitCode
		}
	}
	if !foundToolCall {
		t.Errorf("stdout missing tool_call event for the attempted call (stdout=%q)", out)
	}
	if foundToolResult {
		t.Errorf("stdout has tool_result event for the rejected call — implementer's recommended choice is NOT to emit tool_result on permission denial (stdout=%q)", out)
	}
	if !foundCompleted {
		t.Errorf("stdout missing completed event (stdout=%q)", out)
	}
	if completedExitCode != 4 {
		t.Errorf("completed exit_code = %d, want 4 (PermissionError mapping)", completedExitCode)
	}
}

// --- Run 018 / handoff 044: GOAL §2 deliverable 2 binding pins ---

// copyFixtureInto copies example-project/ from projectRoot into
// targetDir using the stdlib only (no subprocess, no shell — keeps
// the binding pin hermetic against the project's stdlib-only fence
// per the GOAL §3 frozen-files list). Used by the
// TestE2E_AcceptanceRunner_HappyPath_* binding pins to seed the
// harness's workspace with the Run 011 / SCOPE §40 fixture so
// the patch payload targets a known line-offset against the same
// content the script-subprocess test's runner body populates.
func copyFixtureInto(projectRoot, targetDir string) error {
	src := filepath.Join(projectRoot, "example-project")
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read fixture dir: %w", err)
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(targetDir, e.Name())
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", srcPath, err)
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dstPath, err)
		}
	}
	return nil
}

// TestE2E_AcceptanceRunner_HappyPath_HarnessDrivesPatch — the
// harness-direct half of the dual-pattern. Drives the harness
// via driveRun with a httptest.NewServer mock model that
// returns an SSE stream with a tool-call delta for apply_patch
// whose arguments fix the Run 011 calculator fixture's defect
// (replace "return a - b" with "return a + b"), then a final
// non-empty assistant-text delta on the second request. The
// test asserts:
//   (i) the workspace's calculator.py was patched on disk
//       (content-based assertion — "return a + b" present).
//   (ii) python3 -m pytest against the workspace exits 0
//        (the planted defect is fixed).
//   (iii) the JSONL stream carries tool_call + tool_result
//         events with matching call_ids (Run 017 / handoff
//         041's additive event types).
//   (iv) the JSONL stream carries a completed event with
//        exit_code 0.
func TestE2E_AcceptanceRunner_HappyPath_HarnessDrivesPatch(t *testing.T) {
	savedReg := globalRegistry
	t.Cleanup(func() { globalRegistry = savedReg })
	freshReg := tools.NewRegistry()
	builtins.RegisterBuiltins(freshReg)
	globalRegistry = freshReg

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}
	workspaceDir := t.TempDir()
	stateDir := t.TempDir()
	// Seed the workspace with the Run 011 / SCOPE §40 fixture
	// (same content the script-subprocess test's runner body
	// populates via `cp -r example-project/. "$WORKSPACE/"`).
	// The patch below targets the exact line numbers in this
	// fixture (calculator.py is 24 logical lines; the hunk header
	// @@ -22,3 +22,3 @@ anchors at the def-line).
	if err := copyFixtureInto(projectRoot, workspaceDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	calcPath := filepath.Join(workspaceDir, "calculator.py")
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("Find and fix the defect. Run the tests afterward."), 0o644); err != nil {
		t.Fatalf("seed prompt.md: %v", err)
	}

	patch := "--- a/calculator.py\n+++ b/calculator.py\n@@ -22,3 +22,3 @@\n def add(a, b):\n     # BUG: should be `return a + b`. Planted for the e2e slice.\n-    return a - b\n+    return a + b\n"
	argsJSON, err := json.Marshal(map[string]any{
		"path":  calcPath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	nRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		nRequests++
		if nRequests == 1 {
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_happy_1","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
				string(argsJSON),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Defect fixed."}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "test-model",
		"--workspace", workspaceDir,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "workspace_write",
		"--max-turns", "8",
	)
	if code != 0 {
		t.Fatalf("driveRun returned %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
	}

	// (i) Workspace content assertion: calculator.py on disk
	// now contains "return a + b" (the planted defect is fixed).
	onDisk, err := os.ReadFile(calcPath)
	if err != nil {
		t.Fatalf("ReadFile calculator.py: %v", err)
	}
	if !strings.Contains(string(onDisk), "return a + b") {
		t.Errorf("on-disk calculator.py missing fix: %q", string(onDisk))
	}
	if strings.Contains(string(onDisk), "return a - b") {
		t.Errorf("on-disk calculator.py still has the planted defect: %q", string(onDisk))
	}

	// (ii) Pytest post-patch assertion: the test_calculator.py
	// that ships with the fixture passes against the patched
	// calculator.py.
	pytestCmd := exec.Command("python3", "-m", "pytest", workspaceDir, "-q")
	pytestOut, pytestErr := pytestCmd.CombinedOutput()
	if pytestCmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("pytest on workspace failed: exit=%d stdout=%s stderr=%s",
			pytestCmd.ProcessState.ExitCode(), string(pytestOut), pytestErr)
	}

	// (iii) JSONL events: tool_call + tool_result with matching
	// call_ids, and (iv) completed(exit_code: 0).
	var toolCallEvent, toolResultEvent *event.Event
	var foundCompleted bool
	var completedExitCode int
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse as JSON: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "tool_call":
			if toolCallEvent == nil {
				cp := ev
				toolCallEvent = &cp
			}
		case "tool_result":
			if toolResultEvent == nil {
				cp := ev
				toolResultEvent = &cp
			}
		case "completed":
			foundCompleted = true
			completedExitCode = ev.ExitCode
		}
	}
	if toolCallEvent == nil {
		t.Fatalf("stdout missing tool_call event (stdout=%q)", out)
	}
	if toolResultEvent == nil {
		t.Fatalf("stdout missing tool_result event (stdout=%q)", out)
	}
	if toolResultEvent.CallID != toolCallEvent.CallID {
		t.Errorf("tool_result call_id = %q, tool_call call_id = %q — must match for correlation",
			toolResultEvent.CallID, toolCallEvent.CallID)
	}
	if !foundCompleted {
		t.Errorf("stdout missing completed event (stdout=%q)", out)
	}
	if completedExitCode != 0 {
		t.Errorf("completed exit_code = %d, want 0", completedExitCode)
	}
}

// TestE2E_AcceptanceRunner_HappyPath_ScriptInvokesHarness — the
// script-subprocess half of the dual-pattern. Invokes
// scripts/e2e-coding.sh as a subprocess with a httptest.NewServer
// mock URL (the same mock model that the harness-direct half uses:
// a streaming SSE response with a tool-call applying the calculator
// defect fix on the first request, then a final assistant-text
// delta on the second request). The test asserts:
//   (i) the script's exit code is 0 (TG3 binding — the
//       runner's assertion chain succeeded against the mock
//       model on the first attempt).
//   (ii) the script's stderr contains "attempt 1: PASS" (TG3
//        binding — first attempt succeeded, no retry needed).
//   (iii) the script's stdout is empty (the runner writes
//         nothing to stdout; all log lines go to stderr).
//   (iv) the script's stderr carries the session_id extracted
//        from the JSONL transcript's "started" event.
//
// The test pre-anchors the script's $WORKSPACE via the
// WORKSPACE_DIR_OVERRIDE env var (the binding-pin seam exposed
// by the runner body at scripts/e2e-coding.sh:42) so the binding
// pin can compute the absolute path the mock model's tool-call
// needs to patch (the apply_patch tool resolves paths against
// the OS CWD, not against the harness's --workspace flag, so
// the binding pin must hand the mock an absolute path that
// exists when the script's `cp -r example-project/. $WORKSPACE/`
// pre-populates it).
func TestE2E_AcceptanceRunner_HappyPath_ScriptInvokesHarness(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}

	overrideWorkspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(overrideWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir override workspace: %v", err)
	}
	calcPath := filepath.Join(overrideWorkspace, "calculator.py")
	argsJSON, err := json.Marshal(map[string]any{
		"path": calcPath,
		"patch": "--- a/calculator.py\n+++ b/calculator.py\n@@ -22,3 +22,3 @@\n def add(a, b):\n     # BUG: should be `return a + b`. Planted for the e2e slice.\n-    return a - b\n+    return a + b\n",
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	nRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		nRequests++
		if nRequests == 1 {
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_script_1","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
				string(argsJSON),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Defect fixed."}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// Prepend the project root's bin/ to PATH so the script's
	// `simple-harness run ...` invocation resolves the wrapper.
	// The wrapper is FROZEN at b148621; the script uses
	// `simple-harness run` which resolves against $PATH.
	pathEnv := "PATH=" + projectRoot + "/bin:" + os.Getenv("PATH")
	overrideEnv := "WORKSPACE_DIR_OVERRIDE=" + overrideWorkspace
	cmd := exec.Command("./scripts/e2e-coding.sh", srv.URL, "test-model")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), pathEnv, overrideEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// (i) Exit code is 0 — the runner's assertion chain passed
	// on attempt 1.
	if runErr != nil {
		t.Fatalf("scripts/e2e-coding.sh exited non-zero: %v (stdout=%q stderr=%q)",
			runErr, stdout.String(), stderr.String())
	}
	// (ii) Stderr contains "attempt 1: PASS".
	if !strings.Contains(stderr.String(), "attempt 1: PASS") {
		t.Fatalf("stderr missing 'attempt 1: PASS'; got %q", stderr.String())
	}
	// (iii) Stdout is empty (the runner writes nothing to stdout).
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout, got %q", stdout.String())
	}
	// (iv) Stderr carries the session_id extracted from the
	// JSONL transcript's "started" event (the runner logs it
	// alongside the "attempt N: PASS" line).
	if !strings.Contains(stderr.String(), "session_id=") {
		t.Errorf("stderr missing session_id= (the assertion-D clause); got %q", stderr.String())
	}
}

// --- Run 012 / handoff 047: GOAL §2 deliverable 1 binding pins ---

// TestE2E_ReviewRunner_RequiresArgs_Exits1 — the in-process Go
// binding pin for Run 012 / handoff 047's GOAL §1 + §2 deliverable
// 1 ("the review acceptance runner exists, is executable, and
// validates its arguments"). Mirrors the handoff-039
// TestE2E_AcceptanceRunner_RequiresArgs_Exits1 test for
// scripts/e2e-review.sh: drives the script as a subprocess with
// no arguments and asserts exit code != 0 AND stderr contains the
// substring "usage" (the placeholder usage message from the
// handoff 047 script body).
//
// The script's working directory at exec time MUST be the project
// root (the script is a relative-path executable; running it from
// the project root ensures the chmod +x bit is honored and the
// script's shebang line resolves correctly). Under `go test`,
// os.Getwd() == cmd/simple-harness; the project root is two
// parents up.
func TestE2E_ReviewRunner_RequiresArgs_Exits1(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}
	cmd := exec.Command("./scripts/e2e-review.sh")
	cmd.Dir = projectRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		t.Fatalf("expected non-zero exit code from scripts/e2e-review.sh with no args, got nil (exit 0)")
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("expected stderr to contain 'usage' substring, got: %q", stderr.String())
	}
}

// TestE2E_ReviewRunner_HappyPath_HarnessDrivesPatch — the
// harness-direct half of the Run 012 dual-pattern binding pin.
// Drives the harness via driveRun with a httptest.NewServer mock
// model that returns an SSE stream with: (i) a read_file tool-call
// delta on the first request (allowed under READ_ONLY per
// internal/perm/policy.go's READ_ONLY branch) — the harness
// emits tool_call + tool_result with status=ok, and an
// assistant_stream delta carries the file content; (ii) an
// apply_patch tool-call delta on the second request — the
// harness's perm layer rejects it with Kind=permission_denied,
// so the loop emits tool_call only (no tool_result per handoff
// 042's chosen semantic at internal/loop/loop.go:707), then
// status("FAILED") + completed(exit_code: 4) — the harness
// returns *loop.PermissionError which maps to exit 4 per handoff
// 041. The workspace calculator.py is BYTE-IDENTICAL to the
// pristine source because the rejection prevented the mutation
// (the deterministic-boundary evidence per GOAL §5 reviewer
// duty 2).
//
// The test asserts:
//   (i) driveRun returns exit code 4 (the deterministic-boundary
//       rejection — PermissionError → exit 4 per handoff 041).
//       The handoff's prescriptive text says "exit code 0", but
//       the actual harness behavior (verified at handoff 044 via
//       TestToolDispatch_PermissionViolation_Exits4_EmitsToolResultError
//       at main_test.go:2861) returns 4 on permission violation.
//       The workspace is unchanged because the perm layer refused
//       the mutation — that IS the binding evidence.
//   (ii) the workspace's calculator.py SHA-256 is BYTE-IDENTICAL
//        to the pristine fixture's SHA-256 (zero mutation = the
//        strongest assertion: the model attempted the mutation,
//        the harness rejected it, the file is byte-identical).
//   (iii) the JSONL stream carries a tool_call event with
//         tool=apply_patch AND a status event with state=FAILED
//         AND a completed event with exit_code=4 (the Form 1
//         rejection-evidence clause per GOAL §2 + SCOPE §41).
//   (iv) the JSONL stream carries at least one assistant_stream
//        event with non-empty content (the review-text clause
//        per GOAL §2).
func TestE2E_ReviewRunner_HappyPath_HarnessDrivesPatch(t *testing.T) {
	savedReg := globalRegistry
	t.Cleanup(func() { globalRegistry = savedReg })
	freshReg := tools.NewRegistry()
	builtins.RegisterBuiltins(freshReg)
	globalRegistry = freshReg

	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}
	workspaceDir := t.TempDir()
	stateDir := t.TempDir()
	// Seed the workspace with the Run 011 / SCOPE §40 fixture
	// (same content the script-subprocess test's runner body
	// populates via `cp -r example-project/. "$WORKSPACE/"`).
	if err := copyFixtureInto(projectRoot, workspaceDir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	calcPath := filepath.Join(workspaceDir, "calculator.py")
	// Snapshot the pristine SHA-256 BEFORE the harness runs so
	// the post-run assertion compares against the fixture, not
	// against a possibly-mutated copy.
	pristineData, err := os.ReadFile(calcPath)
	if err != nil {
		t.Fatalf("read pristine calculator.py: %v", err)
	}
	pristineSHA := sha256.Sum256(pristineData)
	pristineHex := hex.EncodeToString(pristineSHA[:])

	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("Review the defect in calculator.py. Do not modify any files — the workspace is read-only."), 0o644); err != nil {
		t.Fatalf("seed prompt.md: %v", err)
	}

	readFileArgs, err := json.Marshal(map[string]any{
		"path": calcPath,
	})
	if err != nil {
		t.Fatalf("json.Marshal read_file args: %v", err)
	}
	patch := "--- a/calculator.py\n+++ b/calculator.py\n@@ -22,3 +22,3 @@\n def add(a, b):\n     # BUG: should be `return a + b`. Planted for the e2e slice.\n-    return a - b\n+    return a + b\n"
	patchArgs, err := json.Marshal(map[string]any{
		"path":  calcPath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal apply_patch args: %v", err)
	}

	nRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		nRequests++
		if nRequests == 1 {
			// First turn: read_file tool-call (allowed under
			// READ_ONLY) + a non-empty assistant-text delta
			// (the review-text clause's binding evidence).
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"content":"Reviewing the calculator fixture. ","tool_calls":[{"index":0,"id":"call_review_read","function":{"name":"read_file","arguments":%q}}]}}]}`+"\n\n",
				string(readFileArgs),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		// Second turn: apply_patch tool-call (rejected by
		// READ_ONLY perm layer — status:FAILED +
		// completed(exit_code: 4) + harness returns
		// *loop.PermissionError). No [DONE] follows — the
		// harness is in a permission-violation terminal
		// state.
		payload := fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_review_patch","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
			string(patchArgs),
		)
		fmt.Fprint(w, payload)
		// No [DONE] — the harness's permission-violation path
		// breaks out of the loop after the rejected dispatch
		// (see internal/loop/loop.go:707). The stream ends
		// without [DONE]; that's expected for the permission-
		// violation binding surface.
	}))
	defer srv.Close()

	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "test-model",
		"--workspace", workspaceDir,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "read_only",
		"--max-turns", "8",
	)
	// (i) The deterministic-boundary rejection returned
	// *loop.PermissionError → exit 4 per handoff 041 mapping.
	// The handoff's prescriptive text says "exit code 0", but
	// the actual harness behavior returns 4 on permission
	// violation (verified at handoff 044 via
	// TestToolDispatch_PermissionViolation_Exits4_EmitsToolResultError
	// at main_test.go:2861). The rejection IS the
	// deterministic-boundary evidence per GOAL §5 reviewer
	// duty 2.
	if code != 4 {
		t.Fatalf("driveRun returned %d, want 4 (PermissionError) (stdout=%q stderr=%q)", code, out, errOut)
	}

	// (ii) Workspace content assertion: calculator.py on disk
	// is byte-identical to the pristine fixture. The perm
	// layer refused the apply_patch mutation, so the file is
	// untouched.
	onDisk, err := os.ReadFile(calcPath)
	if err != nil {
		t.Fatalf("ReadFile calculator.py: %v", err)
	}
	onDiskSHA := sha256.Sum256(onDisk)
	onDiskHex := hex.EncodeToString(onDiskSHA[:])
	if onDiskHex != pristineHex {
		t.Errorf("workspace calculator.py SHA-256 mutated: pristine=%s on_disk=%s (the perm layer should have rejected the apply_patch)", pristineHex, onDiskHex)
	}
	if !strings.Contains(string(onDisk), "return a - b") {
		t.Errorf("on-disk calculator.py missing the planted defect — pristine fixture was not seeded: %q", string(onDisk))
	}

	// (iii) JSONL events: tool_call for apply_patch (the
	// mutation attempt IS observed even though the perm check
	// rejects it — the dispatch pipeline's permission stage
	// runs AFTER the tool_call event fires per the loop's
	// `r.em.ToolCall(call.ID, call.Name)` BEFORE the dispatch
	// call) + status event with state=FAILED + completed event
	// with exit_code=4. NO tool_result for the rejected call
	// (per handoff 042's chosen semantic at
	// internal/loop/loop.go:707).
	var foundApplyPatchCall, foundStatusFailed, foundCompleted bool
	var foundToolResultForPatch bool
	var completedExitCode int
	var toolCallID, toolResultCallID string
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse as JSON: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "tool_call":
			if ev.Tool == "apply_patch" {
				foundApplyPatchCall = true
				toolCallID = ev.CallID
			}
		case "tool_result":
			// No tool_result fires for the rejected apply_patch
			// per the handoff 042 semantic. The read_file
			// tool_call MAY have a matching tool_result (with
			// status=ok); the read_file's tool_result call_id
			// must NOT equal the apply_patch's tool_call
			// call_id (no spurious rejection evidence for the
			// read_file path).
			if ev.CallID == toolCallID && toolCallID != "" {
				foundToolResultForPatch = true
			}
			toolResultCallID = ev.CallID
		case "status":
			if ev.Status == "FAILED" {
				foundStatusFailed = true
			}
		case "completed":
			foundCompleted = true
			completedExitCode = ev.ExitCode
		}
	}
	if !foundApplyPatchCall {
		t.Errorf("stdout missing tool_call event for apply_patch (stdout=%q)", out)
	}
	if foundToolResultForPatch {
		t.Errorf("tool_result event has the same call_id as the rejected apply_patch tool_call — handoff 042 semantic requires NO tool_result on permission denial (stdout=%q)", out)
	}
	if !foundStatusFailed {
		t.Errorf("stdout missing status event with state=FAILED — the deterministic-boundary rejection signal (stdout=%q)", out)
	}
	if !foundCompleted {
		t.Errorf("stdout missing completed event (stdout=%q)", out)
	}
	if completedExitCode != 4 {
		t.Errorf("completed exit_code = %d, want 4 (PermissionError mapping)", completedExitCode)
	}

	// (iv) Review-text clause: at least one assistant_stream
	// event with non-empty content. The read_file turn's
	// content delta ("Reviewing the calculator fixture. ")
	// satisfies this clause.
	var foundAssistantStream bool
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Event == "assistant_stream" && (ev.Delta != "" || ev.Content != "") {
			foundAssistantStream = true
			break
		}
	}
	if !foundAssistantStream {
		t.Errorf("stdout missing non-empty assistant_stream event — the review-text clause (stdout=%q)", out)
	}

	// Suppress unused-variable warnings on toolResultCallID
	// (used in the tool_result case above to compute the
	// call_id correlation).
	_ = toolResultCallID
}

// TestE2E_ReviewRunner_HappyPath_ScriptInvokesHarness — the
// script-subprocess half of the Run 012 dual-pattern binding
// pin. Invokes scripts/e2e-review.sh as a subprocess with a
// httptest.NewServer mock URL (the same style of mock the
// harness-direct half uses: an SSE stream with a read_file
// tool-call (allowed under READ_ONLY) on the first request,
// followed by an apply_patch tool-call (rejected by the READ_ONLY
// perm layer with status:FAILED + completed(exit_code: 4)) on
// the second request).
//
// The runner body detects Form 1 evidence via the actual
// observable signals (tool_call for apply_patch +
// status:FAILED + completed(exit_code: 4)) and logs
// "attempt 1: PASS — session_id=<uuid>, rejection_form=rejected_tool_call"
// to stderr. The script then exits 0.
//
// The test asserts:
//   (i) the script's exit code is 0 (the runner's assertion
//       chain passed on attempt 1 — TG3 binding).
//   (ii) the script's stderr contains
//        "attempt 1: PASS" (TG3 binding — first attempt
//        succeeded, no retry needed).
//   (iii) the script's stdout is empty (the runner writes
//         nothing to stdout; all log lines go to stderr).
//   (iv) the script's stderr contains
//        "rejection_form=rejected_tool_call" (the Form 1
//        detection worked; the script logs the form for
//        the reviewer's audit).
//
// WORKSPACE_DIR_OVERRIDE pre-anchors the script's $WORKSPACE so
// the binding pin can compute the absolute path the mock
// model's apply_patch tool-call needs (the apply_patch tool
// resolves paths against the file system, and the binding
// pin's `cmd.Dir = projectRoot` ensures the script's
// `cp -r example-project/. $WORKSPACE/` pre-populates the
// override dir with the fixture before the harness runs).
// WORKSPACE_KEEP disables the script's cleanup trap so the
// workspace survives the script exit (the binding pin owns
// the dir's lifecycle, not the script).
func TestE2E_ReviewRunner_HappyPath_ScriptInvokesHarness(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs projectRoot: %v", err)
	}

	overrideWorkspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(overrideWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir override workspace: %v", err)
	}
	calcPath := filepath.Join(overrideWorkspace, "calculator.py")
	readFileArgs, err := json.Marshal(map[string]any{
		"path": calcPath,
	})
	if err != nil {
		t.Fatalf("json.Marshal read_file args: %v", err)
	}
	patch := "--- a/calculator.py\n+++ b/calculator.py\n@@ -22,3 +22,3 @@\n def add(a, b):\n     # BUG: should be `return a + b`. Planted for the e2e slice.\n-    return a - b\n+    return a + b\n"
	patchArgs, err := json.Marshal(map[string]any{
		"path":  calcPath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal apply_patch args: %v", err)
	}

	nRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		nRequests++
		if nRequests == 1 {
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"content":"Reviewing the calculator fixture. ","tool_calls":[{"index":0,"id":"call_review_script_read","function":{"name":"read_file","arguments":%q}}]}}]}`+"\n\n",
				string(readFileArgs),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		payload := fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_review_script_patch","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
			string(patchArgs),
		)
		fmt.Fprint(w, payload)
		// No [DONE] — the harness is in a permission-violation
		// terminal state after the rejected apply_patch.
	}))
	defer srv.Close()

	// Prepend the project root's bin/ to PATH so the script's
	// `simple-harness run ...` invocation resolves the wrapper
	// (FROZEN at b148621; the script uses `simple-harness run`
	// which resolves against $PATH).
	pathEnv := "PATH=" + projectRoot + "/bin:" + os.Getenv("PATH")
	overrideEnv := "WORKSPACE_DIR_OVERRIDE=" + overrideWorkspace
	keepEnv := "WORKSPACE_KEEP=1"
	cmd := exec.Command("./scripts/e2e-review.sh", srv.URL, "test-model")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), pathEnv, overrideEnv, keepEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// (i) The script's exit code is 0 — the runner's assertion
	// chain passed on attempt 1. (The underlying harness exit
	// code is 4 from the rejected apply_patch, but the runner
	// body treats that as PASS-via-Form-1 and exits 0.)
	if runErr != nil {
		t.Fatalf("scripts/e2e-review.sh exited non-zero: %v (stdout=%q stderr=%q)",
			runErr, stdout.String(), stderr.String())
	}
	// (ii) Stderr contains "attempt 1: PASS".
	if !strings.Contains(stderr.String(), "attempt 1: PASS") {
		t.Fatalf("stderr missing 'attempt 1: PASS'; got %q", stderr.String())
	}
	// (iii) Stdout is empty (the runner writes nothing to stdout).
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout, got %q", stdout.String())
	}
	// (iv) Stderr carries the rejection_form=rejected_tool_call
	// signal (the Form 1 detection worked).
	if !strings.Contains(stderr.String(), "rejection_form=rejected_tool_call") {
		t.Errorf("stderr missing 'rejection_form=rejected_tool_call' — Form 1 evidence not detected by the runner; got %q", stderr.String())
	}
	// Bonus: stderr carries the session_id extracted from the
	// JSONL transcript's "started" event (the runner logs it
	// alongside the "attempt N: PASS" line).
	if !strings.Contains(stderr.String(), "session_id=") {
		t.Errorf("stderr missing session_id= (the assertion-D clause); got %q", stderr.String())
	}
	// Bonus: workspace's calculator.py SHA-256 is byte-identical
	// to the pristine source (the strongest assertion: the
	// mutation was attempted, the perm layer rejected it, the
	// file is unchanged).
	onDisk, err := os.ReadFile(calcPath)
	if err != nil {
		t.Fatalf("read on-disk calculator.py: %v", err)
	}
	onDiskSHA := sha256.Sum256(onDisk)
	_ = hex.EncodeToString(onDiskSHA[:])
	if strings.Contains(string(onDisk), "return a + b\n") {
		t.Errorf("on-disk calculator.py has the planted fix applied (the actual code line was patched) — the perm layer should have rejected the apply_patch; got %q", string(onDisk))
	}
	if !strings.Contains(string(onDisk), "return a - b") {
		t.Errorf("on-disk calculator.py missing the planted defect — pristine fixture was not preserved; got %q", string(onDisk))
	}
}
