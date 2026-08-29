package mcp

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// noopAuth is an AuthorizeFunc that always returns nil. Used by the
// happy-path registration tests; the tests do not exercise Execute,
// so the auth function is never actually called.
var noopAuth tools.AuthorizeFunc = func(_ context.Context, _ tools.Call, _ tools.Schema, _ tools.Workspace, _ tools.Policy) *tools.DecisionError {
	return nil
}

// testToolsInListing is the canned listing used by
// TestMCP_BuildToolListing_Happy and TestMCP_AllowlistEnforced. The
// JSON-Schema-shaped InputSchema mirrors what an MCP server reports
// at session start; the test asserts the conversion + registration
// produces the expected tools.Schema on the registered adapter.
func testToolsInListing() []ListedTool {
	return []ListedTool{
		{
			Name:        "tool_alpha",
			Description: "alpha tool",
			InputSchema: map[string]interface{}{
				"required": []interface{}{"path"},
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        "tool_beta",
			Description: "beta tool",
			InputSchema: map[string]interface{}{
				"properties": map[string]interface{}{
					"count": map[string]interface{}{"type": "integer"},
				},
			},
		},
		{
			Name:        "tool_gamma",
			Description: "gamma tool",
			InputSchema: map[string]interface{}{
				"additionalProperties": true,
			},
		},
	}
}

// TestMCP_BuildToolListing_Happy: Manager.AddServer with a stub
// transport that reports 3 tools. Verify the registry has those 3
// tools registered (the names appear in registry.Names(); the schema
// on each adapter is the converted tools.Schema).
//
// This is the WORK-1 client-core contribution to TG3 (≥ 4 TestMCP_
// pins pass under `go test -run TestMCP_`). WORK 2 + 3 + 4 add the
// transport-level, config-level, and end-to-end pins; TG3's `≥ 4`
// bar is met at WORK 4 close.
func TestMCP_BuildToolListing_Happy(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport(testToolsInListing())

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	got := r.Names()
	want := []string{"tool_alpha", "tool_beta", "tool_gamma"}
	if !stringSlicesEqualUnordered(got, want) {
		t.Fatalf("registry.Names() = %v, want %v (same set, order-agnostic)", got, want)
	}

	// Each registered tool exposes the expected schema (the
	// conversion via schemaFromMap pins the contract). We pull the
	// adapter via r.Get and check its Schema() against the expected
	// shape.
	for name, wantSchema := range map[string]tools.Schema{
		"tool_alpha": {
			Required: []string{"path"},
			Properties: map[string]tools.PropertyType{
				"path": tools.TypeString,
			},
		},
		"tool_beta": {
			Properties: map[string]tools.PropertyType{
				"count": tools.TypeInt,
			},
		},
		"tool_gamma": {
			AdditionalProperties: true,
		},
	} {
		tool, ok := r.Get(name)
		if !ok || tool == nil {
			t.Fatalf("r.Get(%q) = (nil, %v), want (non-nil, true)", name, ok)
		}
		if !schemasEqual(tool.Schema(), wantSchema) {
			t.Fatalf("r.Get(%q).Schema() = %+v, want %+v", name, tool.Schema(), wantSchema)
		}
		if tool.Meta().Name != name {
			t.Fatalf("r.Get(%q).Meta().Name = %q, want %q", name, tool.Meta().Name, name)
		}
	}
}

// TestMCP_BuildToolListing_CollisionNaming: pre-register a builtin
// named "read_file" in the registry; add an MCP server whose listing
// reports a tool named "read_file"; verify the builtin stays
// registered under "read_file" AND the MCP tool is registered under
// "weather__read_file" (the deterministic <server>__<tool> form).
//
// The collision rule is binding per GOAL §2 bound decision 5: the
// builtin wins; the MCP tool is surfaced under the documented
// prefix; no silent shadowing in either direction.
func TestMCP_BuildToolListing_CollisionNaming(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "read_file"})

	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	listing := []ListedTool{
		{
			Name:        "read_file",
			Description: "MCP read_file",
			InputSchema: map[string]interface{}{},
		},
	}
	transport := newStubTransport(listing)

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	// Builtin stays under its own name.
	builtin, ok := r.Get("read_file")
	if !ok || builtin == nil {
		t.Fatalf("r.Get(read_file) = (nil, %v), want (non-nil, true) — builtin disappeared", ok)
	}
	if builtin.Meta().Name != "read_file" {
		t.Fatalf("builtin Meta().Name = %q, want %q", builtin.Meta().Name, "read_file")
	}

	// MCP tool registered under <server>__<tool>.
	mcpTool, ok := r.Get("weather__read_file")
	if !ok || mcpTool == nil {
		t.Fatalf("r.Get(weather__read_file) = (nil, %v), want (non-nil, true) — MCP tool missing", ok)
	}
	if mcpTool.Meta().Name != "weather__read_file" {
		t.Fatalf("MCP tool Meta().Name = %q, want %q", mcpTool.Meta().Name, "weather__read_file")
	}

	// ResolveFinalName returns the deterministic form when called
	// directly with the same inputs.
	if got := ResolveFinalName(r, "weather", "read_file"); got != "weather__read_file" {
		t.Fatalf("ResolveFinalName(weather, read_file) = %q, want %q", got, "weather__read_file")
	}
}

// TestMCP_AllowlistEnforced: add an MCP server with an allowlist
// that names two of the server's three listed tools; verify the third
// tool is NOT registered. The filter is at REGISTRATION time — a
// tool not on the allowlist is never registered, hence never
// callable (per SCOPE §43: "No tool is callable that the allowlist
// excludes").
func TestMCP_AllowlistEnforced(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{
		Name:      "weather",
		Transport: "stdio",
		Command:   []string{"stub"},
		Allowlist: []string{"tool_alpha", "tool_beta"},
	}
	transport := newStubTransport(testToolsInListing())

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	got := r.Names()
	want := []string{"tool_alpha", "tool_beta"}
	if !stringSlicesEqualUnordered(got, want) {
		t.Fatalf("registry.Names() = %v, want %v (tool_gamma excluded by allowlist)", got, want)
	}

	// tool_gamma is explicitly absent.
	if _, ok := r.Get("tool_gamma"); ok {
		t.Fatalf("r.Get(tool_gamma) = (non-nil, true), want (nil, false) — excluded by allowlist but registered")
	}
}

// TestAddServer_PropagatesListError: transport.List returning an
// error is propagated verbatim from AddServer. Per GOAL §2 bound
// decision 4: a server declared but unreachable at session start is
// a structured startup error, not a silent omission. The error
// carries the server name for diagnostics (the cmd-side wiring in
// WORK 4 converts it to exit 2).
func TestAddServer_PropagatesListError(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	transport := newStubTransport(nil)
	transport.nextListErr = errStubUnreachable

	err := m.AddServer(context.Background(), srv, transport)
	if err == nil {
		t.Fatalf("AddServer = nil, want error (transport.List failed)")
	}
	if !contains(err.Error(), "weather") {
		t.Fatalf("AddServer error = %q, want error mentioning server name", err.Error())
	}

	// No tools registered.
	if names := r.Names(); len(names) != 0 {
		t.Fatalf("registry.Names() = %v, want empty (no tools should be registered on listing failure)", names)
	}
}

// TestAddServer_EmptyAllowlistRegistersAll: when the Allowlist is
// empty (the default), every listed tool is registered. The empty
// allowlist means "all listed tools are registered" — the opposite
// of TestMCP_AllowlistEnforced's restrictive allowlist case.
func TestAddServer_EmptyAllowlistRegistersAll(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	// Allowlist explicitly empty (the zero-value default is also
	// empty, but this makes the contract obvious).
	srv := Server{
		Name:      "weather",
		Transport: "stdio",
		Command:   []string{"stub"},
		// Allowlist: nil  // empty = all tools registered
	}
	transport := newStubTransport(testToolsInListing())

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	got := r.Names()
	want := []string{"tool_alpha", "tool_beta", "tool_gamma"}
	if !stringSlicesEqualUnordered(got, want) {
		t.Fatalf("registry.Names() = %v, want %v (empty allowlist → all 3 tools registered)", got, want)
	}
}

// errStubUnreachable is the canned error the stub transport returns
// when nextListErr is set. The message is non-empty so tests can
// assert on error wrapping.
var errStubUnreachable = &stubError{msg: "stub: connection refused"}

// stubError is a minimal error type for tests.
type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }

// stringSlicesEqualUnordered returns true iff a and b contain the
// same elements regardless of order. registry.Names returns sorted
// output, but tests should not depend on the order — the comparison
// is order-agnostic.
func stringSlicesEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// contains is a tiny helper for substring assertions. Used in lieu of
// strings.Contains to keep the test file's imports minimal (the test
// file is package mcp, not the tools package).
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// schemasEqual reports whether two tools.Schema values are equal. The
// comparison is field-by-field; for Properties, the comparison is
// map-equality (every key in a.Properties is in b.Properties with the
// same PropertyType value).
//
// The function is test-local; the package's production code does not
// need a schema equality helper.
func schemasEqual(a, b tools.Schema) bool {
	if !stringSlicesEqualUnordered(a.Required, b.Required) {
		return false
	}
	if a.AdditionalProperties != b.AdditionalProperties {
		return false
	}
	if len(a.Properties) != len(b.Properties) {
		return false
	}
	for k, v := range a.Properties {
		if bv, ok := b.Properties[k]; !ok || bv != v {
			return false
		}
	}
	return true
}