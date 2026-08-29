// Package mcp owns the MCP (Model Context Protocol) client for Simple
// Harness. It surfaces configuration-pinned MCP servers' tools through
// the existing tools.Registry, respecting the same schema → path → policy
// → execution pipeline (SCOPE §13). MCP-provided tools are
// indistinguishable from builtins at the execution boundary: they carry
// the same JSON-schema-lite schema, participate in the same authorize
// pipeline, and produce the same structured Result.
//
// Per Out-scope §11 (replacement; SCOPE §1892-1900): no dynamic MCP
// server discovery, no server-initiated model requests (sampling), no
// server-initiated file access (roots). The client surface stays
// narrow on purpose.
//
// Per SCOPE §43 (In-scope §43; SCOPE §1652-1692) + Run 019 GOAL §2
// bound decisions 1-7: the tool listing is fetched once at session
// start and immutable per session; a server declared but unreachable
// at session start produces a structured startup error (not a silent
// omission); a transport failure during a call is a structured tool
// failure the model sees, never a harness crash; name collisions with
// builtins are resolved deterministically (<server>__<tool>, builtin
// wins); allowlist enforcement is at registration time (a tool not on
// the allowlist is never registered, hence never callable).
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy. It imports only the Go
// standard library and the local internal/tools package. The permission
// seam (AuthorizeFunc) is passed in by the caller — typically
// perm.Authorize — so the MCP client does not introduce a second
// permission pipeline (per the Run 019 reviewer duty #1: no second
// door around perm.Policy).
//
// The client-core handoff (Run 019 WORK slot 1, handoff 056) ships the
// package skeleton, the public types, the collision-naming rule, the
// registration flow against the existing tools.Registry, and the unit-
// level TestMCP_ pins. The Transport implementations (http streamable-
// http + stdio) land in WORK 2 (handoff 057); the config surface lands
// in WORK 3 (handoff 058); the cmd-side wiring + the HARNESS-CONTRACT
// additive subsection + the Version literal advance + the runtime
// binary rebuild land in WORK 4 (handoff 059).
package mcp

import (
	"context"
	"sync"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Server is a configuration-pinned MCP server declaration (SCOPE §43,
// SCOPE §1652-1692). Server declarations come from configuration under
// the `mcp_servers` key; this struct is the resolved shape after
// configuration parsing. The config-key work lands in WORK 3 (handoff
// 058) — this handoff ships the package surface the config layer will
// populate.
//
// Name is the stable identifier; ResolveFinalName uses it as the prefix
// for the MCP-tool registration name on builtin-collision. Transport
// is "stdio" | "http". Endpoint is the http URL for transport=http or
// the command string for transport=stdio. Permission is the permission
// mode the server's tools map into (e.g., "read_only"); the
// authoritative mapping is the run-time Policy construction in WORK 4
// (cmd-side wiring). Allowlist is the optional subset of server-
// offered tools; empty means "all listed tools are registered".
type Server struct {
	Name       string   // stable identifier
	Transport  string   // "stdio" | "http"
	Endpoint   string   // http URL for transport=http; command string for transport=stdio
	Command    []string // command + args for transport=stdio (empty for http)
	Permission string   // permission mode the server's tools map into (e.g., "read_only")
	Allowlist  []string // optional subset of server-offered tools; empty = all
}

// ListedTool is a tool the server reports at session-start listing.
// InputSchema is the server's verbatim JSON Schema (decoded as a
// generic map[string]interface{} — the MCP wire format is JSON); the
// client converts it to a tools.Schema at registration time via
// schemaFromMap in registry.go.
//
// The session-start listing is fetched ONCE per SCOPE §43 and Run 019
// GOAL §2 bound decision 3; the resolved set is immutable for the
// session. The Manager.AddServer caller fetches the listing and
// iterates the result.
type ListedTool struct {
	Name        string                 // tool name as the server reports it
	Description string                 // human-readable
	InputSchema map[string]interface{} // JSON Schema (verbatim from server listing)
}

// ToolRegistration carries the resolved data needed to register one
// MCP-provided tool into the existing tools.Registry. The client-core
// builds these inside Manager.AddServer; the actual registry.Register
// call wraps each ToolRegistration in an mcpAdapter (registry.go).
//
// Schema is the resolved tools.Schema (JSON-schema-lite shape); the
// OriginalName is the server-reported name (preserved for the
// transport.Call dispatch); the FinalName is the registration name
// after collision resolution (e.g., "weather__read_file"). Server is
// the parent server (kept on the registration for diagnostics + future
// transport routing at dispatch time).
type ToolRegistration struct {
	ServerName   string
	OriginalName string       // name as the server reported it
	FinalName    string       // name after collision resolution (e.g., "weather__read_file")
	Schema       tools.Schema // resolved JSON-schema-lite schema
	Server       Server       // parent server (for permission mapping + transport lookup)
	Description  string       // copied from ListedTool
}

// Transport is the pluggable seam the WORK-2 slot (handoff 057) wires
// to MCP streamable-http + stdio. THIS HANDOFF ships only the
// interface; the production implementations land in WORK 2. A test-
// only stub (stubTransport) lives in transport_stub_test.go and is
// imported only by the unit tests.
//
// List returns the server's tool listing (SCOPE §43 + GOAL §2 bound
// decision 3: fetched once at session start, immutable per session).
// Call dispatches a single tool call to the MCP server (the result is
// the server's verbatim JSON-shaped map; the MCP client treats the
// shape as opaque Content). Close releases any resources the
// transport holds (sockets, child processes, etc.) — Manager.Close
// invokes Close on every registered transport at session shutdown.
//
// Per GOAL §2 bound decision 4: a List or Call error is a structured
// error (returned through the Go error interface), never a panic or
// silent omission. The client surfaces List errors at session start
// (as a startup error from AddServer) and Call errors at dispatch
// time (as a ToolError{Kind:"execution_failed"}).
type Transport interface {
	// List returns the server's tool listing (server reports its
	// available tools at session start; the resolved set is immutable
	// per session per SCOPE §43).
	List(ctx context.Context) ([]ListedTool, error)
	// Call dispatches a single tool call to the MCP server.
	Call(ctx context.Context, name string, args map[string]interface{}) (map[string]interface{}, error)
	// Close releases any resources the transport holds.
	Close() error
}

// serverState holds per-server runtime state: the Server declaration,
// the Transport handle (so Manager.Close can release it), and the
// resolved tool FinalNames registered into the registry (kept for
// future diagnostics — `context show`/`context doctor` will surface
// these per SCOPE §43 + Run 019 GOAL §2 bound decision 3 in WORK 4).
type serverState struct {
	server    Server
	transport Transport
	tools     []string // FinalName values registered against the registry
}

// Manager orchestrates one or more MCP servers. The Manager owns the
// Transport handles (Close releases them all) and the registration of
// each server's listed tools into the shared tools.Registry.
//
// Concurrency: the Manager's mu protects the servers map. Per-server
// state is set once at AddServer time (which is single-threaded —
// session-start wiring in main.go) and read at Close time; the registry
// is itself concurrent-safe (tools.Registry takes a write-lock on
// Register and an RW-lock read-side on Names/Get/Dispatch).
type Manager struct {
	mu       sync.Mutex
	servers  map[string]*serverState
	registry *tools.Registry
	auth     tools.AuthorizeFunc
	policy   tools.Policy
	ws       tools.Workspace
}

// NewManager constructs a Manager that registers MCP tools into the
// given registry.
//
// auth is the seam that runs the SCOPE §13 pipeline at MCP-tool
// dispatch time; the caller wires perm.Authorize (or any
// AuthorizeFunc-shaped function) at startup. The MCP client does NOT
// implement its own permission check — it passes through the caller-
// supplied function (no second door around perm.Policy per the Run 019
// GOAL §2 bound decision 5 + reviewer duty #1).
//
// policy and ws are passed to auth at every adapter.Execute call. The
// zero value of tools.Policy is the nil interface; callers should wire
// a real Policy (typically perm.NewPolicy(mode)) to avoid nil-pointer
// panics inside auth. The zero value of tools.Workspace is safe (the
// path stage in perm.Authorize Normalizes against the workspace root;
// an empty root leaves most relative paths unchanged).
//
// NewManager does NOT register any tools — the caller invokes
// Manager.AddServer for each configured MCP server (typically in
// session-start wiring). The current signature is the implementer's
// divergence from the decomposer's suggestion (which named the second
// parameter "policy" but typed it as AuthorizeFunc); the divergence
// reason is documented in the handoff 056 result file ("Files Changed"
// section).
func NewManager(registry *tools.Registry, auth tools.AuthorizeFunc, policy tools.Policy, ws tools.Workspace) *Manager {
	return &Manager{
		servers:  map[string]*serverState{},
		registry: registry,
		auth:     auth,
		policy:   policy,
		ws:       ws,
	}
}

// AddServer registers the given server's tools against the shared
// tools.Registry. The full flow lives in registry.go (next to the
// mcpAdapter type that wraps each ListedTool); this method's contract
// is documented here.
//
// The caller (cmd/simple-harness/main.go, WORK 4) drives AddServer
// during session start. Errors from transport.List propagate verbatim
// — a server declared but unreachable at session start is a
// structured startup error, not a silent omission (per SCOPE §43 +
// Run 019 GOAL §2 bound decision 4). The exit-2 handling lives at
// the caller.