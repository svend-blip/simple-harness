package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestConfigShowRendersThePosition pins SCOPE §2.2: `simple-harness
// config show` gains a top-level "position" object beside the existing
// fields, carrying the three SIMPLE_HARNESS_* environment variables as
// the process sees them, so an operator can verify what the MCP
// adapter will fill. With the variables set, their values appear; with
// them empty/unset, the three fields render as empty strings. The
// position never comes from the config file — there is no config-file
// key for it.
func TestConfigShowRendersThePosition(t *testing.T) {
	t.Setenv("SIMPLE_HARNESS_RUN_ID", "017")
	t.Setenv("SIMPLE_HARNESS_HANDOFF_ID", "063")
	t.Setenv("SIMPLE_HARNESS_FLOW_KEY", "9000-01-PLOOP")

	var buf bytes.Buffer
	if err := Default().Render(&buf); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("rendered config is not valid JSON: %v\n%s", err, buf.String())
	}
	pos, ok := doc["position"].(map[string]any)
	if !ok {
		t.Fatalf("rendered config missing position object; got %s", buf.String())
	}
	for key, want := range map[string]string{
		"run_id":     "017",
		"handoff_id": "063",
		"flow_key":   "9000-01-PLOOP",
	} {
		if got := pos[key]; got != want {
			t.Fatalf("position.%s = %v, want %q", key, got, want)
		}
	}
	// The position sits beside the existing fields, which stay present.
	for _, field := range []string{"model", "mcp_servers", "shell_timeout"} {
		if _, ok := doc[field]; !ok {
			t.Fatalf("rendered config lost existing field %q; got %s", field, buf.String())
		}
	}

	// Unset variables render as empty strings (the operator sees the
	// shape even when the harness has no position).
	t.Setenv("SIMPLE_HARNESS_RUN_ID", "")
	t.Setenv("SIMPLE_HARNESS_HANDOFF_ID", "")
	t.Setenv("SIMPLE_HARNESS_FLOW_KEY", "")
	buf.Reset()
	if err := Default().Render(&buf); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	doc = nil
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("rendered config is not valid JSON: %v", err)
	}
	pos, ok = doc["position"].(map[string]any)
	if !ok {
		t.Fatalf("rendered config missing position object when env unset; got %s", buf.String())
	}
	for _, key := range []string{"run_id", "handoff_id", "flow_key"} {
		if got, ok := pos[key].(string); !ok || got != "" {
			t.Fatalf("position.%s with unset variable = %v (%T), want empty string", key, pos[key], pos[key])
		}
	}
}
