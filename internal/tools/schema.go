package tools

import "fmt"

// Validate runs the JSON-schema-lite validation rules and returns nil
// on success or a *ToolError with Kind="schema_violation" on the first
// failure. The rules (binding per handoff 013's §"Deliverable"
// subsection 3):
//
//   - Required: every name in s.Required must appear in call.Arguments.
//   - Types: each call.Arguments[name] must match s.Properties[name]
//     (string/int/bool/number/array/object).
//   - AdditionalProperties: if s.AdditionalProperties is false (the
//     JSON default), no key in call.Arguments may be absent from
//     s.Properties. If true, extra keys are accepted without per-key
//     type checking.
//
// The first failure produces a *ToolError{Kind: "schema_violation",
// Message: ...} with the offending Call round-tripped.
//
// An empty schema (no Required, no Properties, AdditionalProperties
// false) accepts an empty call: there are no required fields, no typed
// fields, and no extra fields to reject.
func Validate(call Call, s Schema) *ToolError {
	for _, req := range s.Required {
		if _, ok := call.Arguments[req]; !ok {
			return &ToolError{
				Kind:    "schema_violation",
				Message: fmt.Sprintf("missing required field %q", req),
				Call:    call,
			}
		}
	}
	if !s.AdditionalProperties {
		for name := range call.Arguments {
			if _, ok := s.Properties[name]; !ok {
				return &ToolError{
					Kind:    "schema_violation",
					Message: fmt.Sprintf("unknown field %q", name),
					Call:    call,
				}
			}
		}
	}
	for name, expected := range s.Properties {
		v, present := call.Arguments[name]
		if !present {
			continue // Required check covers missing.
		}
		if !typeMatches(v, expected) {
			return &ToolError{
				Kind:    "schema_violation",
				Message: fmt.Sprintf("field %q has wrong type (expected %s)", name, expected),
				Call:    call,
			}
		}
	}
	return nil
}

// typeMatches returns true iff v has the JSON type expected. JSON
// numbers decode as float64 in Go's encoding/json package, so TypeNumber
// matches float64 (and the int variants that JSON considers numeric are
// also covered via Go's dynamic typing — the validator treats float64
// as the canonical JSON number).
//
// TypeInt accepts BOTH the Go literal `int` (used by Go-constructed
// call.Arguments maps in tests) AND `float64` (the canonical JSON-
// decoded number). A float64 with no fractional part is accepted
// (since JSON integer literals round-trip through float64 even when
// they have no decimal point — e.g. `{"x": 5}` decodes as
// float64(5)). A float64 with a fractional part is rejected for
// TypeInt (the schema promised an integer, not a real).
func typeMatches(v any, t PropertyType) bool {
	switch t {
	case TypeString:
		_, ok := v.(string)
		return ok
	case TypeInt:
		if i, ok := v.(int); ok {
			_ = i
			return ok
		}
		if f, ok := v.(float64); ok {
			return f == float64(int64(f))
		}
		return false
	case TypeBool:
		_, ok := v.(bool)
		return ok
	case TypeNumber:
		_, ok := v.(float64) // JSON numbers decode as float64
		return ok
	case TypeArray:
		_, ok := v.([]any)
		return ok
	case TypeObject:
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}