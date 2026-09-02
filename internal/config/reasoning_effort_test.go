package config

import (
	"strings"
	"testing"
)

// reasoning_effort (2026-09-02): env and file set it, the empty default
// leaves the request field out, and an unknown level is rejected loudly.
func TestEnvAppliesReasoningEffort(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	cfg, err := loadFrom(home, proj, []string{"SIMPLE_HARNESS_REASONING_EFFORT=medium"})
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.ReasoningEffort != "medium" {
		t.Fatalf("ReasoningEffort = %q, want medium", cfg.Model.ReasoningEffort)
	}
}

func TestReasoningEffortDefaultsToEmpty(t *testing.T) {
	cfg, err := loadFrom(t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.ReasoningEffort != "" {
		t.Fatalf("default ReasoningEffort = %q, want empty", cfg.Model.ReasoningEffort)
	}
}

func TestEnvInvalidReasoningEffortFails(t *testing.T) {
	_, err := loadFrom(t.TempDir(), t.TempDir(), []string{"SIMPLE_HARNESS_REASONING_EFFORT=maximum"})
	if err == nil {
		t.Fatal("expected an error for reasoning_effort=maximum")
	}
	if !strings.Contains(err.Error(), "reasoning_effort") {
		t.Errorf("error = %v, want mention of reasoning_effort", err)
	}
}

func TestEnvAppliesThinkingControls(t *testing.T) {
	cfg, err := loadFrom(t.TempDir(), t.TempDir(), []string{"SIMPLE_HARNESS_ENABLE_THINKING=false", "SIMPLE_HARNESS_THINKING_BUDGET=512"})
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Model.EnableThinking == nil || *cfg.Model.EnableThinking {
		t.Fatalf("EnableThinking = %v, want false", cfg.Model.EnableThinking)
	}
	if cfg.Model.ThinkingBudget != 512 {
		t.Fatalf("ThinkingBudget = %d, want 512", cfg.Model.ThinkingBudget)
	}
	def, _ := loadFrom(t.TempDir(), t.TempDir(), nil)
	if def.Model.EnableThinking != nil || def.Model.ThinkingBudget != 0 {
		t.Fatal("thinking controls must default to omitted")
	}
	if _, err := loadFrom(t.TempDir(), t.TempDir(), []string{"SIMPLE_HARNESS_ENABLE_THINKING=maybe"}); err == nil {
		t.Fatal("expected an error for enable_thinking=maybe")
	}
}
