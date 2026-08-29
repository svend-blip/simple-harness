// Package skill implements the Simple Harness skill discovery +
// loader (SCOPE §15, §16).
//
// A skill is a directory containing a SKILL.md file with reusable
// instructions or context. V1 skills are PURE INSTRUCTION INJECTORS:
// the loader reads the file's bytes and returns them; consumers
// compose the content into the model context per SCOPE §14. There is
// no code execution, no marketplace, no plugin registry, no script
// interpreter, no RPC — a skill is a markdown file with a name. The
// Go code in this package never imports os/exec, never calls
// plugin.Open, never evaluates the file as code. Reviewer duty #3
// (no plugin creep) is structural here, not behavioral.
//
// Two search roots are recognised by default:
//
//	1. <workspace>/.simple-harness/skills/<name>/SKILL.md   (workspace)
//	2. ~/.simple-harness/skills/<name>/SKILL.md             (global)
//
// The workspace root wins on collision (SCOPE §15 — projects can
// override a globally-shipped skill without modifying the global
// location). The HOME root is a fallback for skills shipped with the
// harness binary or installed by the operator.
//
// The third search root is the test-only deterministic override
// (GOAL §2): when LoadOptions.SkillsDir is non-empty, it REPLACES
// BOTH default search roots. Tests use this to point the loader at a
// temporary directory without touching the real workspace or HOME
// locations; the runtime CLI exposes the same override as the
// --skills-dir <DIR> flag.
//
// SCOPE §16 contract: startup file names ("README", "GOAL.md",
// "DIRECTION.md", etc.) live ONLY in skill files. The harness
// runtime never hardcodes them. This package is the LOAD PATH, not
// the consumer of the file names — internal/skill's own source code
// may mention SCOPE §16 by reference, but the names it actually
// reads are the directory layout <dir>/<name>/SKILL.md, not project
// filenames. The cmd/simple-harness source tree is verified by the
// TestSkill_NoStartupNamesInRuntime grep-negative test in
// cmd/simple-harness/skill_test.go.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Skill is the loaded representation of a reusable instruction
// package (SCOPE §15, §16). The Content is the raw SKILL.md file
// body — consumers compose it into the model context per SCOPE §14.
// Source records which search root produced the Skill ("workspace",
// "global", or "override") for inspection and testing.
type Skill struct {
	Name    string
	Content string
	// Source records which search root produced this Skill. One of:
	//   "workspace" — <workspace>/.simple-harness/skills/<name>/SKILL.md
	//   "global"    — <HomeDir>/.simple-harness/skills/<name>/SKILL.md
	//   "override"  — <SkillsDir>/<name>/SKILL.md (the --skills-dir / GOAL §2 override)
	Source string
}

// LoadOptions configures the search roots for skill discovery.
//
// SkillsDir, when non-empty, REPLACES BOTH the workspace and global
// roots — it is the test-only deterministic handle per GOAL §2 (and
// the --skills-dir DIR flag's effect). WorkspaceDir / HomeDir supply
// the default search roots when SkillsDir is empty.
//
// An empty WorkspaceDir is treated as "no workspace root active"
// (the workspace search is skipped). An empty HomeDir is treated as
// "no global root active" (the global search is skipped). When
// SkillsDir is non-empty, WorkspaceDir and HomeDir are ignored —
// the override is the sole search root.
type LoadOptions struct {
	SkillsDir    string
	WorkspaceDir string
	HomeDir      string
}

// ErrSkillNotFound is the sentinel returned when a skill name
// resolves to no file under any of the active search roots.
// Callers compare with errors.Is to detect "unknown skill".
var ErrSkillNotFound = errors.New("skill not found")

// Load resolves name to a Skill by searching the active search
// roots in this order:
//
//  1. opts.SkillsDir (the --skills-dir override), if non-empty
//  2. opts.WorkspaceDir + "/.simple-harness/skills/" (workspace wins)
//  3. opts.HomeDir + "/.simple-harness/skills/" (global fallback)
//
// The order respects SCOPE §15's workspace-wins-on-collision rule:
// when the same skill name exists under both the workspace and the
// global root, the workspace's file is returned and the global's
// file is shadowed.
//
// An empty name returns an error wrapping a descriptive message
// (the error is NOT ErrSkillNotFound — the empty-name case is a
// distinct validation failure, not a "searched and missed" case).
//
// A name not found under ANY root returns ErrSkillNotFound (wrapped
// with a brief descriptive context for log readability; callers
// MUST use errors.Is to detect it).
//
// Any other read error (permission denied, I/O error on a found
// file, etc.) is wrapped with the path so the caller can log it.
//
// The function is the single seam for skill discovery this Run —
// composition (loading the skill's Content into the model context)
// is the caller's job and lands on a future handoff. This package
// is a leaf: it does NOT import any other internal/ package.
func Load(name string, opts LoadOptions) (*Skill, error) {
	if name == "" {
		return nil, fmt.Errorf("skill name is empty")
	}

	// Order matters: SCOPE §15 workspace-wins-on-collision.
	// SkillsDir is the test-only override (GOAL §2) and REPLACES
	// both default roots when set — when SkillsDir is non-empty,
	// the workspace and global roots are NOT searched.
	if opts.SkillsDir != "" {
		path := filepath.Join(opts.SkillsDir, name, "SKILL.md")
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("skill %q not found under override %s: %w", name, opts.SkillsDir, ErrSkillNotFound)
			}
			return nil, fmt.Errorf("read skill %q at %s: %w", name, path, err)
		}
		return &Skill{
			Name:    name,
			Content: string(content),
			Source:  "override",
		}, nil
	}

	if opts.WorkspaceDir != "" {
		path := filepath.Join(opts.WorkspaceDir, ".simple-harness", "skills", name, "SKILL.md")
		content, err := os.ReadFile(path)
		if err == nil {
			return &Skill{
				Name:    name,
				Content: string(content),
				Source:  "workspace",
			}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read skill %q at %s: %w", name, path, err)
		}
		// Not found in workspace — fall through to the global root.
	}

	if opts.HomeDir != "" {
		path := filepath.Join(opts.HomeDir, ".simple-harness", "skills", name, "SKILL.md")
		content, err := os.ReadFile(path)
		if err == nil {
			return &Skill{
				Name:    name,
				Content: string(content),
				Source:  "global",
			}, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("skill %q not found in workspace or global: %w", name, ErrSkillNotFound)
		}
		return nil, fmt.Errorf("read skill %q at %s: %w", name, path, err)
	}

	// No active search root produced a result and no specific
	// not-found error was emitted by either branch — this is the
	// "all roots empty / all roots missed" path.
	return nil, fmt.Errorf("skill %q not found: %w", name, ErrSkillNotFound)
}
