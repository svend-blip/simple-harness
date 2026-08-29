// mcp_init.go is the cmd-side wiring helper for the MCP client
// (Run 019 WORK slot 4, handoff 063). The helper plumbs the
// configuration-pinned MCP server declarations (resolved by
// internal/config/ at handoff 058's Git-gate close) into the live
// tools.Registry the model loop dispatches against.
//
// The helper is the binding seam between four layers:
//
//   - config.Config.MCPServers: the declared servers
//     (name, transport, endpoint, command, permission, allowlist,
//     api_key, headers).
//
//   - mcp.Manager: the client-core facade (Run 019 WORK 1,
//     handoff 056) that owns the server states and registers
//     adapters into the registry.
//
//   - mcp.NewHTTPTransport / mcp.NewStdioTransport: the production
//     transports (Run 019 WORK 2, handoff 057).
//
//   - tools.Registry: the shared registry the loop dispatches tool
//     calls against. MCP tools live alongside builtins here; the
//     registry's Dispatch path is the single source of truth for
//     the authorize pipeline (no second door around perm.Policy).
//
// Per GOAL §2 bound decision 4: a server declared but unreachable
// at session start is a structured startup error, never a silent
// omission. cmdMcpInit maps the manager's wrap-form error to a
// (count, error) tuple; the caller in main.go converts the error
// to `os.Exit(2)` + a one-line stderr message naming the server.
//
// Per GOAL §2 bound decision 5: name collisions with builtins are
// resolved deterministically — the builtin wins; the MCP tool is
// surfaced under `<server>__<tool>`. The collision logic lives in
// internal/mcp/builtin_collision.go (ResolveFinalName) and is wired
// into Manager.AddServer at WORK 1 — this file does NOT re-implement
// the rule.
//
// Architectural boundary: this is a Simple Harness component. It
// does not import orchestration, harness selection, GPU/VRAM
// allocation, model lifecycle, or Model Allocator policy. It
// imports only the Go standard library plus the local internal/
// packages (config, mcp, perm, tools).
package main

import (
	"context"
	"fmt"

	"github.com/svend-blip/simple-harness/internal/config"
	"github.com/svend-blip/simple-harness/internal/mcp"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// cmdMcpInit wires every configuration-pinned MCP server into the
// live registry at session start. The function returns:
//
//   - a *mcp.Manager whose Close() releases the transports when
//     the session ends (the caller defers the close);
//   - the cumulative count of MCP tools registered (the per-server
//     diff between registry.Names() before and after AddServer);
//   - a wrapped error when ANY server's listing fails.
//
// The error format mirrors the WORK 1 wrap at
// internal/mcp/registry.go:46 ("mcp: server %q listing failed:
// %w"); the caller in run() (this file's runtime call site) maps
// it to the canonical one-line stderr:
//
//	simple-harness: mcp server "<name>" unreachable: <reason>
//
// and exits with code 2 (SCOPE §28 configuration error) — never
// a silent omission.
//
// On success: zero tools is a legitimate outcome (no MCP servers
// configured, or allowlists that filter every listing). The
// returned count is the sum across all servers of the tools that
// survived the allowlist filter. The count is intended for the
// interactive-mode identity-card banner; the headless `run` mode
// ignores it.
//
// The auth and policy wiring matches handoff 056's pattern
// (perm.Authorize is the canonical SCOPE §13 pipeline; the
// Manager takes it as the AuthorizeFunc seam). The workspace is
// passed through for path-stage normalization at execute time (the
// MCP adapter's authorization runs against the manager's ws).
//
// Concurrency: cmdMcpInit is a single-threaded session-start
// helper. It calls Manager.AddServer once per declared server
// sequentially; each AddServer call mutates the registry's
// internal state under the registry's write-lock. Concurrent
// callers would be a fence violation; this function is called
// only from the single-threaded run() boot path.
//
// The returned manager is the cleanup seam: the caller should
// `defer manager.Close()` after a successful call to release the
// transports (stdio child-process reaping per Run 005's
// process-group discipline; http idle-connection release). A
// session that exits without closing the manager leaks stdio
// children — the OS reaps them when the harness process exits
// (children with no parent), but the controlled SIGTERM/SIGKILL
// escalation at SCOPE §27 is the canonical cleanup path.
func cmdMcpInit(ctx context.Context, cfg *config.Config, mode perm.Mode, registry *tools.Registry, ws tools.Workspace) (*mcp.Manager, int, error) {
	if cfg == nil {
		return nil, 0, nil
	}
	if len(cfg.MCPServers) == 0 {
		// No servers declared — nothing to wire. This is the
		// common V1 path (most operators don't pin MCP servers
		// on day 1). Returning a nil manager is legitimate;
		// the caller's `if mgr != nil { defer mgr.Close() }`
		// is the canonical cleanup pattern.
		return nil, 0, nil
	}

	policy := perm.NewPolicy(mode)
	manager := mcp.NewManager(registry, perm.Authorize, policy, ws)

	totalRegistered := 0
	for _, srvCfg := range cfg.MCPServers {
		// Convert the config-layer shape (config.MCPServerConfig)
		// to the client-core shape (mcp.Server). The two structs
		// have overlapping fields by design (handoff 058's
		// pointer-overlay pattern + handoff 056's Server struct);
		// the conversion is the explicit boundary between the
		// config layer and the MCP client layer. The config layer
		// does NOT import internal/mcp/ (decoupling per SCOPE
		// §29's "small predictable configuration hierarchy" +
		// the no-new-abstractions principle), so the shape
		// duplication is intentional.
		mcpSrv := mcp.Server{
			Name:       srvCfg.Name,
			Transport:  srvCfg.Transport,
			Endpoint:   srvCfg.Endpoint,
			Command:    srvCfg.Command,
			Permission: srvCfg.Permission,
			Allowlist:  srvCfg.Allowlist,
		}

		// Per-server transport factory. httpTransport is created
		// directly; stdioTransport requires a child-process spawn
		// (the cmd is non-empty by config validation at
		// internal/config/config.go:392-400). The spawn-error
		// path returns the wrapped error here; AddServer
		// surfaces a separate listing error if the spawn
		// succeeded but the first tools/list roundtrip failed.
		var transport mcp.Transport
		switch srvCfg.Transport {
		case "http":
			transport = mcp.NewHTTPTransport(srvCfg.Endpoint)
		case "stdio":
			var err error
			transport, err = mcp.NewStdioTransport(ctx, srvCfg.Command)
			if err != nil {
				manager.Close()
				return nil, totalRegistered, fmt.Errorf("mcp: server %q: %w", srvCfg.Name, err)
			}
		default:
			// Config validation rejects anything other than
			// "http" | "stdio" (internal/config/config.go:384),
			// but a defensive guard here means a future config
			// surface that bypasses validation does not
			// silently wire a broken transport.
			manager.Close()
			return nil, totalRegistered, fmt.Errorf("mcp: server %q: unsupported transport %q", srvCfg.Name, srvCfg.Transport)
		}

		// AddServer fetches the listing, applies the allowlist
		// filter, registers adapters into the registry, and
		// records the serverState. A listing error propagates
		// verbatim with the wire form
		//
		//   mcp: server %q listing failed: <err>
		//
		// which is the cmd-side exit-2 mapping's anchor
		// (internal/mcp/unreachable_server_test.go's
		// wire-shape pin). On listing failure, release the
		// transports wired so far (they are about to be leaked
		// anyway since the harness will exit 2).
		before := len(registry.Names())
		if err := manager.AddServer(ctx, mcpSrv, transport); err != nil {
			manager.Close()
			return nil, totalRegistered, err
		}
		totalRegistered += len(registry.Names()) - before
	}
	return manager, totalRegistered, nil
}
