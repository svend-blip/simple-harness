package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// writeLoadSkillFixture writes a SKILL.md file at
// <root>/.simple-harness/skills/<name>/SKILL.md with the given
// content. Mirrors list_skills_test.go's helper for the load_skill
// test surface.
func writeLoadSkillFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".simple-harness", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, "SKILL.md"), err)
	}
}

// TestLoadSkill_HappyPath_LoadsContent pins the SCOPE §45 binding
// surface: a known skill's content + source location are returned
// with Status=ok.
func TestLoadSkill_HappyPath_LoadsContent(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	marker := "LOAD-SKILL-MARKER-cc11"
	writeLoadSkillFixture(t, ws, "cold-start", marker+"\n")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ll := LoadSkill{}
	res, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{"name": "cold-start"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*LoadSkillResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *LoadSkillResult", res.Content)
	}
	if result.Name != "cold-start" {
		t.Errorf("Name = %q, want %q", result.Name, "cold-start")
	}
	if result.Source != "workspace" {
		t.Errorf("Source = %q, want %q", result.Source, "workspace")
	}
	if !strings.Contains(result.Content, marker) {
		t.Errorf("Content missing marker %q; got %q", marker, result.Content)
	}
}

// TestLoadSkill_NotFound_ReturnsSkillNotFound pins the SCOPE §45
// "unknown skill name = structured tool failure" path: an unknown
// skill name returns Status=error + Error.Kind=skill_not_found.
// The model can call list_skills to discover what's available;
// the harness never panics, never crashes, never exits mid-session.
func TestLoadSkill_NotFound_ReturnsSkillNotFound(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ll := LoadSkill{}
	res, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{"name": "no-such-skill-tg"},
	})
	if err != nil {
		t.Fatalf("Execute returned non-nil error (should be a structured Result, not a panic): %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("Error is nil, want ToolError with Kind=skill_not_found")
	}
	if res.Error.Kind != "skill_not_found" {
		t.Errorf("Error.Kind = %q, want %q", res.Error.Kind, "skill_not_found")
	}
	// The message must reference the unknown name so the
	// model + operator can see what failed.
	if !strings.Contains(res.Error.Message, "no-such-skill-tg") {
		t.Errorf("Error.Message missing unknown-name %q; got %q",
			"no-such-skill-tg", res.Error.Message)
	}
}

// TestLoadSkill_IdempotentDoubleLoad pins the idempotent double-
// load contract: loading the same skill twice returns the same
// Content + Source; the tool's Execute does not corrupt internal
// state on repeated calls (the duplicate detection is structural
// at the SetSkills call site in the follow-on Run, but this pin
// demonstrates the tool itself can be called repeatedly without
// state corruption).
func TestLoadSkill_IdempotentDoubleLoad(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	marker := "IDEMPOTENT-MARKER-cc11"
	writeLoadSkillFixture(t, ws, "cold-start", marker+"\n")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ll := LoadSkill{}

	// First load.
	res1, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{"name": "cold-start"},
	})
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if res1.Status != "ok" {
		t.Fatalf("first Status = %q, want %q", res1.Status, "ok")
	}
	r1, ok := res1.Content.(*LoadSkillResult)
	if !ok {
		t.Fatalf("first Result.Content type = %T, want *LoadSkillResult", res1.Content)
	}

	// Second load — same skill, same call shape.
	res2, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{"name": "cold-start"},
	})
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if res2.Status != "ok" {
		t.Fatalf("second Status = %q, want %q", res2.Status, "ok")
	}
	r2, ok := res2.Content.(*LoadSkillResult)
	if !ok {
		t.Fatalf("second Result.Content type = %T, want *LoadSkillResult", res2.Content)
	}

	// Idempotency contract: same Name + Source + Content.
	if r1.Name != r2.Name {
		t.Errorf("Name mismatch on double-load: %q vs %q", r1.Name, r2.Name)
	}
	if r1.Source != r2.Source {
		t.Errorf("Source mismatch on double-load: %q vs %q", r1.Source, r2.Source)
	}
	if r1.Content != r2.Content {
		t.Errorf("Content mismatch on double-load: tool state corrupted by repeated call")
	}
}

// TestLoadSkill_SkillsDirOverride pins the --skills_dir override
// path: when skills_dir is non-empty, it REPLACES both default
// roots (workspace + global), matching Load semantics.
func TestLoadSkill_SkillsDirOverride(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	overrideDir := t.TempDir()

	// Override carries the cold-start skill with a known marker.
	overrideMarker := "OVERRIDE-MARKER-cc11"
	dir := filepath.Join(overrideDir, "cold-start")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte(overrideMarker+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Workspace also carries cold-start (a different marker) —
	// the override must shadow it.
	wsMarker := "WS-MARKER-cc11"
	writeLoadSkillFixture(t, ws, "cold-start", wsMarker+"\n")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ll := LoadSkill{}
	res, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{"name": "cold-start", "skills_dir": overrideDir},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*LoadSkillResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *LoadSkillResult", res.Content)
	}
	if result.Source != "override" {
		t.Errorf("Source = %q, want %q", result.Source, "override")
	}
	if !strings.Contains(result.Content, overrideMarker) {
		t.Errorf("Content missing override marker %q; got %q",
			overrideMarker, result.Content)
	}
	if strings.Contains(result.Content, wsMarker) {
		t.Errorf("Content contains workspace marker %q (override should replace both roots); got %q",
			wsMarker, result.Content)
	}
}

// TestLoadSkill_MissingName_ReturnsSchemaViolation pins the
// schema-validator-equivalent surface: a missing `name` argument
// returns Status=error with Kind=schema_violation. The schema
// validator already catches this at the pipeline stage; the
// tool's defensive guard returns the same shape for direct-
// Execute callers (e.g. tests that bypass Dispatch).
func TestLoadSkill_MissingName_ReturnsSchemaViolation(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ll := LoadSkill{}
	res, err := ll.Execute(context.Background(), tools.Call{
		Name:      "load_skill",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute returned non-nil error (should be a structured Result, not a panic): %v", err)
	}
	if res.Status != "error" {
		t.Fatalf("Status = %q, want %q", res.Status, "error")
	}
	if res.Error == nil {
		t.Fatalf("Error is nil, want ToolError with Kind=schema_violation")
	}
	if res.Error.Kind != "schema_violation" {
		t.Errorf("Error.Kind = %q, want %q", res.Error.Kind, "schema_violation")
	}
}