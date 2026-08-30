package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillFixture creates <root>/.simple-harness/skills/<name>/SKILL.md
// with content as the file body. Returns the absolute path of the
// created file. The intermediate directories are created with 0o755
// permissions.
func writeSkillFixture(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, ".simple-harness", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestLoad_FoundInWorkspace: a skill file under
// <tmp>/.simple-harness/skills/<name>/SKILL.md is loaded with
// Source="workspace" when WorkspaceDir is set.
func TestLoad_FoundInWorkspace(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	marker := "WORKSPACE-MARKER-cb3a1f"
	writeSkillFixture(t, ws, "cold-start", marker+"\n")

	got, err := Load("cold-start", LoadOptions{
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Name != "cold-start" {
		t.Errorf("Skill.Name = %q, want %q", got.Name, "cold-start")
	}
	if !strings.Contains(got.Content, marker) {
		t.Errorf("Skill.Content missing marker %q; got %q", marker, got.Content)
	}
	if got.Source != "workspace" {
		t.Errorf("Skill.Source = %q, want %q", got.Source, "workspace")
	}
}

// TestLoad_FoundInGlobal: a skill file under
// <tmp>/.simple-harness/skills/<name>/SKILL.md in the HOME root
// is loaded with Source="global" when WorkspaceDir is empty and
// only the global root is configured.
func TestLoad_FoundInGlobal(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	marker := "GLOBAL-MARKER-7d2b"
	writeSkillFixture(t, home, "cold-start", marker+"\n")

	got, err := Load("cold-start", LoadOptions{
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Name != "cold-start" {
		t.Errorf("Skill.Name = %q, want %q", got.Name, "cold-start")
	}
	if !strings.Contains(got.Content, marker) {
		t.Errorf("Skill.Content missing marker %q; got %q", marker, got.Content)
	}
	if got.Source != "global" {
		t.Errorf("Skill.Source = %q, want %q", got.Source, "global")
	}
}

// TestLoad_WorkspaceWinsOnCollision: the same skill name exists
// under both workspace and global with distinguishable content. The
// workspace's file is returned (SCOPE §15 binding contract pin).
func TestLoad_WorkspaceWinsOnCollision(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	wsMarker := "WS-WINS-collision-pin-aaaa"
	homeMarker := "HOME-LOSES-collision-pin-bbbb"
	writeSkillFixture(t, ws, "cold-start", wsMarker+"\n")
	writeSkillFixture(t, home, "cold-start", homeMarker+"\n")

	got, err := Load("cold-start", LoadOptions{
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Source != "workspace" {
		t.Errorf("Skill.Source = %q, want %q (workspace wins on collision per SCOPE §15)", got.Source, "workspace")
	}
	if !strings.Contains(got.Content, wsMarker) {
		t.Errorf("Skill.Content missing workspace marker %q; got %q", wsMarker, got.Content)
	}
	if strings.Contains(got.Content, homeMarker) {
		t.Errorf("Skill.Content contains global marker %q (workspace should win); got %q", homeMarker, got.Content)
	}
}

// TestLoad_NotFound_ReturnsErrSkillNotFound: a name that does not
// exist under any active root returns ErrSkillNotFound (checkable
// via errors.Is).
func TestLoad_NotFound_ReturnsErrSkillNotFound(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	got, err := Load("no-such-skill-tg9", LoadOptions{
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if got != nil {
		t.Errorf("Load returned non-nil Skill on not-found: %+v", got)
	}
	if err == nil {
		t.Fatalf("Load returned nil error for missing skill")
	}
	if !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("Load returned error %v; errors.Is(err, ErrSkillNotFound) = false, want true", err)
	}
}

// TestLoad_SkillsDirOverride_OnlyUsesOverride: when SkillsDir is
// non-empty, the loader searches ONLY that root — workspace and
// home roots are NOT consulted. The returned Source is "override".
// Also verifies the override replaces both default roots when the
// same skill name exists under the workspace and override.
func TestLoad_SkillsDirOverride_OnlyUsesOverride(t *testing.T) {
	overrideDir := t.TempDir()
	ws := t.TempDir()
	home := t.TempDir()

	// Place a skill at <overrideDir>/<name>/SKILL.md (no
	// .simple-harness prefix — the override is a literal dir).
	overrideMarker := "OVERRIDE-MARKER-9f3a"
	overridePath := filepath.Join(overrideDir, "cold-start")
	if err := os.MkdirAll(overridePath, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", overridePath, err)
	}
	if err := os.WriteFile(filepath.Join(overridePath, "SKILL.md"),
		[]byte(overrideMarker+"\n"), 0o644); err != nil {
		t.Fatalf("write override skill: %v", err)
	}

	// Also place the same skill name under workspace — the
	// override must shadow it (workspace is NOT consulted when
	// override is active).
	wsMarker := "SHOULD-NOT-APPEAR-ws-marker"
	writeSkillFixture(t, ws, "cold-start", wsMarker+"\n")

	got, err := Load("cold-start", LoadOptions{
		SkillsDir:    overrideDir,
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Source != "override" {
		t.Errorf("Skill.Source = %q, want %q", got.Source, "override")
	}
	if !strings.Contains(got.Content, overrideMarker) {
		t.Errorf("Skill.Content missing override marker %q; got %q", overrideMarker, got.Content)
	}
	if strings.Contains(got.Content, wsMarker) {
		t.Errorf("Skill.Content contains workspace marker %q (override should replace both roots); got %q", wsMarker, got.Content)
	}
}

// TestLoad_EmptyName_ReturnsError: an empty name returns an error
// that is NOT ErrSkillNotFound — it's a distinct validation failure
// per the function contract.
func TestLoad_EmptyName_ReturnsError(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	got, err := Load("", LoadOptions{
		WorkspaceDir: ws,
		HomeDir:      home,
	})
	if got != nil {
		t.Errorf("Load returned non-nil Skill on empty name: %+v", got)
	}
	if err == nil {
		t.Fatalf("Load returned nil error for empty name")
	}
	if errors.Is(err, ErrSkillNotFound) {
		t.Errorf("Load returned ErrSkillNotFound for empty name; want a distinct error (the empty-name case is not a 'searched and missed' case)")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Load error message %q should mention \"empty\" for diagnostic clarity", err.Error())
	}
}

// TestLoad_SourceFieldReportsOrigin: exhaustive table-driven
// coverage of the Source field across the four search-root
// permutations: workspace-only, global-only, override-only,
// workspace+global collision.
func TestLoad_SourceFieldReportsOrigin(t *testing.T) {
	type fixture struct {
		name        string
		wantSource  string
		wantContent string
	}
	cases := []struct {
		label   string
		setup   func(t *testing.T) (LoadOptions, fixture)
	}{
		{
			label: "workspace-only",
			setup: func(t *testing.T) (LoadOptions, fixture) {
				ws := t.TempDir()
				home := t.TempDir()
				writeSkillFixture(t, ws, "alpha", "WS-ONLY-alpha\n")
				return LoadOptions{WorkspaceDir: ws, HomeDir: home}, fixture{
					name: "alpha", wantSource: "workspace", wantContent: "WS-ONLY-alpha",
				}
			},
		},
		{
			label: "global-only",
			setup: func(t *testing.T) (LoadOptions, fixture) {
				ws := t.TempDir()
				home := t.TempDir()
				writeSkillFixture(t, home, "beta", "HOME-ONLY-beta\n")
				return LoadOptions{WorkspaceDir: ws, HomeDir: home}, fixture{
					name: "beta", wantSource: "global", wantContent: "HOME-ONLY-beta",
				}
			},
		},
		{
			label: "override-only",
			setup: func(t *testing.T) (LoadOptions, fixture) {
				overrideDir := t.TempDir()
				dir := filepath.Join(overrideDir, "gamma")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
					[]byte("OVERRIDE-ONLY-gamma\n"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				return LoadOptions{SkillsDir: overrideDir}, fixture{
					name: "gamma", wantSource: "override", wantContent: "OVERRIDE-ONLY-gamma",
				}
			},
		},
		{
			label: "collision-workspace-wins",
			setup: func(t *testing.T) (LoadOptions, fixture) {
				ws := t.TempDir()
				home := t.TempDir()
				writeSkillFixture(t, ws, "delta", "WS-COLLISION-delta\n")
				writeSkillFixture(t, home, "delta", "HOME-COLLISION-delta\n")
				return LoadOptions{WorkspaceDir: ws, HomeDir: home}, fixture{
					name: "delta", wantSource: "workspace", wantContent: "WS-COLLISION-delta",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			opts, fx := tc.setup(t)
			got, err := Load(fx.name, opts)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if got.Source != fx.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, fx.wantSource)
			}
			if !strings.Contains(got.Content, fx.wantContent) {
				t.Errorf("Content missing %q; got %q", fx.wantContent, got.Content)
			}
			if got.Name != fx.name {
				t.Errorf("Name = %q, want %q", got.Name, fx.name)
			}
		})
	}
}

// --- Run 021 / handoff 068: Available accessor seam ---

// TestAvailable_ReturnsAllSkills pins the SCOPE §45 binding: a
// skill under each search root (workspace + global + SkillsDir
// override) is enumerated with the correct Name + Source +
// Description. Workspace + global + override each contribute at
// least one skill with distinguishable content; all three appear
// in the result.
func TestAvailable_ReturnsAllSkills(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	overrideDir := t.TempDir()

	writeSkillFixture(t, ws, "alpha", "# Alpha\n\nWS-ALPHA-CONTENT\n")
	writeSkillFixture(t, home, "beta", "# Beta heading\nHOME-BETA-CONTENT\n")

	// override: no .simple-harness prefix
	overrideDirSkill := filepath.Join(overrideDir, "gamma")
	if err := os.MkdirAll(overrideDirSkill, 0o755); err != nil {
		t.Fatalf("mkdir override: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overrideDirSkill, "SKILL.md"),
		[]byte("# Gamma heading\n\nOVERRIDE-GAMMA-CONTENT\n"), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	// First: ws + home (no override).
	got, err := Available(LoadOptions{WorkspaceDir: ws, HomeDir: home})
	if err != nil {
		t.Fatalf("Available returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Available returned %d skills, want 2 (got %+v)", len(got), got)
	}

	byName := make(map[string]Skill)
	for _, s := range got {
		byName[s.Name] = s
	}
	if s, ok := byName["alpha"]; !ok {
		t.Errorf("Available missing alpha (got %+v)", byName)
	} else {
		if s.Source != "workspace" {
			t.Errorf("alpha.Source = %q, want %q", s.Source, "workspace")
		}
		if !strings.Contains(s.Description, "Alpha") {
			t.Errorf("alpha.Description = %q, want to contain %q", s.Description, "Alpha")
		}
	}
	if s, ok := byName["beta"]; !ok {
		t.Errorf("Available missing beta (got %+v)", byName)
	} else {
		if s.Source != "global" {
			t.Errorf("beta.Source = %q, want %q", s.Source, "global")
		}
		if !strings.Contains(s.Description, "Beta") {
			t.Errorf("beta.Description = %q, want to contain %q", s.Description, "Beta")
		}
	}

	// Second: with override (REPLACES both roots).
	got2, err := Available(LoadOptions{SkillsDir: overrideDir})
	if err != nil {
		t.Fatalf("Available (override) returned error: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("Available (override) returned %d skills, want 1 (got %+v)", len(got2), got2)
	}
	if got2[0].Name != "gamma" {
		t.Errorf("override[0].Name = %q, want %q", got2[0].Name, "gamma")
	}
	if got2[0].Source != "override" {
		t.Errorf("override[0].Source = %q, want %q", got2[0].Source, "override")
	}
}

// TestAvailable_WorkspaceWinsCollision pins the SCOPE §15
// workspace-wins-on-collision rule AT THE ENUMERATION LEVEL (not
// just the Load level): when the same skill name exists under both
// workspace and global, Available returns the workspace's entry
// only — the global's entry is shadowed (no duplication).
func TestAvailable_WorkspaceWinsCollision(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	wsHeading := "WS-WINS-AVAILABLE-PIN-aaaa"
	homeHeading := "HOME-LOSES-AVAILABLE-PIN-bbbb"
	writeSkillFixture(t, ws, "cold-start", "# "+wsHeading+"\nWS-content\n")
	writeSkillFixture(t, home, "cold-start", "# "+homeHeading+"\nHOME-content\n")

	got, err := Available(LoadOptions{WorkspaceDir: ws, HomeDir: home})
	if err != nil {
		t.Fatalf("Available returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Available returned %d skills, want 1 (collision must drop the global copy; got %+v)", len(got), got)
	}
	if got[0].Name != "cold-start" {
		t.Errorf("Name = %q, want %q", got[0].Name, "cold-start")
	}
	if got[0].Source != "workspace" {
		t.Errorf("Source = %q, want %q (workspace wins on collision per SCOPE §15)", got[0].Source, "workspace")
	}
	if !strings.Contains(got[0].Description, wsHeading) {
		t.Errorf("Description missing workspace heading %q; got %q", wsHeading, got[0].Description)
	}
	if strings.Contains(got[0].Description, homeHeading) {
		t.Errorf("Description contains global heading %q (workspace should win); got %q", homeHeading, got[0].Description)
	}
}

// TestAvailable_EmptyWorkspace_ReturnsEmptyList pins the "no
// skills is a valid state" semantics: a workspace + home that
// contain no skills directory returns an empty (non-nil) slice
// with a nil error. The Available call must not panic on missing
// root directories.
func TestAvailable_EmptyWorkspace_ReturnsEmptyList(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()

	got, err := Available(LoadOptions{WorkspaceDir: ws, HomeDir: home})
	if err != nil {
		t.Fatalf("Available returned error: %v", err)
	}
	if got == nil {
		t.Errorf("Available returned nil slice, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("Available returned %d skills, want 0 (got %+v)", len(got), got)
	}
}
