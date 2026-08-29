package mcp

import (
	"context"
	"fmt"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// AddServer registers the given server's tools against the shared
// tools.Registry. The flow:
//
//  1. transport.List(ctx) → []ListedTool.
//  2. For each ListedTool:
//     - Apply the allowlist filter (drop any tool whose name is not in
//       Server.Allowlist when Allowlist is non-empty; the filter is at
//       REGISTRATION time, so an excluded tool is never callable).
//     - Convert InputSchema (the server's verbatim JSON Schema as a
//       map[string]interface{}) → tools.Schema via schemaFromMap.
//     - Resolve the final name via ResolveFinalName (builtin wins;
//       MCP tool is registered under "<server>__<tool>" on collision;
//       double underscore; server name sanitized).
//     - Build the mcpAdapter and register it against the registry.
//  3. Record the serverState (Server + Transport + the resolved
//     FinalNames) so Manager.Close can release the Transport.
//
// Errors:
//   - transport.List error → propagated verbatim. Per GOAL §2 bound
//     decision 4, a server declared but unreachable at session start
//     is a structured startup error, not a silent omission. The
//     exit-2 handling lives at the caller (cmd/simple-harness/main.go,
//     WORK 4).
//   - schemaFromMap error → propagated. A server whose listing
//     includes a malformed schema is a structured startup error.
//
// Successful AddServer returns nil; partial success is not a thing —
// either every listed tool that survives the allowlist is registered,
// or AddServer returns the first error and the manager has no
// serverState for this server (the registry may have some tools
// registered; a caller that wants to undo a failed AddServer can
// construct a fresh registry; in practice, a malformed listing is a
// session-start configuration error and the caller aborts).
func (m *Manager) AddServer(ctx context.Context, srv Server, transport Transport) error {
	listing, err := transport.List(ctx)
	if err != nil {
		return fmt.Errorf("mcp: server %q listing failed: %w", srv.Name, err)
	}

	state := &serverState{
		server:    srv,
		transport: transport,
		tools:     make([]string, 0, len(listing)),
	}

	for _, lt := range listing {
		if !allowlisted(srv.Allowlist, lt.Name) {
			continue
		}

		schema, err := schemaFromMap(lt.InputSchema)
		if err != nil {
			return fmt.Errorf("mcp: server %q tool %q: schema invalid: %w",
				srv.Name, lt.Name, err)
		}

		finalName := ResolveFinalName(m.registry, srv.Name, lt.Name)
		adapter := newAdapter(adapterConfig{
			Registry:     m.registry,
			Auth:         m.auth,
			Policy:       m.policy,
			Workspace:    m.ws,
			Server:       srv,
			OriginalName: lt.Name,
			FinalName:    finalName,
			Description:  lt.Description,
			Schema:       schema,
			Transport:    transport,
		})
		m.registry.Register(adapter)
		state.tools = append(state.tools, finalName)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[srv.Name] = state
	return nil
}

// Close releases every server's Transport. Called at session shutdown
// (the wiring is WORK 4's job — main.go drives it). The function is
// idempotent: Close on an already-closed Transport should be a no-op
// per the Transport contract; the Manager does not enforce this and
// does not retry Close on failure.
//
// Transport close errors are silently dropped: by the time Close is
// called the session is shutting down, and the per-transport error
// has nowhere useful to go (the model has already stopped emitting
// tool calls). A future Run that wants shutdown-error reporting can
// collect these into an error slice.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, st := range m.servers {
		_ = st.transport.Close()
	}
}

// allowlisted returns true iff the tool name is in the allowlist, or
// the allowlist is empty (= "all listed tools are registered"). The
// check is case-sensitive and exact-match — MCP server tool names are
// case-sensitive on the wire and we mirror the server's spelling.
//
// The filter is at REGISTRATION time (this function), not at DISPATCH
// time. A tool not on the allowlist is never registered, hence never
// callable from the model. Per SCOPE §43: "No tool is callable that
// the allowlist excludes."
func allowlisted(allowlist []string, name string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, n := range allowlist {
		if n == name {
			return true
		}
	}
	return false
}

// schemaFromMap converts an MCP server's verbatim JSON Schema (a
// map[string]interface{}) into a tools.Schema. The conversion handles
// the three fields MCP tools commonly use:
//
//   - "required" (JSON array of strings) → tools.Schema.Required.
//   - "properties" (JSON object whose values are {"type": "<json>"} or
//     similar) → tools.Schema.Properties (each value's "type" becomes
//     a tools.PropertyType; values without a recognized "type" are
//     silently dropped from the properties map).
//   - "additionalProperties" (JSON boolean) → tools.Schema.AdditionalProperties.
//     The object form (JSON Schema's "additionalProperties": { ... })
//     is not represented in tools.Schema's JSON-schema-lite shape; the
//     conversion treats any non-boolean as false (the strict default
//     that rejects unknown fields).
//
// A nil input returns the zero-value tools.Schema. An unrecognized
// shape (e.g. "required" is not an array) returns an error; the
// caller (AddServer) surfaces it as a structured startup error.
//
// Property-type mapping (jsonType → tools.PropertyType):
//
//   - "string" → TypeString
//   - "integer" / "int" → TypeInt (JSON Schema's "integer" is the
//     stricter int-only variant; "int" is the synonym some servers use)
//   - "number" → TypeNumber
//   - "boolean" / "bool" → TypeBool
//   - "array" → TypeArray
//   - "object" → TypeObject
//   - anything else → silently dropped (the property is not added to
//     the schema map; the validator will reject unknown fields if the
//     tool tries to use them).
//
// The conversion is intentionally lenient: MCP servers in the wild
// return a wide variety of schema shapes, and the JSON-schema-lite
// validator only consumes a small subset. Strict validation happens
// at validate time (tools.Validate) — the converter just maps the
// subset it recognizes.
func schemaFromMap(in map[string]interface{}) (tools.Schema, error) {
	out := tools.Schema{}
	if in == nil {
		return out, nil
	}

	if reqRaw, ok := in["required"]; ok {
		reqSlice, ok := reqRaw.([]interface{})
		if !ok {
			return out, fmt.Errorf("required must be a JSON array of strings")
		}
		for _, r := range reqSlice {
			s, ok := r.(string)
			if !ok {
				return out, fmt.Errorf("required entries must be strings")
			}
			out.Required = append(out.Required, s)
		}
	}

	if addRaw, ok := in["additionalProperties"]; ok {
		switch v := addRaw.(type) {
		case bool:
			out.AdditionalProperties = v
		default:
			// Object form (JSON Schema's "additionalProperties": { ... })
			// is not represented in tools.Schema. Treat it as false
			// (the strict default) so unknown fields are rejected.
			out.AdditionalProperties = false
		}
	}

	if propsRaw, ok := in["properties"]; ok {
		propsMap, ok := propsRaw.(map[string]interface{})
		if !ok {
			return out, fmt.Errorf("properties must be a JSON object")
		}
		if len(propsMap) > 0 {
			out.Properties = make(map[string]tools.PropertyType, len(propsMap))
		}
		for name, propRaw := range propsMap {
			propMap, ok := propRaw.(map[string]interface{})
			if !ok {
				// Non-object property definition (e.g., a bare
				// string in shorthand). Skip silently.
				continue
			}
			tRaw, ok := propMap["type"]
			if !ok {
				// No "type" field. Skip silently — the
				// validator would reject this anyway at
				// use time.
				continue
			}
			tStr, ok := tRaw.(string)
			if !ok {
				continue
			}
			pt := jsonTypeToPropertyType(tStr)
			if pt == "" {
				continue
			}
			out.Properties[name] = pt
		}
	}

	return out, nil
}

// jsonTypeToPropertyType maps a JSON Schema "type" string to a
// tools.PropertyType. Returns "" for unrecognized types (the caller
// in schemaFromMap skips the property).
//
// Recognized: "string", "integer", "int", "number", "boolean", "bool",
// "array", "object". The "integer"/"int" and "boolean"/"bool" aliases
// cover the JSON Schema strict-variant names + the shorthand some MCP
// servers use.
func jsonTypeToPropertyType(t string) tools.PropertyType {
	switch t {
	case "string":
		return tools.TypeString
	case "integer", "int":
		return tools.TypeInt
	case "number":
		return tools.TypeNumber
	case "boolean", "bool":
		return tools.TypeBool
	case "array":
		return tools.TypeArray
	case "object":
		return tools.TypeObject
	}
	return ""
}

// adapterConfig carries the resolved data needed to construct one MCP
// adapter (one per ListedTool that survives the allowlist filter).
// Bundling the configuration in a struct keeps newAdapter's signature
// stable as the adapter grows new fields across the WORK 2/3/4 slots.
type adapterConfig struct {
	Registry     *tools.Registry
	Auth         tools.AuthorizeFunc
	Policy       tools.Policy
	Workspace    tools.Workspace
	Server       Server
	OriginalName string
	FinalName    string
	Description  string
	Schema       tools.Schema
	Transport    Transport
}

// mcpAdapter is a tools.Tool implementation that wraps a single MCP
// tool. The adapter is registered against the shared tools.Registry
// inside Manager.AddServer; the registry's Dispatch path runs the
// same schema → path → policy → execution pipeline for the adapter
// as it does for builtins (because the adapter is a tools.Tool, and
// Dispatch treats every Tool the same).
//
// The adapter's Execute method ALSO runs the authorize step (calling
// the caller-supplied AuthorizeFunc). This is intentional: the
// adapter is self-contained — it can be invoked directly (e.g. from
// the unit test TestMCP_PermissionMapping_PassesThroughAuthorize) OR
// via registry.Dispatch. When invoked via registry.Dispatch, the
// registry runs auth first (idempotent pass), then calls Execute
// (which runs auth again — same result on the same call). The
// "no second door around perm.Policy" requirement is satisfied
// because the MCP integration uses the same AuthorizeFunc the
// builtins use, with no parallel permission pipeline.
//
// Transport-call errors are returned as Result{Status:"error",
// Error:&ToolError{Kind:"execution_failed", ...}} — same shape as a
// builtin execution failure. Per GOAL §2 bound decision 4: "Transport
// failures during a tool call are structured tool failures (the model
// sees them), never harness crashes."
type mcpAdapter struct {
	server    Server
	origName  string
	meta      tools.ToolMeta
	schema    tools.Schema
	transport Transport
	auth      tools.AuthorizeFunc
	policy    tools.Policy
	ws        tools.Workspace
}

// Compile-time assertion that mcpAdapter satisfies tools.Tool.
var _ tools.Tool = (*mcpAdapter)(nil)

// newAdapter constructs an mcpAdapter from the resolved
// adapterConfig. The FinalName becomes the adapter's registration
// name; the OriginalName is preserved for transport.Call dispatch
// (the MCP server sees the original tool name, not the collision
// prefix).
func newAdapter(cfg adapterConfig) *mcpAdapter {
	return &mcpAdapter{
		server:    cfg.Server,
		origName:  cfg.OriginalName,
		meta:      tools.ToolMeta{Name: cfg.FinalName, Description: cfg.Description},
		schema:    cfg.Schema,
		transport: cfg.Transport,
		auth:      cfg.Auth,
		policy:    cfg.Policy,
		ws:        cfg.Workspace,
	}
}

// Meta implements tools.Tool.
func (a *mcpAdapter) Meta() tools.ToolMeta { return a.meta }

// Schema implements tools.Tool. Returns the resolved JSON-schema-lite
// shape (the MCP server's verbatim schema, converted via schemaFromMap).
func (a *mcpAdapter) Schema() tools.Schema { return a.schema }

// Execute implements tools.Tool. The dispatch order:
//
//  1. Call the caller-supplied AuthorizeFunc. The function runs schema
//     → path → policy and returns nil on pass or a *tools.DecisionError
//     on the first failure.
//  2. On *tools.DecisionError, return Result{Status:"error", Error:
//     &tools.ToolError{Kind:<mapped>, Message:de.Error(), Call:
//     de.Call}}. The mapping (stage → kind) is identical to
//     tools.mapStageToKind so the model's view of an MCP-tool failure
//     is indistinguishable from a builtin-tool failure.
//  3. On authorize-pass, call transport.Call. A transport error
//     becomes Result{Status:"error", Error:&tools.ToolError{Kind:
//     "execution_failed", ...}} — same shape as a builtin execution
//     failure. The model sees this structured error; the harness does
//     NOT crash (per GOAL §2 bound decision 4).
//  4. On transport success, return Result{Status:"ok", Content: <map>}
//     where <map> is the transport.Call result verbatim. MCP server
//     results are arbitrary JSON-shaped maps; the tools layer treats
//     them as opaque content (the downstream consumer parses the map).
//
// The adapter passes a.ws and a.policy to the AuthorizeFunc — these
// are the values the Manager was constructed with (typically
// perm.NewPolicy(mode) and the active Workspace). The Policy and
// Workspace are stable across all calls (they're set at Manager
// construction); per-call variation lives in the call itself.
func (a *mcpAdapter) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	if de := a.auth(ctx, call, a.schema, a.ws, a.policy); de != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    stageToKind(de.Stage, de.Reason),
			Message: de.Error(),
			Call:    de.Call,
		}}, nil
	}
	out, err := a.transport.Call(ctx, a.origName, call.Arguments)
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("mcp: server %q tool %q: %v", a.server.Name, a.origName, err),
			Call:    call,
		}}, nil
	}
	return tools.Result{Status: "ok", Content: out}, nil
}

// stageToKind maps an internal DecisionError's (Stage, Reason) to the
// external ToolError.Kind. Mirrors tools.mapStageToKind (which is
// unexported). The mapping is the single source of truth for how
// internal pipeline failures become external structured errors; the
// adapter duplicates the mapping here because the function is not
// exported from the tools package.
//
// Keeping the mapping in sync with tools.mapStageToKind is the
// implementer's responsibility. The values match the registry.Dispatch
// pipeline's mapping so the model's view of an MCP-tool failure is
// indistinguishable from a builtin-tool failure:
//
//   - stage "schema" → "schema_violation"
//   - stage "path" → "path_escape"
//   - stage "policy" → "permission_denied"
//   - anything else → "internal_error"
//
// A future Run that wants to expose tools.mapStageToKind as an
// exported function can remove this duplicate.
func stageToKind(stage, reason string) string {
	switch stage {
	case "schema":
		return "schema_violation"
	case "path":
		return "path_escape"
	case "policy":
		return "permission_denied"
	}
	return "internal_error"
}