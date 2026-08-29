package mcp

import (
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestSanitizeServerName_NoDoubleUnderscore: a server name with no
// "__" sequence is returned unchanged. The collision-naming rule
// expects the server name to already be a clean identifier.
func TestSanitizeServerName_NoDoubleUnderscore(t *testing.T) {
	got := sanitizeServerName("weather")
	if got != "weather" {
		t.Fatalf("sanitizeServerName(%q) = %q, want %q", "weather", got, "weather")
	}
}

// TestSanitizeServerName_ReplacesDoubleUnderscore: "__" becomes "_"
// so the <server>__<tool> prefix's first "__" always marks the
// separator. A server named "foo__bar" becomes "foo_bar".
func TestSanitizeServerName_ReplacesDoubleUnderscore(t *testing.T) {
	got := sanitizeServerName("foo__bar")
	if got != "foo_bar" {
		t.Fatalf("sanitizeServerName(%q) = %q, want %q", "foo__bar", got, "foo_bar")
	}
}

// TestSanitizeServerName_MultipleReplacements: every "__" is
// replaced, not just the first. "foo__bar__baz" becomes "foo_bar_baz".
func TestSanitizeServerName_MultipleReplacements(t *testing.T) {
	got := sanitizeServerName("foo__bar__baz")
	if got != "foo_bar_baz" {
		t.Fatalf("sanitizeServerName(%q) = %q, want %q", "foo__bar__baz", got, "foo_bar_baz")
	}
}

// TestFormatCollisionName_Shape: the form is exactly "<server>__<tool>".
// The double-underscore is the binding separator (per the GOAL's
// literal "<server>__<tool>" directive); a single underscore would be
// ambiguous because MCP server names can contain underscores.
func TestFormatCollisionName_Shape(t *testing.T) {
	got := FormatCollisionName("weather", "read_file")
	if got != "weather__read_file" {
		t.Fatalf("FormatCollisionName(weather, read_file) = %q, want %q", got, "weather__read_file")
	}
}

// TestFormatCollisionName_SanitizesServer: the server portion is
// sanitized; "foo__bar" + "tool" becomes "foo_bar__tool" (the first
// "__" in the result marks the separator).
func TestFormatCollisionName_SanitizesServer(t *testing.T) {
	got := FormatCollisionName("foo__bar", "tool")
	if got != "foo_bar__tool" {
		t.Fatalf("FormatCollisionName(foo__bar, tool) = %q, want %q", got, "foo_bar__tool")
	}
}

// TestSchemaFromMap_Nil: a nil input returns the zero-value
// tools.Schema. A server whose listing reports a tool with no schema
// produces a tool that accepts any call (the empty schema's default
// behavior).
func TestSchemaFromMap_Nil(t *testing.T) {
	s, err := schemaFromMap(nil)
	if err != nil {
		t.Fatalf("schemaFromMap(nil) error = %v, want nil", err)
	}
	if len(s.Required) != 0 {
		t.Fatalf("Required = %v, want empty", s.Required)
	}
	if len(s.Properties) != 0 {
		t.Fatalf("Properties = %v, want empty", s.Properties)
	}
	if s.AdditionalProperties {
		t.Fatalf("AdditionalProperties = true, want false")
	}
}

// TestSchemaFromMap_RequiredAndProperties: a fully-populated schema
// converts cleanly. Required → Required; Properties → Properties
// (with the type strings mapped to PropertyType values); the type
// mapping pins the test against future drift.
func TestSchemaFromMap_RequiredAndProperties(t *testing.T) {
	in := map[string]interface{}{
		"required": []interface{}{"path"},
		"properties": map[string]interface{}{
			"path":  map[string]interface{}{"type": "string"},
			"count": map[string]interface{}{"type": "integer"},
		},
		"additionalProperties": false,
	}
	s, err := schemaFromMap(in)
	if err != nil {
		t.Fatalf("schemaFromMap(...) error = %v, want nil", err)
	}
	if len(s.Required) != 1 || s.Required[0] != "path" {
		t.Fatalf("Required = %v, want [path]", s.Required)
	}
	if s.Properties["path"] != tools.TypeString {
		t.Fatalf("Properties[path] = %q, want %q", s.Properties["path"], tools.TypeString)
	}
	if s.Properties["count"] != tools.TypeInt {
		t.Fatalf("Properties[count] = %q, want %q", s.Properties["count"], tools.TypeInt)
	}
	if s.AdditionalProperties {
		t.Fatalf("AdditionalProperties = true, want false")
	}
}

// TestSchemaFromMap_AdditionalPropertiesTrue: the strict-default
// false is the JSON-schema-lite default; an explicit true from the
// server is honored. This is the only way to opt into "extra keys
// pass without per-key type checking" at registration time.
func TestSchemaFromMap_AdditionalPropertiesTrue(t *testing.T) {
	in := map[string]interface{}{"additionalProperties": true}
	s, err := schemaFromMap(in)
	if err != nil {
		t.Fatalf("schemaFromMap(...) error = %v, want nil", err)
	}
	if !s.AdditionalProperties {
		t.Fatalf("AdditionalProperties = false, want true")
	}
}

// TestSchemaFromMap_AdditionalPropertiesObjectForm: JSON Schema's
// object form (additionalProperties: { ... }) is not represented in
// tools.Schema. The conversion treats any non-boolean as false (the
// strict default). This pins the conversion's documented leniency.
func TestSchemaFromMap_AdditionalPropertiesObjectForm(t *testing.T) {
	in := map[string]interface{}{
		"additionalProperties": map[string]interface{}{"type": "string"},
	}
	s, err := schemaFromMap(in)
	if err != nil {
		t.Fatalf("schemaFromMap(...) error = %v, want nil", err)
	}
	if s.AdditionalProperties {
		t.Fatalf("AdditionalProperties = true, want false (object form is treated as strict-default)")
	}
}

// TestSchemaFromMap_UnknownTypeDropped: a property whose "type" is
// not in the recognized set is silently dropped from the Properties
// map. The validator will reject the property at use time (no
// validator entry → unknown field). The conversion is intentionally
// lenient to survive the wide variety of shapes MCP servers report.
func TestSchemaFromMap_UnknownTypeDropped(t *testing.T) {
	in := map[string]interface{}{
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"type": "exotic_type"},
		},
	}
	s, err := schemaFromMap(in)
	if err != nil {
		t.Fatalf("schemaFromMap(...) error = %v, want nil", err)
	}
	if _, present := s.Properties["x"]; present {
		t.Fatalf("Properties[x] present, want absent (unknown type is dropped)")
	}
}

// TestSchemaFromMap_NoTypeFieldDropped: a property with no "type"
// field is silently dropped. Same rationale as
// TestSchemaFromMap_UnknownTypeDropped.
func TestSchemaFromMap_NoTypeFieldDropped(t *testing.T) {
	in := map[string]interface{}{
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"description": "no type field"},
		},
	}
	s, err := schemaFromMap(in)
	if err != nil {
		t.Fatalf("schemaFromMap(...) error = %v, want nil", err)
	}
	if _, present := s.Properties["x"]; present {
		t.Fatalf("Properties[x] present, want absent (no type field is dropped)")
	}
}

// TestSchemaFromMap_RequiredNotArray: a "required" field that is not
// an array is a structured error. Per the contract, malformed
// listings are startup errors (AddServer returns the error verbatim).
func TestSchemaFromMap_RequiredNotArray(t *testing.T) {
	in := map[string]interface{}{
		"required": "path",
	}
	_, err := schemaFromMap(in)
	if err == nil {
		t.Fatalf("schemaFromMap(..., required: string) = nil, want error")
	}
}

// TestSchemaFromMap_PropertiesNotObject: a "properties" field that is
// not an object is a structured error.
func TestSchemaFromMap_PropertiesNotObject(t *testing.T) {
	in := map[string]interface{}{
		"properties": "not an object",
	}
	_, err := schemaFromMap(in)
	if err == nil {
		t.Fatalf("schemaFromMap(..., properties: string) = nil, want error")
	}
}

// TestJsonTypeToPropertyType_AllKnown: the type mapping covers the
// six PropertyType values + the JSON-Schema alias pairs (integer/int,
// boolean/bool). Unknown types return the empty string.
func TestJsonTypeToPropertyType_AllKnown(t *testing.T) {
	cases := map[string]tools.PropertyType{
		"string":  tools.TypeString,
		"integer": tools.TypeInt,
		"int":     tools.TypeInt,
		"number":  tools.TypeNumber,
		"boolean": tools.TypeBool,
		"bool":    tools.TypeBool,
		"array":   tools.TypeArray,
		"object":  tools.TypeObject,
		"":        "",
		"unknown": "",
	}
	for in, want := range cases {
		got := jsonTypeToPropertyType(in)
		if got != want {
			t.Fatalf("jsonTypeToPropertyType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStageToKind: the (stage → kind) mapping matches
// tools.mapStageToKind so the model's view of an MCP-tool failure is
// indistinguishable from a builtin-tool failure. An unknown stage
// maps to "internal_error" (the same fallback the registry uses).
func TestStageToKind(t *testing.T) {
	cases := map[string]string{
		"schema":          "schema_violation",
		"path":            "path_escape",
		"policy":          "permission_denied",
		"unknown_stage":   "internal_error",
		"":                "internal_error",
	}
	for stage, want := range cases {
		got := stageToKind(stage, "ignored")
		if got != want {
			t.Fatalf("stageToKind(%q) = %q, want %q", stage, got, want)
		}
	}
}

// TestAllowlisted_EmptyAllowsAll: an empty allowlist means "all
// listed tools are registered". The check is at registration time;
// a tool not on the allowlist is never registered (never callable).
func TestAllowlisted_EmptyAllowsAll(t *testing.T) {
	for _, name := range []string{"a", "b", "c"} {
		if !allowlisted(nil, name) {
			t.Fatalf("allowlisted(nil, %q) = false, want true", name)
		}
		if !allowlisted([]string{}, name) {
			t.Fatalf("allowlisted([], %q) = false, want true", name)
		}
	}
}

// TestAllowlisted_ExactMatch: the filter is case-sensitive and exact-
// match. A name not on the list is excluded; a name on the list is
// included.
func TestAllowlisted_ExactMatch(t *testing.T) {
	list := []string{"tool_a", "tool_b"}
	if !allowlisted(list, "tool_a") {
		t.Fatalf("allowlisted([tool_a, tool_b], tool_a) = false, want true")
	}
	if !allowlisted(list, "tool_b") {
		t.Fatalf("allowlisted([tool_a, tool_b], tool_b) = false, want true")
	}
	if allowlisted(list, "tool_c") {
		t.Fatalf("allowlisted([tool_a, tool_b], tool_c) = true, want false")
	}
	if allowlisted(list, "TOOL_A") {
		t.Fatalf("allowlisted is case-sensitive; TOOL_A should be excluded")
	}
}