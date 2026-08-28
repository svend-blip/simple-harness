package main

import (
	"io"
	"os"
	"strings"
	"testing"
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

// TestNoArgsRejected pins the no-argument default: exit non-zero. The
// handoff lets the implementer pick either "exit 0 with usage" or "exit 1
// with usage"; we picked exit 1 (consistent with the unknown-flag
// behaviour and with "no subcommand yet" being a misuse of the binary).
func TestNoArgsRejected(t *testing.T) {
	code := run([]string{})
	if code != 1 {
		t.Fatalf("run() returned %d, want 1 (no subcommand yet)", code)
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
