package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestMCP_TransportFailure_StructuredToolResult is handoff 058's
// pin for SCOPE §43 + amendment §43 + GOAL §2 bound decision 4:
//
//	"Transport failures during a tool call are structured tool
//	failures (the model sees them), never harness crashes."
//
// The test wires a stub transport that reports one tool on List
// then returns a "connection reset" error from Call. After
// Manager.AddServer registers the adapter, the test invokes
// adapter.Execute directly (white-box against the dispatch path)
// and asserts the returned Result is the structured tool failure
// shape (Status="error", Kind="execution_failed", Message
// contains the wrapped "connection reset" string). The harness
// must NOT crash — the structured error is the model's
// signal-and-continue surface.
//
// The pin parallels the existing TestMCP_PermissionMapping
// pattern (in registry_integration_test.go) — both are
// white-box exercise of the adapter's structured-error wire.
// WORK 4 (handoff 059) wraps this wire in a
// TestMCP_EndToEnd_Happy that drives a real Call through
// tools.Registry.Dispatch.
func TestMCP_TransportFailure_StructuredToolResult(t *testing.T) {
	r := tools.NewRegistry()
	m := NewManager(r, noopAuth, tools.Policy(nil), tools.Workspace{})

	srv := Server{Name: "weather", Transport: "stdio", Command: []string{"stub"}}
	listing := []ListedTool{
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
	}
	transport := newStubTransport(listing)
	transport.nextCallErr = errors.New("connection reset")

	if err := m.AddServer(context.Background(), srv, transport); err != nil {
		t.Fatalf("AddServer error = %v, want nil", err)
	}

	tool, ok := r.Get("tool_alpha")
	if !ok || tool == nil {
		t.Fatalf("r.Get(tool_alpha) = (nil, %v), want (non-nil, true)", ok)
	}

	call := tools.Call{
		Name:      "tool_alpha",
		Arguments: map[string]interface{}{"path": "/tmp/x"},
	}
	res, err := tool.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("adapter.Execute returned non-nil error = %v, want nil (transport failure must be a structured Result, not a Go error)", err)
	}
	if res.Status != "error" {
		t.Fatalf("res.Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("res.Error = nil, want non-nil (transport failure must be a structured ToolError)")
	}
	if res.Error.Kind != "execution_failed" {
		t.Fatalf("res.Error.Kind = %q, want %q", res.Error.Kind, "execution_failed")
	}
	if !strings.Contains(res.Error.Message, "connection reset") {
		t.Fatalf("res.Error.Message = %q, want message containing %q (transport error must be wrapped, not swallowed)", res.Error.Message, "connection reset")
	}
	if res.Error.Call.Name != "tool_alpha" {
		t.Fatalf("res.Error.Call.Name = %q, want %q (offending call preserved for the model's audit trail)", res.Error.Call.Name, "tool_alpha")
	}

	// Sanity: the stub recorded the failed call — the test
	// confirms the wire actually invoked the transport (not a
	// short-circuit return path).
	if len(transport.calls) != 1 {
		t.Fatalf("transport.calls len = %d, want 1 (the adapter should have invoked the transport exactly once)", len(transport.calls))
	}
	if transport.calls[0].Name != "tool_alpha" {
		t.Fatalf("transport.calls[0].Name = %q, want %q", transport.calls[0].Name, "tool_alpha")
	}
}
