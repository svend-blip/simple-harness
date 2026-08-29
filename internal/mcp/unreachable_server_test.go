package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestMCP_UnreachableServer_StartupErrorExit2 is handoff 058's pin
// for SCOPE §43 + GOAL §2 bound decision 4:
//
//	"Declared-but-unreachable at session start = structured
//	startup error, exit 2 (never a silent omission)."
//
// The test wires a stub transport that returns
// "connection refused" from List. Manager.AddServer wraps the
// transport error with the server name (per the existing wrap at
// internal/mcp/registry.go:46) and returns it. The test asserts the
// wire-shape the cmd-side wiring in WORK 4 (handoff 059) will
// match against to drive the exit-2 path:
//
//	mcp: server %q listing failed: <err>
//
// The "Exit2" suffix in the test name acknowledges that the
// cmd-side is OUT OF SCOPE for this handoff — the test pins the
// wire so WORK 4's exit-2 mapping is reliable. The cmd-side wiring
// at WORK 4 will check for this error form via `strings.HasPrefix`
// or a sentinel-error type to drive the process exit. The
// end-to-end `bin/simple-harness run` exit-2 verification is the
// TG2 testgoal at WORK 4 close.
//
// The harness must NOT crash — the structured error is the
// session-start signal-and-fail surface. No tools are registered
// against the registry when a server's listing fails (the test
// pins that).
func TestMCP_UnreachableServer_StartupErrorExit2(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport(nil)
	transport.nextListErr = errors.New("connection refused")

	err := m.AddServer(context.Background(), srv, transport)
	if err == nil {
		t.Fatalf("AddServer = nil, want error (declared server but unreachable at session start = structured startup error)")
	}

	// Wire-shape pin: the exact wrap form from
	// internal/mcp/registry.go:46 is "mcp: server %q listing
	// failed: %w". The cmd-side mapping at WORK 4 matches on
	// this form. Drift in the wrap string is a fence violation.
	const wantPrefix = `mcp: server "weather" listing failed:`
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Fatalf("AddServer error = %q, want prefix %q (wire-shape pin for cmd-side exit-2 mapping at WORK 4)", err.Error(), wantPrefix)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("AddServer error = %q, want wrapped underlying error containing %q", err.Error(), "connection refused")
	}

	// Partial-success guard: no tools registered against the
	// registry when the server's listing fails. Per the
	// Manager.AddServer contract, partial success is not a thing
	// — either every listed tool that survives the allowlist is
	// registered, or AddServer returns the first error and the
	// manager has no serverState for this server. The test pins
	// that side-effect (a list-failure AddServer must not register
	// zero of N tools and call it a success).
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("registry.Names() = %v, want empty (no tools should be registered on listing failure)", names)
	}
}
