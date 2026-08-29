package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svend-blip/simple-harness/internal/config"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// cmdMcpInit happy-path pin. Sets up a stub HTTP MCP server via
// httptest (JSON-RPC 2.0 wire shape); builds a config.Config with
// one MCPServers entry pointing at the stub; calls cmdMcpInit;
// verifies:
//
//   - the returned count equals the number of tools the stub
//     reported (deterministic — the stub seeds 2 tools);
//   - the registry contains the resolved tool names (under the
//     bare form, since no builtin collides);
//   - no error returned.
//
// This is the WORK 4 cmd-side wiring happy path: the WORK 1
// client-core + WORK 2 transports + WORK 3 config surface are
// wired together at session start. The TEST pin is the binding
// anchor for the cmd-side of the RUN-MODE tg2 + INTERACTIVE-MODE
// identity-card-banner flow.
func TestMCP_Init_Success_RegistersTools(t *testing.T) {
	// Stub HTTP server: 2 tools in the listing.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
			`{"name":"alpha","description":"alpha tool","inputSchema":{"properties":{"path":{"type":"string"}}}}` +
			`,{"name":"beta","description":"beta tool","inputSchema":{}}` +
			`]}}`))
	})
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:      "stub",
				Transport: "http",
				Endpoint:  httpSrv.URL,
			},
		},
	}

	registry := tools.NewRegistry()
	ws := tools.Workspace{}
	mgr, count, err := cmdMcpInit(context.Background(), cfg, perm.READ_ONLY, registry, ws)
	if err != nil {
		t.Fatalf("cmdMcpInit error = %v, want nil", err)
	}
	if mgr == nil {
		t.Fatalf("cmdMcpInit returned nil manager (want non-nil so callers can defer Close())")
	}
	defer mgr.Close()
	if count != 2 {
		t.Fatalf("cmdMcpInit returned count = %d, want 2 (stub seeded 2 tools)", count)
	}

	names := registry.Names()
	if len(names) != 2 {
		t.Fatalf("registry.Names() len = %d, want 2 (names=%v)", len(names), names)
	}
	for _, want := range []string{"alpha", "beta"} {
		if _, ok := registry.Get(want); !ok {
			t.Fatalf("registry missing %q after cmdMcpInit (names=%v)", want, names)
		}
	}
}

// cmdMcpInit unreachable-server pin: a server is declared in the
// config but its HTTP endpoint points at a closed port. The
// TRANSPORT's tools/list roundtrip returns a wrapped error;
// Manager.AddServer wraps it with the wire form
// "mcp: server %q listing failed: %w". cmdMcpInit returns the
// wrapped error verbatim; the caller's exit-2 mapping is the next
// step (pinning TG2 in main.go's run() function).
//
// The pin verifies:
//   - a non-nil error is returned (no silent omission);
//   - the error's prefix matches "mcp: server \"<name>\"
//     listing failed:" (the wire-shape anchor for the cmd-side
//     exit-2 mapping);
//   - the registry is empty (no partial success — even an
//     unreachable server with a partial registry must surface as
//     the structured startup error, not silently wire any tools).
func TestMCP_Init_Unreachable_Exit2(t *testing.T) {
	cfg := &config.Config{
		MCPServers: []config.MCPServerConfig{
			{
				Name:      "tg19-063",
				Transport: "http",
				Endpoint:  "http://127.0.0.1:9/mcp", // closed port (no service bound)
			},
		},
	}

	registry := tools.NewRegistry()
	ws := tools.Workspace{}
	_, count, err := cmdMcpInit(context.Background(), cfg, perm.READ_ONLY, registry, ws)
	if err == nil {
		t.Fatalf("cmdMcpInit err = nil, want error (declared-but-unreachable = structured startup error)")
	}
	if count != 0 {
		t.Fatalf("cmdMcpInit count = %d, want 0 (no tools should be registered on listing failure)", count)
	}
	const wantPrefix = `mcp: server "tg19-063" listing failed:`
	if !hasPrefix(err.Error(), wantPrefix) {
		t.Fatalf("cmdMcpInit err = %q, want prefix %q (wire-shape pin for cmd-side exit-2 mapping)", err.Error(), wantPrefix)
	}
	if names := registry.Names(); len(names) != 0 {
		t.Fatalf("registry.Names() = %v, want empty (no partial success on listing failure)", names)
	}
}

// Empty MCPServers slice path: cmdMcpInit returns (0, nil) when
// no servers are declared. The pin guards the no-op path — a
// future Run that adds a startup-side-effect for the empty case
// (e.g., emitting an event) must not regress this contract.
func TestMCP_Init_EmptyMCPServers_NoOp(t *testing.T) {
	cfg := &config.Config{}
	registry := tools.NewRegistry()
	ws := tools.Workspace{}
	mgr, count, err := cmdMcpInit(context.Background(), cfg, perm.READ_ONLY, registry, ws)
	if err != nil {
		t.Fatalf("cmdMcpInit err = %v, want nil (empty MCPServers is the no-op path)", err)
	}
	if count != 0 {
		t.Fatalf("cmdMcpInit count = %d, want 0", count)
	}
	if mgr != nil {
		mgr.Close()
		t.Fatalf("cmdMcpInit returned non-nil manager; empty MCPServers path should return nil manager")
	}
}

// hasPrefix is a tiny substring-prefix helper for the cmd-side
// test file (kept local so the package's other test files do not
// need to share helpers across files).
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
