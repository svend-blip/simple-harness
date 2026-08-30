package builtins

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// writeListSkillsFixture writes a SKILL.md file at
// <root>/.simple-harness/skills/<name>/SKILL.md with the given
// content. Mirrors internal/skill/skill_test.go's helper for the
// builtins test surface.
func writeListSkillsFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".simple-harness", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, "SKILL.md"), err)
	}
}

// writeListSkillsOverride writes a SKILL.md file at
// <root>/<name>/SKILL.md (NO .simple-harness prefix — the
// --skills-dir override is a literal directory).
func writeListSkillsOverride(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, "SKILL.md"), err)
	}
}

// TestListSkills_HappyPath_EnumeratesBothRoots pins the binding
// surface: a workspace + HOME both containing skills are
// enumerated with correct Source values.
func TestListSkills_HappyPath_EnumeratesBothRoots(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	writeListSkillsFixture(t, ws, "alpha", "# Alpha heading\nWS-ALPHA-CONTENT\n")
	writeListSkillsFixture(t, home, "beta", "# Beta heading\nHOME-BETA-CONTENT\n")

	// Drive the tool's Execute with PWD pointed at the
	// workspace + HOME pointed at the home dir so the
	// defaults resolve to the right roots.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ls := ListSkills{}
	res, err := ls.Execute(context.Background(), tools.Call{Name: "list_skills"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*ListSkillsResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *ListSkillsResult", res.Content)
	}
	if len(result.Skills) != 2 {
		t.Fatalf("len(Skills) = %d, want 2 (got %+v)", len(result.Skills), result.Skills)
	}

	byName := make(map[string]SkillInfo)
	for _, s := range result.Skills {
		byName[s.Name] = s
	}
	if s, ok := byName["alpha"]; !ok {
		t.Errorf("missing alpha in result: %+v", byName)
	} else {
		if s.Source != "workspace" {
			t.Errorf("alpha.Source = %q, want %q", s.Source, "workspace")
		}
		if !strings.Contains(s.Description, "Alpha") {
			t.Errorf("alpha.Description = %q, want to contain %q", s.Description, "Alpha")
		}
	}
	if s, ok := byName["beta"]; !ok {
		t.Errorf("missing beta in result: %+v", byName)
	} else {
		if s.Source != "global" {
			t.Errorf("beta.Source = %q, want %q", s.Source, "global")
		}
		if !strings.Contains(s.Description, "Beta") {
			t.Errorf("beta.Description = %q, want to contain %q", s.Description, "Beta")
		}
	}
}

// TestListSkills_WorkspaceWins pins the SCOPE §15 workspace-wins
// collision rule at the list_skills surface: a skill name present
// under BOTH workspace and global is enumerated ONCE with Source
// = "workspace" (no duplication, no "global" entry).
func TestListSkills_WorkspaceWins(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	wsHeading := "WS-WINS-LIST-PIN-aaaa"
	homeHeading := "HOME-LOSES-LIST-PIN-bbbb"
	writeListSkillsFixture(t, ws, "cold-start", "# "+wsHeading+"\nWS-content\n")
	writeListSkillsFixture(t, home, "cold-start", "# "+homeHeading+"\nHOME-content\n")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ls := ListSkills{}
	res, err := ls.Execute(context.Background(), tools.Call{Name: "list_skills"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*ListSkillsResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *ListSkillsResult", res.Content)
	}
	if len(result.Skills) != 1 {
		t.Fatalf("len(Skills) = %d, want 1 (collision must drop the global copy; got %+v)",
			len(result.Skills), result.Skills)
	}
	if result.Skills[0].Source != "workspace" {
		t.Errorf("Source = %q, want %q (workspace wins on collision per SCOPE §15)",
			result.Skills[0].Source, "workspace")
	}
	if !strings.Contains(result.Skills[0].Description, wsHeading) {
		t.Errorf("Description missing workspace heading %q; got %q",
			wsHeading, result.Skills[0].Description)
	}
	if strings.Contains(result.Skills[0].Description, homeHeading) {
		t.Errorf("Description contains global heading %q (workspace should win); got %q",
			homeHeading, result.Skills[0].Description)
	}
}

// TestListSkills_EmptyWorkspace_ReturnsEmptyList pins the "no
// skills is a valid state" semantics: an empty workspace + HOME
// returns an empty (non-nil) Skills slice with Status=ok.
func TestListSkills_EmptyWorkspace_ReturnsEmptyList(t *testing.T) {
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

	ls := ListSkills{}
	res, err := ls.Execute(context.Background(), tools.Call{Name: "list_skills"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*ListSkillsResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *ListSkillsResult", res.Content)
	}
	if result.Skills == nil {
		t.Errorf("Skills is nil, want empty non-nil slice")
	}
	if len(result.Skills) != 0 {
		t.Errorf("len(Skills) = %d, want 0 (got %+v)", len(result.Skills), result.Skills)
	}
}

// TestListSkills_SkillsDirOverride pins the --skills_dir
// override path: when skills_dir is non-empty, it REPLACES both
// default roots (workspace + global). The tool returns the
// override's skills with Source="override" only.
func TestListSkills_SkillsDirOverride(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	overrideDir := t.TempDir()

	writeListSkillsFixture(t, ws, "alpha", "# alpha\nWS-ALPHA\n")
	writeListSkillsFixture(t, home, "beta", "# beta\nHOME-BETA\n")
	writeListSkillsOverride(t, overrideDir, "gamma", "# gamma\nOVERRIDE-GAMMA\n")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ls := ListSkills{}
	res, err := ls.Execute(context.Background(), tools.Call{
		Name:      "list_skills",
		Arguments: map[string]any{"skills_dir": overrideDir},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}

	result, ok := res.Content.(*ListSkillsResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *ListSkillsResult", res.Content)
	}
	if len(result.Skills) != 1 {
		t.Fatalf("len(Skills) = %d, want 1 (override must replace both roots; got %+v)",
			len(result.Skills), result.Skills)
	}
	if result.Skills[0].Name != "gamma" {
		t.Errorf("Name = %q, want %q", result.Skills[0].Name, "gamma")
	}
	if result.Skills[0].Source != "override" {
		t.Errorf("Source = %q, want %q", result.Skills[0].Source, "override")
	}
}

// TestListSkills_SortedByName pins the deterministic-sort
// contract: when multiple skills are enumerated, the Skills
// slice is sorted by Name (ascending). The sort happens
// in-Execute so callers do not need to sort themselves.
func TestListSkills_SortedByName(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	for _, name := range []string{"zebra", "alpha", "mango", "bravo"} {
		writeListSkillsFixture(t, ws, name, "# "+name+"\ncontent\n")
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatalf("chdir ws: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	t.Setenv("HOME", home)

	ls := ListSkills{}
	res, err := ls.Execute(context.Background(), tools.Call{Name: "list_skills"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q", res.Status, "ok")
	}

	result, ok := res.Content.(*ListSkillsResult)
	if !ok {
		t.Fatalf("Result.Content type = %T, want *ListSkillsResult", res.Content)
	}
	if len(result.Skills) != 4 {
		t.Fatalf("len(Skills) = %d, want 4", len(result.Skills))
	}

	// Confirm in-place sorted order.
	if !sort.SliceIsSorted(result.Skills, func(i, j int) bool {
		return result.Skills[i].Name < result.Skills[j].Name
	}) {
		t.Errorf("Skills not sorted by Name: %+v", result.Skills)
	}
}