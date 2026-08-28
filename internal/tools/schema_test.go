package tools

import "testing"

// TestSchema_Validate_MissingRequired: Required=["a"]; call.Arguments={};
// returns schema_violation with message "missing required field \"a\"".
func TestSchema_Validate_MissingRequired(t *testing.T) {
	schema := Schema{Required: []string{"a"}}
	call := Call{Arguments: map[string]any{}}

	err := Validate(call, schema)
	if err == nil {
		t.Fatalf("Validate returned nil, want *ToolError")
	}
	if err.Kind != "schema_violation" {
		t.Fatalf("Kind = %q, want %q", err.Kind, "schema_violation")
	}
	if msg := err.Message; msg != `missing required field "a"` {
		t.Fatalf("Message = %q, want %q", msg, `missing required field "a"`)
	}
}

// TestSchema_Validate_WrongType: Properties={"a": TypeString};
// call.Arguments={"a": 42}; returns schema_violation with message
// "field \"a\" has wrong type (expected string)".
func TestSchema_Validate_WrongType(t *testing.T) {
	schema := Schema{Properties: map[string]PropertyType{"a": TypeString}}
	call := Call{Arguments: map[string]any{"a": 42}}

	err := Validate(call, schema)
	if err == nil {
		t.Fatalf("Validate returned nil, want *ToolError")
	}
	if err.Kind != "schema_violation" {
		t.Fatalf("Kind = %q, want %q", err.Kind, "schema_violation")
	}
	want := `field "a" has wrong type (expected string)`
	if msg := err.Message; msg != want {
		t.Fatalf("Message = %q, want %q", msg, want)
	}
}

// TestSchema_Validate_AdditionalPropertiesRejected: Properties={"a":
// TypeString}, AdditionalProperties=false; call.Arguments={"a": "x",
// "b": "y"}; returns schema_violation with message "unknown field \"b\"".
func TestSchema_Validate_AdditionalPropertiesRejected(t *testing.T) {
	schema := Schema{
		Properties:           map[string]PropertyType{"a": TypeString},
		AdditionalProperties: false,
	}
	call := Call{Arguments: map[string]any{"a": "x", "b": "y"}}

	err := Validate(call, schema)
	if err == nil {
		t.Fatalf("Validate returned nil, want *ToolError")
	}
	if err.Kind != "schema_violation" {
		t.Fatalf("Kind = %q, want %q", err.Kind, "schema_violation")
	}
	want := `unknown field "b"`
	if msg := err.Message; msg != want {
		t.Fatalf("Message = %q, want %q", msg, want)
	}
}

// TestSchema_Validate_AdditionalPropertiesAllowed: Properties={"a":
// TypeString}, AdditionalProperties=true; call.Arguments={"a": "x",
// "b": "y"}; returns nil.
func TestSchema_Validate_AdditionalPropertiesAllowed(t *testing.T) {
	schema := Schema{
		Properties:           map[string]PropertyType{"a": TypeString},
		AdditionalProperties: true,
	}
	call := Call{Arguments: map[string]any{"a": "x", "b": "y"}}

	err := Validate(call, schema)
	if err != nil {
		t.Fatalf("Validate returned %v, want nil (AdditionalProperties=true accepts extras)", err)
	}
}

// TestSchema_Validate_EmptySchema: Schema{} (no required, no properties,
// no additional); call.Arguments={}; returns nil.
func TestSchema_Validate_EmptySchema(t *testing.T) {
	schema := Schema{}
	call := Call{Arguments: map[string]any{}}

	err := Validate(call, schema)
	if err != nil {
		t.Fatalf("Validate(empty schema, empty call) returned %v, want nil", err)
	}
}

// TestSchema_Validate_NumberType: Properties={"n": TypeNumber};
// call.Arguments={"n": 3.14}; returns nil. JSON numbers decode as
// float64; the validator accepts float64 for TypeNumber.
func TestSchema_Validate_NumberType(t *testing.T) {
	schema := Schema{Properties: map[string]PropertyType{"n": TypeNumber}}
	call := Call{Arguments: map[string]any{"n": 3.14}}

	err := Validate(call, schema)
	if err != nil {
		t.Fatalf("Validate(n=3.14, TypeNumber) returned %v, want nil", err)
	}
}

// TestSchema_Validate_AllJSONTypes: a single test that walks every
// PropertyType through a matching value (string / int / bool / number /
// array / object) and asserts nil. Defensive coverage: if a future
// refactor drops a type from the switch in typeMatches, this test fails.
func TestSchema_Validate_AllJSONTypes(t *testing.T) {
	schema := Schema{
		Properties: map[string]PropertyType{
			"s": TypeString,
			"i": TypeInt,
			"b": TypeBool,
			"n": TypeNumber,
			"a": TypeArray,
			"o": TypeObject,
		},
	}
	call := Call{Arguments: map[string]any{
		"s": "hello",
		"i": 7,
		"b": true,
		"n": 1.5,
		"a": []any{1, 2},
		"o": map[string]any{"k": "v"},
	}}
	if err := Validate(call, schema); err != nil {
		t.Fatalf("Validate(all-types match) returned %v, want nil", err)
	}
}