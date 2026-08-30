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
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill is the loaded representation of a reusable instruction
// package (SCOPE §15, §16). The Content is the raw SKILL.md file
// body — consumers compose it into the model context per SCOPE §14.
// Source records which search root produced the Skill ("workspace",
// "global", or "override") for inspection and testing.
//
// Description is a single-line summary derived from the SKILL.md
// body (the first heading or first non-empty line). It is populated
// by Available (the read-only enumeration seam); Load leaves it
// empty (Load only resolves the skill by name, it does not
// enumerate). List consumers (the list_skills builtin tool) read
// the Description; the load_skill builtin tool reads Content.
type Skill struct {
	Name        string
	Content     string
	Source      string
	Description string
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

// Available enumerates all skills discoverable under the active
// search roots (opts.SkillsDir if non-empty, else
// opts.WorkspaceDir + opts.HomeDir), in workspace-wins collision
// order. Each Skill carries its Name + Source + Description (the
// first heading or first non-empty line of SKILL.md). Available
// is READ-ONLY: it calls os.ReadDir + os.ReadFile only; no
// write/create/remove. Reviewer duty §1 binds.
//
// Returns an error only on a hard filesystem failure (e.g.
// permission denied); a missing skills directory is treated as
// empty (matches the existing Load semantics — no skills is a
// valid state).
//
// The Description extraction: the first "# heading" line of the
// SKILL.md (after `#` stripping + whitespace trimming); if no "#
// heading" is present, fall back to the first non-empty line of
// the file. The Description is bounded to a single line (replace
// any '\n' with a space). If the SKILL.md is empty, Description is
// "". A read failure on an individual SKILL.md is non-fatal — the
// skill is silently dropped (matches the "missing root = empty
// list" semantics; the operator discovers the broken skill on
// their next `list_skills` or `load_skill` attempt).
//
// Run 021 / handoff 068 introduces Available as the read-only
// accessor seam for the model-invoked list_skills + load_skill
// builtin tools (SCOPE §45). The function is importable from
// internal/tools/builtins/ per the GOAL §4 fence. The architectural
// boundary of internal/skill/ is unchanged — the package remains
// the LOAD PATH, not the consumer of file names (SCOPE §16 holds).
func Available(opts LoadOptions) ([]Skill, error) {
	out := make([]Skill, 0)

	// SkillsDir override (when set, REPLACES both workspace +
	// global roots — same semantics as Load).
	if opts.SkillsDir != "" {
		descs, err := readSkillsFromDir(opts.SkillsDir, "override", false)
		if err != nil {
			return nil, err
		}
		out = append(out, descs...)
		return out, nil
	}

	// Workspace first — workspace wins on collision
	// (SCOPE §15 binding).
	var wsSkills []Skill
	if opts.WorkspaceDir != "" {
		descs, err := readSkillsFromDir(filepath.Join(opts.WorkspaceDir, ".simple-harness", "skills"), "workspace", false)
		if err != nil {
			return nil, err
		}
		wsSkills = descs
	}

	// Global second — when a name collides with workspace, the
	// workspace's entry wins and the global's entry is dropped
	// (dedup by Name; workspace first → its Source wins).
	wsNames := make(map[string]struct{}, len(wsSkills))
	for _, s := range wsSkills {
		wsNames[s.Name] = struct{}{}
	}
	out = append(out, wsSkills...)

	if opts.HomeDir != "" {
		descs, err := readSkillsFromDir(filepath.Join(opts.HomeDir, ".simple-harness", "skills"), "global", true)
		if err != nil {
			return nil, err
		}
		for _, s := range descs {
			if _, collides := wsNames[s.Name]; collides {
				continue // workspace wins — drop the global entry
			}
			out = append(out, s)
		}
	}

	return out, nil
}

// readSkillsFromDir enumerates the immediate subdirectories of
// skillsDir and, for each subdirectory containing a SKILL.md file,
// returns a Skill with the subdirectory's name + the SKILL.md's
// derived Description. The source argument is the Source value
// stamped on each returned Skill ("workspace" | "global" |
// "override"). When `skipMissing` is true, a missing skillsDir is
// treated as empty (no error); when false, a missing skillsDir is
// also non-fatal — but the function's behaviour matches the
// "missing root = empty list" Load semantics either way.
//
// Per-entry read failures (a SKILL.md that exists but cannot be
// read) are non-fatal — the skill is silently dropped. A
// directory itself that cannot be enumerated returns a non-nil
// error (a hard filesystem failure).
func readSkillsFromDir(skillsDir, source string, skipMissing bool) ([]Skill, error) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) && skipMissing {
			return nil, nil
		}
		if os.IsNotExist(err) && !skipMissing {
			// override root missing is a configuration
			// oddity but not a hard failure — return
			// empty list with no error (matches Load's
			// "all roots empty" semantics).
			return nil, nil
		}
		return nil, fmt.Errorf("read skill directory %s: %w", skillsDir, err)
	}

	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(skillsDir, name, "SKILL.md")
		content, err := os.ReadFile(path)
		if err != nil {
			// Per-entry failure is non-fatal — the operator
			// discovers the broken skill on their next
			// list_skills / load_skill attempt.
			if os.IsNotExist(err) {
				continue
			}
			continue
		}
		desc := extractDescription(string(content))
		out = append(out, Skill{
			Name:        name,
			Content:     string(content),
			Source:      source,
			Description: desc,
		})
	}
	return out, nil
}

// extractDescription derives a single-line Description from the
// SKILL.md body. The first `# heading` line wins (after `#`
// stripping + whitespace trimming); if no `# heading` is present,
// the first non-empty line wins. Multi-line content is collapsed
// to a single line (any '\n' becomes a space). An empty body
// produces an empty Description.
func extractDescription(body string) string {
	if body == "" {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if heading == "" {
				continue
			}
			return strings.ReplaceAll(heading, "\n", " ")
		}
		// First non-empty line as fallback.
		return strings.ReplaceAll(line, "\n", " ")
	}
	return ""
}
