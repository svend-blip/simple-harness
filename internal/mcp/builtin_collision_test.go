package mcp

import (
	"context"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// builtinStub is a minimal tools.Tool used by the collision tests to
// pre-register a builtin under a known name. The Tool's Execute is
// never invoked in these tests — the collision tests only inspect
// ResolveFinalName's output against the registry's Name set — so the
// Execute body is a panic with a pointer to the test's purpose.
type builtinStub struct{ name string }

func (b *builtinStub) Meta() tools.ToolMeta { return tools.ToolMeta{Name: b.name} }
func (b *builtinStub) Schema() tools.Schema { return tools.Schema{} }
func (b *builtinStub) Execute(_ context.Context, _ tools.Call) (tools.Result, error) {
	panic("builtinStub.Execute is not used in collision tests; the test only inspects ResolveFinalName")
}

// TestResolveFinalName_NoCollision: when the original name is not in
// the registry, ResolveFinalName returns the original name verbatim.
// The MCP tool surfaces under the server-reported name; the builtin
// (if any) is unaffected.
func TestResolveFinalName_NoCollision(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "read_file"})

	got := ResolveFinalName(r, "weather", "forecast")
	if got != "forecast" {
		t.Fatalf("ResolveFinalName = %q, want %q (no collision, original name returned)", got, "forecast")
	}
}

// TestResolveFinalName_BuiltinCollision: when the original name
// collides with a builtin, ResolveFinalName returns
// "<server>__<original>" with double underscore. The builtin stays
// under its own name; the MCP tool is registered under the
// deterministic prefix form.
func TestResolveFinalName_BuiltinCollision(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "read_file"})

	got := ResolveFinalName(r, "weather", "read_file")
	if got != "weather__read_file" {
		t.Fatalf("ResolveFinalName = %q, want %q (builtin collision → server__tool prefix)", got, "weather__read_file")
	}
}

// TestResolveFinalName_ServerNameSanitized: a server name containing
// "__" is sanitized in the prefix. The form stays "<sanitized>__
// <tool>" with the first "__" marking the separator.
func TestResolveFinalName_ServerNameSanitized(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "read_file"})

	got := ResolveFinalName(r, "foo__bar", "read_file")
	if got != "foo_bar__read_file" {
		t.Fatalf("ResolveFinalName = %q, want %q (server name sanitized in prefix)", got, "foo_bar__read_file")
	}
}

// TestResolveFinalName_BuiltinNotRegisteredYet: a name that WOULD
// collide with a builtin registered AFTER ResolveFinalName was called
// is reported as non-colliding at the time of the call. The function
// reflects the registry's state at call time (the snapshot is the
// sorted Names() at the moment of the call).
//
// In practice, AddServer calls ResolveFinalName BEFORE registering
// the adapter, so the adapter's name is NOT in the registry yet —
// the function sees the registry without the MCP tool. This test
// pins that ordering.
func TestResolveFinalName_BuiltinNotRegisteredYet(t *testing.T) {
	r := tools.NewRegistry()
	// "read_file" is NOT registered.

	got := ResolveFinalName(r, "weather", "read_file")
	if got != "read_file" {
		t.Fatalf("ResolveFinalName = %q, want %q (no builtin registered, original name returned)", got, "read_file")
	}
}

// TestResolveFinalName_DeterministicAcrossCalls: ResolveFinalName
// returns the same value on repeated calls with the same inputs.
// Determinism is a property the WORK-4 diagnostics + the
// `simple-harness tools` listing rely on (the resolved name appears
// in the model-facing inventory and must not change within a session).
func TestResolveFinalName_DeterministicAcrossCalls(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "read_file"})

	first := ResolveFinalName(r, "weather", "read_file")
	second := ResolveFinalName(r, "weather", "read_file")
	if first != second {
		t.Fatalf("ResolveFinalName is not deterministic: first=%q second=%q", first, second)
	}
	if first != "weather__read_file" {
		t.Fatalf("ResolveFinalName = %q, want %q", first, "weather__read_file")
	}
}

// TestRegistryHasName: a focused unit test for the lookup helper.
// registryHasName returns true iff the registry contains the given
// name. The function is unexported; the test is in-package.
func TestRegistryHasName(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&builtinStub{name: "alpha"})
	r.Register(&builtinStub{name: "beta"})

	if !registryHasName(r, "alpha") {
		t.Fatalf("registryHasName(alpha) = false, want true")
	}
	if !registryHasName(r, "beta") {
		t.Fatalf("registryHasName(beta) = false, want true")
	}
	if registryHasName(r, "gamma") {
		t.Fatalf("registryHasName(gamma) = true, want false")
	}
}