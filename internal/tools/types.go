// Package tools owns the tool surface for Simple Harness: the public
// types (Call, Result, ToolError, Schema, ToolMeta), the JSON-schema-lite
// validator (Validate), the tool registry (Registry), and the pipeline
// integration (Dispatch). The package is the runtime half of SCOPE §13:
//
//	schema validation  (Validate)
//	      ↓
//	path normalization (the authorize function the caller passes to Dispatch)
//	      ↓
//	permission policy  (Policy.Decide)
//	      ↓
//	execution          (Tool.Execute)
//
// The schema validator is JSON-schema-lite, NOT full JSON Schema: it
// carries Required, Properties (typed), and AdditionalProperties (default
// false). No $ref, no oneOf, no patternProperties. Run 014 and Run 015
// declare their tools' schemas against this validator; the validator does
// not grow in Run 003.
//
// The Registry is safe for concurrent reads after construction; Register
// takes a write-lock so concurrent registration is also safe.
//
// The Policy interface, Decision, and DecisionError live in this package
// because Dispatch takes a Policy as a parameter and an AuthorizeFunc
// that returns *DecisionError; keeping these types here means the perm
// package can implement Permissive against tools.Policy without
// importing tools' concrete dispatch surface. The seam between this
// package and perm is the AuthorizeFunc type — main.go wires perm.Authorize
// into Dispatch at startup. See the "Seam choices" subsection in the
// handoff 013 result file for the rationale.
//
// Architectural boundary: this is a Simple Harness component. It does not
// import orchestration, harness selection, GPU/VRAM allocation, model
// lifecycle, or Model Allocator policy. It imports only the Go standard
// library and the local internal/path package.
package tools

import (
	"context"

	"github.com/svend-blip/simple-harness/internal/path"
)

// Workspace re-exports path.Workspace. The alias keeps the
// AuthorizeFunc signature short (tools.Workspace instead of
// path.Workspace) and matches the handoff's "internal/perm re-exports
// path.Workspace" pattern, generalized to the tools package.
type Workspace = path.Workspace

// Call is a single tool invocation from the model. The shape is the
// wire format the model emits (and the dispatcher parses); tool
// implementations read Arguments as a map and return a Result.
//
// Arguments is the JSON-decoded form: strings as Go strings, numbers as
// float64, booleans as bool, arrays as []any, objects as map[string]any.
type Call struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Result is the structured outcome of a tool invocation. Status is "ok"
// or "error"; on success Content holds the tool's return value (its shape
// is tool-specific); on failure Error carries the structured ToolError.
//
// Exactly one of Content and Error is populated. On success Status is
// "ok" and Error is nil; on failure Status is "error" and Error carries
// the structured kind/message/call.
type Result struct {
	Status  string     `json:"status"` // "ok" or "error"
	Content any        `json:"content,omitempty"`
	Error   *ToolError `json:"error,omitempty"`
}

// ToolError is the structured error type for tool failures. Kind is one
// of the documented constants (see the schema_violation / path_escape /
// permission_denied / unknown_tool / execution_failed constants used by
// the Dispatch pipeline); Message is a human-readable explanation; Call
// is the offending call (round-trip).
type ToolError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Call    Call   `json:"call"`
}

// Schema declares a tool's input shape (JSON-schema-lite; see Validate).
//
// Required lists argument names that MUST appear in Call.Arguments.
// Properties maps argument name → expected type. AdditionalProperties
// controls whether extra keys (not in Properties) are accepted; false
// (the JSON default for this struct) means they are rejected.
//
// The wire shape uses lowercase JSON keys with underscores so the schema
// can be marshaled directly to the model.
type Schema struct {
	Required             []string                `json:"required,omitempty"`
	Properties           map[string]PropertyType `json:"properties,omitempty"`
	AdditionalProperties bool                    `json:"additional_properties"` // default false
}

// PropertyType declares the expected type of an argument. The string
// values match JSON types so the schema can be marshaled directly to the
// model.
type PropertyType string

// PropertyType constants. The string values match JSON types.
const (
	TypeString PropertyType = "string"
	TypeInt    PropertyType = "int"
	TypeBool   PropertyType = "bool"
	TypeNumber PropertyType = "number"
	TypeArray  PropertyType = "array"
	TypeObject PropertyType = "object"
)

// ToolMeta is the registry-facing metadata for a tool: name, description,
// and a reserved Mode field for future permission-aware dispatch.
//
// Mode is reserved for future permission-aware dispatch (Run 004+); Run
// 003 does not gate on it. The empty value means "not specified".
type ToolMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Mode is reserved for future permission-aware dispatch (Run 004+);
	// Run 003 does not gate on it.
	Mode string `json:"mode,omitempty"`
}

// Tool is the interface every registered tool implements. Schema is
// JSON-schema-lite (see Validate); Execute is the tool's deterministic
// implementation.
//
// Execute returns a Result with Status="ok" on success or a non-nil
// error on execution failure (Dispatch wraps the error in a
// ToolError{Kind: "execution_failed"}). A tool that detects a
// pre-execution problem (a permission boundary, a missing input) should
// return Result{Status:"error", Error:&ToolError{...}} directly so the
// structured kind/message reaches the caller.
type Tool interface {
	Meta() ToolMeta
	Schema() Schema
	Execute(ctx context.Context, call Call) (Result, error)
}

// Policy decides whether a tool call is allowed given the active
// workspace. The seam exists today; Run 004 replaces Permissive with a
// real policy that honors the three explicit modes (READ_ONLY /
// WORKSPACE_WRITE / FULL_ACCESS) and adds a Mode parameter to Decide.
//
// The interface lives in this package because tools.Registry.Dispatch
// takes a Policy as a parameter type. The perm package implements
// Permissive against this interface; the caller (main.go) wires
// perm.Permissive into Dispatch.
type Policy interface {
	Decide(ctx context.Context, call Call, ws Workspace) Decision
}

// Decision is the policy's verdict. Reason is human-readable and is
// surfaced in tool_result events for audit-trail transparency (the
// stub's "policy-stub" reason makes the seam visible in the trail).
type Decision struct {
	Allowed bool
	Reason  string // human-readable, surfaced in tool_result events
}

// DecisionError is the structured error returned by the authorize
// pipeline when a call is rejected. Stage names the pipeline step that
// fired; Reason is a one-word kind; Call is the offending call (round-
// trip).
//
// The Error() message is a single line with no stack trace and no
// os.PathError wrapping — it carries the offending tool name, the
// stage, and the one-word reason, period.
type DecisionError struct {
	Stage  string // "schema", "path", "policy"
	Reason string // one-word kind
	Call   Call
}

func (e *DecisionError) Error() string {
	return "perm.Authorize rejected call " + e.Call.Name +
		" at stage " + e.Stage + ": " + e.Reason
}

// AuthorizeFunc is the seam between tools.Registry.Dispatch and the
// authorization pipeline (currently implemented by perm.Authorize).
// Dispatch invokes auth(ctx, call, schema, ws, pol) and short-circuits
// on the first failure; the pipeline order (schema → path → policy) is
// defined inside the function the caller supplies.
//
// The function type lives in this package (rather than in perm) so the
// tools package can accept it as a Dispatch parameter without importing
// perm — which would create a Go import cycle (perm imports tools for
// the validator; tools importing perm.Authorize would close the cycle).
//
// main.go wires perm.Authorize into Registry.Dispatch at startup; the
// tests wire a no-op or a stub function.
type AuthorizeFunc func(ctx context.Context, call Call, schema Schema, ws Workspace, pol Policy) *DecisionError

// Reason is a helper for tests and for the schema validator to surface a
// short reason string alongside the structured ToolError.Kind. The
// schema validator uses "missing_field", "wrong_type",
// "additional_property" as the reasons; the path normalizer reuses
// internal/path's ReasonAbsolutePath / ReasonParentTraversal /
// ReasonSymlinkEscape.
func (e *ToolError) Reason() string { return e.Kind }