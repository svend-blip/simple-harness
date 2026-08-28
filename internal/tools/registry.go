package tools

import (
	"context"
	"sort"
	"sync"
)

// Registry holds the registered tools. It is safe for concurrent reads
// after construction; Register takes a write-lock so concurrent
// registration is also safe.
//
// Run 003's cmd entry point registers all tools at startup, before any
// concurrent dispatch, so the simpler "register once at startup" usage
// is sufficient. Run 004+ may extend Register to accept more sophisticated
// registration paths; the public surface stays the same.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool. Overwriting an existing tool is a programming
// error and panics — double-registration at startup is a bug the panic
// surfaces immediately rather than silently masking.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Meta().Name
	if _, exists := r.tools[name]; exists {
		panic("tools.Registry: duplicate registration of " + name)
	}
	r.tools[name] = t
}

// Get returns the tool with the given name, or (nil, false) if no such
// tool is registered.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the sorted list of registered tool names. Used by the
// `simple-harness tools` subcommand.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Dispatch runs the SCOPE §13 pipeline on the call:
//
//  1. Get the tool by name (rejects unknown).
//  2. Run the authorize function (schema + path + policy).
//  3. Run tool.Execute (only if authorize returned nil).
//
// On any pipeline failure, Dispatch returns a Result with Status="error"
// and a structured ToolError. The error's Kind field is one of:
// "unknown_tool" (step 1), "schema_violation" / "path_escape" /
// "permission_denied" (step 2, mapped from DecisionError via
// mapStageToKind), or "execution_failed" (step 3 — Execute returned a
// non-nil error).
//
// The pipeline order matches docs/ARCHITECTURE.md §"Permission boundary
// placement" §"Enforcement placement" verbatim: schema validation →
// path normalization → permission policy → execution. The order is
// defined inside the authorize function the caller passes in (perm's
// Authorize today; main.go wires it).
//
// The auth parameter is the seam: passing an AuthorizeFunc rather than
// importing perm.Authorize breaks the would-be Go import cycle between
// tools (which needs the validator) and perm (which also needs the
// validator AND is the natural home of the pipeline). main.go wires
// perm.Authorize at startup.
func (r *Registry) Dispatch(ctx context.Context, call Call, ws Workspace, pol Policy, auth AuthorizeFunc) Result {
	t, ok := r.Get(call.Name)
	if !ok {
		return Result{Status: "error", Error: &ToolError{
			Kind:    "unknown_tool",
			Message: "no tool named " + call.Name,
			Call:    call,
		}}
	}
	if de := auth(ctx, call, t.Schema(), ws, pol); de != nil {
		return Result{Status: "error", Error: &ToolError{
			Kind:    mapStageToKind(de.Stage, de.Reason),
			Message: de.Error(),
			Call:    de.Call,
		}}
	}
	res, err := t.Execute(ctx, call)
	if err != nil {
		return Result{Status: "error", Error: &ToolError{
			Kind:    "execution_failed",
			Message: err.Error(),
			Call:    call,
		}}
	}
	return res
}

// mapStageToKind converts a DecisionError's (Stage, Reason) to the
// ToolError.Kind that the external Result surfaces. The mapping is the
// single source of truth for how internal pipeline failures become
// external structured errors.
func mapStageToKind(stage, reason string) string {
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