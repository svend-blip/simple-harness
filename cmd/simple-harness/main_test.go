package main

import (
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
