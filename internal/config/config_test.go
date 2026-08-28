package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes a JSON config to dir/.simple-harness/config.json
// and returns the path. The test creates the directory if needed.
func writeConfig(t *testing.T, dir string, body string) string {
	t.Helper()
	cfgDir := filepath.Join(dir, ".simple-harness")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	path := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDefaultsOnly(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	want := Default()
	if cfg != want {
		t.Fatalf("defaults-only config = %+v, want %+v", cfg, want)
	}
}

func TestUserConfigOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"base_url":"http://user:9000/v1","temperature":0.7}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "http://user:9000/v1" {
		t.Errorf("BaseURL = %q, want user override", cfg.Model.BaseURL)
	}
	if cfg.Model.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want user override 0.7", cfg.Model.Temperature)
	}
	if cfg.Model.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %d, want default 8192", cfg.Model.MaxOutputTokens)
	}
}

func TestProjectConfigBeatsUserConfig(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"base_url":"http://user:9000/v1"}}`)
	writeConfig(t, proj, `{"model":{"base_url":"http://project:9000/v1","temperature":0.9}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "http://project:9000/v1" {
		t.Errorf("BaseURL = %q, want project override", cfg.Model.BaseURL)
	}
	if cfg.Model.Temperature != 0.9 {
		t.Errorf("Temperature = %v, want project override 0.9", cfg.Model.Temperature)
	}
}

func TestEnvBeatsProjectConfig(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, proj, `{"model":{"base_url":"http://project:9000/v1"}}`)
	env := []string{
		"SIMPLE_HARNESS_BASE_URL=http://env:9000/v1",
		"SIMPLE_HARNESS_TEMPERATURE=0.1",
	}
	cfg, err := loadFrom(home, proj, env)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "http://env:9000/v1" {
		t.Errorf("BaseURL = %q, want env override", cfg.Model.BaseURL)
	}
	if cfg.Model.Temperature != 0.1 {
		t.Errorf("Temperature = %v, want env override 0.1", cfg.Model.Temperature)
	}
}

func TestEnvBeatsUserConfig(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"base_url":"http://user:9000/v1"}}`)
	env := []string{"SIMPLE_HARNESS_BASE_URL=http://env:9000/v1"}
	cfg, err := loadFrom(home, proj, env)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "http://env:9000/v1" {
		t.Errorf("BaseURL = %q, want env override", cfg.Model.BaseURL)
	}
}

func TestRequestTimeoutParseVariants(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"request_timeout":"1m"}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.RequestTimeout != time.Minute {
		t.Errorf("RequestTimeout = %v, want 1m", cfg.Model.RequestTimeout)
	}
}

func TestInvalidRequestTimeoutFails(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"request_timeout":"not-a-duration"}}`)
	_, err := loadFrom(home, proj, nil)
	if err == nil {
		t.Fatalf("loadFrom: expected error for invalid request_timeout, got nil")
	}
	if !strings.Contains(err.Error(), "request_timeout") {
		t.Errorf("error = %v, want mention of request_timeout", err)
	}
}

func TestMalformedJSONFails(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"base_url": "missing-close-quote`)
	_, err := loadFrom(home, proj, nil)
	if err == nil {
		t.Fatalf("loadFrom: expected error for malformed JSON, got nil")
	}
}

func TestMissingConfigFilesAreNotErrors(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v (missing files should not be errors)", err)
	}
	if cfg != Default() {
		t.Fatalf("missing-file config = %+v, want defaults", cfg)
	}
}

func TestProjectConfigSearchedUpward(t *testing.T) {
	proj := t.TempDir()
	writeConfig(t, proj, `{"model":{"base_url":"http://upward:9000/v1"}}`)
	subdir := filepath.Join(proj, "a", "b", "c")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home := t.TempDir()
	cfg, err := loadFrom(home, subdir, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "http://upward:9000/v1" {
		t.Errorf("BaseURL = %q, want upward-discovered project config", cfg.Model.BaseURL)
	}
}

func TestRenderRedactsAPIKey(t *testing.T) {
	cfg := Default()
	cfg.Model.APIKey = "sk-very-secret"
	var buf bytes.Buffer
	if err := cfg.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "sk-very-secret") {
		t.Fatalf("Render output leaked api_key: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "<redacted>") {
		t.Fatalf("Render output missing <redacted> marker: %q", buf.String())
	}
}

func TestRenderEmptyAPIKey(t *testing.T) {
	cfg := Default()
	cfg.Model.APIKey = ""
	var buf bytes.Buffer
	if err := cfg.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "<redacted>") {
		t.Fatalf("Render of empty api_key used redaction marker: %q", buf.String())
	}
}

func TestAPIKeyEmptyStringClearsPrevious(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"api_key":"user-key"}}`)
	writeConfig(t, proj, `{"model":{"api_key":""}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.APIKey != "" {
		t.Errorf("APIKey = %q, want empty (project config explicit-empty must override user)", cfg.Model.APIKey)
	}
}

// TestAPIKeyNullClearsPrevious — explicit JSON null in a higher-priority
// source MUST clear a lower-priority value. The previous pointer-overlay
// implementation collapsed null and absent into the same "skip" branch,
// which contradicted the handoff's explicit spec. This test is the
// regression guard for that gap.
func TestAPIKeyNullClearsPrevious(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"api_key":"sk-user-key"}}`)
	writeConfig(t, proj, `{"model":{"api_key": null}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.APIKey != "" {
		t.Errorf("APIKey = %q, want \"\" (explicit null must clear user-config value)", cfg.Model.APIKey)
	}
}

// TestBaseURLNullClearsPrevious — structural precedent for the
// non-string fields: a higher-priority null also clears for non-strings.
// Same shape as TestAPIKeyNullClearsPrevious but exercises a different
// type to confirm the presence-tracking is uniform, not api-key-specific.
func TestBaseURLNullClearsPrevious(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"base_url":"http://user:9000/v1"}}`)
	writeConfig(t, proj, `{"model":{"base_url": null}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.BaseURL != "" {
		t.Errorf("BaseURL = %q, want \"\" (explicit null must clear user-config value)", cfg.Model.BaseURL)
	}
}

// TestTemperatureNullClearsPrevious — another type: explicit null on
// a numeric field must also zero it out (replace the lower-priority
// value with 0, not with the default 0.2).
func TestTemperatureNullClearsPrevious(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	writeConfig(t, home, `{"model":{"temperature":0.7}}`)
	writeConfig(t, proj, `{"model":{"temperature": null}}`)
	cfg, err := loadFrom(home, proj, nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.Temperature != 0 {
		t.Errorf("Temperature = %v, want 0 (explicit null must clear user-config value)", cfg.Model.Temperature)
	}
}

func TestEnvInvalidTemperatureFails(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	env := []string{"SIMPLE_HARNESS_TEMPERATURE=not-a-number"}
	_, err := loadFrom(home, proj, env)
	if err == nil {
		t.Fatalf("loadFrom: expected error for invalid temperature, got nil")
	}
	if !strings.Contains(err.Error(), "TEMPERATURE") && !strings.Contains(err.Error(), "temperature") {
		t.Errorf("error = %v, want mention of TEMPERATURE", err)
	}
}

func TestEnvAppliesRequestTimeout(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	env := []string{"SIMPLE_HARNESS_REQUEST_TIMEOUT=45s"}
	cfg, err := loadFrom(home, proj, env)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.RequestTimeout != 45*time.Second {
		t.Errorf("RequestTimeout = %v, want 45s", cfg.Model.RequestTimeout)
	}
}

func TestEnvAppliesProvider(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	env := []string{"SIMPLE_HARNESS_PROVIDER=custom_provider"}
	cfg, err := loadFrom(home, proj, env)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.Provider != "custom_provider" {
		t.Errorf("Provider = %q, want custom_provider", cfg.Model.Provider)
	}
}
