package mcp

import (
	"context"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestApplyPositionDefaultsFillsOnlyDeclaredAndAbsentArguments pins
// the full fill rule for the pure function: declared + absent →
// filled; declared + model-supplied value → untouched; declared + empty
// string → filled; undeclared → never added; unset environment
// variable → never added; and the input map is not mutated.
func TestApplyPositionDefaultsFillsOnlyDeclaredAndAbsentArguments(t *testing.T) {
	env := func(k string) string {
		return map[string]string{
			"SIMPLE_HARNESS_RUN_ID":     "run-7",
			"SIMPLE_HARNESS_HANDOFF_ID": "hand-7",
			"SIMPLE_HARNESS_FLOW_KEY":   "flow-7",
			"SOMETHING_ELSE":            "",
		}[k]
	}
	schema := tools.Schema{Properties: map[string]tools.PropertyType{
		"query":      tools.TypeString,
		"run_id":     tools.TypeString,
		"handoff_id": tools.TypeString,
		// flow_key deliberately NOT declared: a property the
		// tool does not declare must never be added even
		// though SIMPLE_HARNESS_FLOW_KEY is set.
	}}

	args := map[string]any{
		"query":      "q",
		"handoff_id": "from-model",
	}
	out := applyPositionDefaults(schema, args, env)

	if got := out["run_id"]; got != "run-7" {
		t.Fatalf("declared+absent: run_id = %v, want %q", got, "run-7")
	}
	if got := out["handoff_id"]; got != "from-model" {
		t.Fatalf("model-supplied value must win: handoff_id = %v, want %q", got, "from-model")
	}
	if _, ok := out["flow_key"]; ok {
		t.Fatalf("undeclared property flow_key must not be added; got %v", out["flow_key"])
	}
	// The input map is never mutated: args sees none of the fills.
	if _, ok := args["run_id"]; ok {
		t.Fatalf("input map was mutated: args gained run_id = %v", args["run_id"])
	}
	if len(args) != 2 {
		t.Fatalf("input map was mutated: len(args) = %d, want 2", len(args))
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3 (query + handoff_id + filled run_id)", len(out))
	}

	// Declared + present-but-empty-string entry → filled.
	empty := map[string]any{"run_id": ""}
	outEmpty := applyPositionDefaults(schema, empty, env)
	if got := outEmpty["run_id"]; got != "run-7" {
		t.Fatalf("declared+empty-string: run_id = %v, want %q", got, "run-7")
	}

	// Non-empty environment variable required: with every variable
	// unset (empty lookup), nothing is added — not even for declared
	// properties.
	noEnv := func(string) string { return "" }
	outNoEnv := applyPositionDefaults(schema, map[string]any{"query": "q"}, noEnv)
	if _, ok := outNoEnv["run_id"]; ok {
		t.Fatalf("unset variable must not add run_id; got %v", outNoEnv["run_id"])
	}

	// nil args with a declared property and a set variable yields a
	// new map carrying the fills.
	outNil := applyPositionDefaults(schema, nil, env)
	if outNil == nil {
		t.Fatalf("nil args with declared property + set variable must yield a new map, got nil")
	}
	if outNil["run_id"] != "run-7" || outNil["handoff_id"] != "hand-7" {
		t.Fatalf("nil args fills = %v, want run_id %q and handoff_id %q", outNil, "run-7", "hand-7")
	}
}

// TestAdapterExecuteSendsTheFilledArguments pins the adapter wiring:
// what the transport receives is call.Arguments plus the position
// defaults from the environment. With SIMPLE_HARNESS_RUN_ID set and a
// schema that declares run_id, the recorded transport call carries the
// filled value; with the variable unset, it does not.
func TestAdapterExecuteSendsTheFilledArguments(t *testing.T) {
	stub := newStubTransport(nil)
	schema := tools.Schema{Properties: map[string]tools.PropertyType{
		"query":  tools.TypeString,
		"run_id": tools.TypeString,
	}}
	adapter := newAdapter(adapterConfig{
		Server:       Server{Name: "knowledge"},
		OriginalName: "search",
		FinalName:    "knowledge__search",
		Schema:       schema,
		Transport:    stub,
	})

	t.Setenv("SIMPLE_HARNESS_RUN_ID", "run-42")
	res, err := adapter.Execute(context.Background(), tools.Call{
		Name:      "knowledge__search",
		Arguments: map[string]any{"query": "q"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Result.Status = %q, want %q", res.Status, "ok")
	}
	if len(stub.calls) != 1 {
		t.Fatalf("stub recorded %d calls, want 1", len(stub.calls))
	}
	if got := stub.calls[0].Args["run_id"]; got != "run-42" {
		t.Fatalf("transport saw run_id = %v, want %q", got, "run-42")
	}
	if got := stub.calls[0].Args["query"]; got != "q" {
		t.Fatalf("transport saw query = %v, want %q", got, "q")
	}

	// Unset variable (empty value counts as unset): the transport
	// must see exactly what the model sent, no fill.
	t.Setenv("SIMPLE_HARNESS_RUN_ID", "")
	if _, err := adapter.Execute(context.Background(), tools.Call{
		Name:      "knowledge__search",
		Arguments: map[string]any{"query": "q"},
	}); err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("stub recorded %d calls, want 2", len(stub.calls))
	}
	if _, ok := stub.calls[1].Args["run_id"]; ok {
		t.Fatalf("unset variable must not leak into transport args; got %v", stub.calls[1].Args["run_id"])
	}
}

// echoTool is a minimal builtin-style tools.Tool whose Execute echoes
// the arguments it received. It lets the test observe the arguments
// the registry pipeline hands to a non-MCP tool.
type echoTool struct{}

func (echoTool) Meta() tools.ToolMeta { return tools.ToolMeta{Name: "echo"} }

func (echoTool) Schema() tools.Schema {
	return tools.Schema{Properties: map[string]tools.PropertyType{
		"query":  tools.TypeString,
		"run_id": tools.TypeString,
	}}
}

func (echoTool) Execute(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Status: "ok", Content: call.Arguments}, nil
}

// TestBuiltinToolsAreUnaffectedByPositionDefaults pins the boundary:
// the position defaults live in the MCP adapter only. A tool reached
// through tools.Registry.Dispatch with the same environment set and a
// schema that declares run_id still receives exactly what the model
// sent — the fill does not leak onto the builtin path.
func TestBuiltinToolsAreUnaffectedByPositionDefaults(t *testing.T) {
	t.Setenv("SIMPLE_HARNESS_RUN_ID", "run-9")

	registry := tools.NewRegistry()
	registry.Register(echoTool{})

	allow := func(context.Context, tools.Call, tools.Schema, tools.Workspace, tools.Policy) *tools.DecisionError {
		return nil
	}
	res := registry.Dispatch(context.Background(), tools.Call{
		Name:      "echo",
		Arguments: map[string]any{"query": "q"},
	}, tools.Workspace{}, nil, allow)

	if res.Status != "ok" {
		t.Fatalf("Dispatch failed: %+v", res.Error)
	}
	echoed, ok := res.Content.(map[string]any)
	if !ok {
		t.Fatalf("Content = %T, want map[string]any", res.Content)
	}
	if _, ok := echoed["run_id"]; ok {
		t.Fatalf("builtin path must not receive position fills; echoed run_id = %v", echoed["run_id"])
	}
	if len(echoed) != 1 || echoed["query"] != "q" {
		t.Fatalf("echoed args = %v, want exactly {query: q}", echoed)
	}
}
