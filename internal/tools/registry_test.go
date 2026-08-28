package tools

import (
	"context"
	"strings"
	"testing"
)

// TestRegistry_StartEmpty: NewRegistry().Names() returns an empty slice.
func TestRegistry_StartEmpty(t *testing.T) {
	r := NewRegistry()
	names := r.Names()
	if len(names) != 0 {
		t.Fatalf("NewRegistry().Names() = %v, want empty", names)
	}
}

// TestRegistry_RegisterAndList: register two tools, Names() returns
// the sorted list.
func TestRegistry_RegisterAndList(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "zulu"})
	r.Register(&echoTool{name: "alpha"})

	names := r.Names()
	want := []string{"alpha", "zulu"}
	if !stringSlicesEqual(names, want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
}

// TestRegistry_DuplicateRegisterPanics: Register twice with the same
// name panics.
func TestRegistry_DuplicateRegisterPanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "dup"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("Register of duplicate did not panic")
		}
	}()
	r.Register(&echoTool{name: "dup"})
}

// TestRegistry_GetRegisteredAndUnknown: Get returns (tool, true) for a
// registered name and (nil, false) for an unknown name.
func TestRegistry_GetRegisteredAndUnknown(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "echo"})

	if t1, ok := r.Get("echo"); !ok || t1 == nil {
		t.Fatalf("Get(\"echo\") = (%v, %v), want (non-nil, true)", t1, ok)
	}
	if t1, ok := r.Get("no_such_tool"); ok || t1 != nil {
		t.Fatalf("Get(\"no_such_tool\") = (%v, %v), want (nil, false)", t1, ok)
	}
}

// echoTool is a minimal Tool used for registry tests. It accepts any
// call (empty schema) and returns Status="ok" with Content=Arguments.
type echoTool struct{ name string }

func (e *echoTool) Meta() ToolMeta              { return ToolMeta{Name: e.name} }
func (e *echoTool) Schema() Schema              { return Schema{} }
func (e *echoTool) Execute(_ context.Context, call Call) (Result, error) {
	return Result{Status: "ok", Content: call.Arguments}, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guardAgainstFutureShape is a no-op kept to mark the file as
// intentionally covering both Registry and Schema in the same _test.go
// file (the handoff splits registry_test.go and schema_test.go but
// allows them to coexist; we put registry tests here for clarity).
var guardAgainstFutureShape = func() bool { return strings.HasPrefix("a", "a") }()