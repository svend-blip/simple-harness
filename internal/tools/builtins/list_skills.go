// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Run 021 / handoff 068 adds list_skills: the
// model-invoked surface for SCOPE §45 ("lists the skills
// discoverable under §15's locations") + SCOPE §15's workspace-
// wins collision order. The tool is READ-ONLY per reviewer duty
// §1: no write/create/remove calls anywhere in this file.
package builtins

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/svend-blip/simple-harness/internal/skill"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// ListSkills is the list_skills builtin tool. It enumerates the
// skills discoverable under SCOPE §15's locations with workspace-
// wins collision order preserved. The tool is READ-ONLY — no
// write/create/remove calls (reviewer duty §1 binding). The
// discovery uses internal/skill.Available, the run-021 handoff-068
// read-only accessor seam.
//
// The tool's permission gating is handled by the standard
// tools.Registry.Dispatch pipeline (SCOPE §13 — schema → path →
// policy). READ_ONLY mode permits the call (it is a read).
type ListSkills struct{}

// Meta implements tools.Tool.
func (ListSkills) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "list_skills",
		Description: "List the skills discoverable under SCOPE §15's locations, with each skill's source location. Workspace-wins collision order preserved.",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields — write-attempt-shaped inputs
// (e.g. {"name": ..., "content": ..., "path": ...}) are rejected
// by the schema validator before the tool's Execute runs.
func (ListSkills) Schema() tools.Schema {
	return tools.Schema{
		Properties: map[string]tools.PropertyType{
			"skills_dir": tools.TypeString,
		},
	}
}

// SkillInfo is one entry in the list_skills result. JSON tags
// match the wire format (the JSON the tool emits and the JSON a
// caller parses by name).
//
// Source is one of "workspace" | "global" | "override" — the
// same value internal/skill.Skill.Source carries (so callers can
// pattern-match against either spelling).
// Description is a single-line summary derived from the SKILL.md
// body (the first heading or first non-empty line).
type SkillInfo struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

// ListSkillsResult is the success content shape. Result.Content
// on success carries a *ListSkillsResult; the JSON tags above
// drive the wire format.
type ListSkillsResult struct {
	Skills []SkillInfo `json:"skills"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract optional `skills_dir` argument (the --skills-dir
//     override). When present, it REPLACES both default roots
//     (workspace + global), matching the Load semantics.
//  2. Resolve workspace + HOME roots from environment (HOME for
//     HomeDir; PWD for WorkspaceDir).
//  3. Call skill.Available(LoadOptions{...}) and translate the
//     returned []Skill into []SkillInfo (the wire shape).
//  4. Sort by Name for deterministic output.
//  5. Return Result{Status: "ok", Content: ListSkillsResult{Skills: ...}}.
//
// On any internal/skill.Available error (a hard filesystem
// failure), returns Result{Status: "error", Error:
// &ToolError{Kind: "execution_failed", ...}}.
//
// The call is permitted under READ_ONLY mode (it is a read).
func (ListSkills) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	_ = ctx // ctx is reserved for future cancellation hooks; today's discovery does not honor it.

	// Resolve workspace + HOME roots. The transport-supplied
	// workspace handle is not exposed via the tools.Call
	// contract today — the binding surface for this handoff is
	// HOME + PWD (the same surface the --workspace / --skills-dir
	// CLI flags ultimately resolve against). A future Run can
	// extend the tools.Call contract to thread an explicit
	// workspace handle.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("list_skills: cannot determine home directory: %v", err),
			Call:    call,
		}}, nil
	}

	workspaceDir, err := os.Getwd()
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("list_skills: cannot determine workspace directory: %v", err),
			Call:    call,
		}}, nil
	}

	opts := skill.LoadOptions{
		WorkspaceDir: workspaceDir,
		HomeDir:      homeDir,
	}

	// Optional skills_dir override.
	if v, present := call.Arguments["skills_dir"]; present {
		if s, ok := v.(string); ok && s != "" {
			opts.SkillsDir = s
			// When override is set, WorkspaceDir + HomeDir
			// are NOT consulted (matches Load semantics).
			opts.WorkspaceDir = ""
			opts.HomeDir = ""
		}
	}

	skills, err := skill.Available(opts)
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("list_skills: %v", err),
			Call:    call,
		}}, nil
	}

	out := make([]SkillInfo, 0, len(skills))
	for _, s := range skills {
		out = append(out, SkillInfo{
			Name:        s.Name,
			Source:      s.Source,
			Description: s.Description,
		})
	}

	// Sort by Name for deterministic output (the wire surface
	// must be stable for the model + for tests).
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return tools.Result{Status: "ok", Content: &ListSkillsResult{Skills: out}}, nil
}