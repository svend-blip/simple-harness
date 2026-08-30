// Package builtins ships the concrete Tool implementations Simple
// Harness registers against the foundation's tool registry
// (internal/tools). Run 021 / handoff 068 adds load_skill: the
// model-invoked surface for SCOPE §45 ("loads a named skill's
// instruction material into the next model request's context").
// The tool is READ-ONLY per reviewer duty §1: no write/create/
// remove calls anywhere in this file.
package builtins

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/svend-blip/simple-harness/internal/skill"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// LoadSkill is the load_skill builtin tool. It loads a named
// skill's instruction material per SCOPE §45 + SCOPE §14's
// deterministic ordering. The tool is READ-ONLY — no write/
// create/remove calls (reviewer duty §1 binding).
//
// Unknown skill name returns a structured tool failure
// (Kind: "skill_not_found"); the model can call list_skills to
// discover what's available. The structured failure is bound by
// the SCOPE §21 event wire shape — the model sees the
// tool_result with the structured error and the next turn can
// retry (or call list_skills to discover the available set).
//
// The call is permitted under READ_ONLY mode (it is a read).
type LoadSkill struct{}

// Meta implements tools.Tool.
func (LoadSkill) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name:        "load_skill",
		Description: "Load a named skill's instruction material into the next model request's context, per SCOPE §14's deterministic ordering. Returns the skill's content + its source location. Loading an already-loaded skill is idempotent (no duplication in context).",
	}
}

// Schema implements tools.Tool. `name` is required (string);
// `skills_dir` is optional (string, override for both roots).
// The AdditionalProperties=false default rejects write-attempt-
// shaped inputs (e.g. {"name": ..., "content": ..., "path": ...}).
func (LoadSkill) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"name"},
		Properties: map[string]tools.PropertyType{
			"name":       tools.TypeString,
			"skills_dir": tools.TypeString,
		},
	}
}

// LoadSkillResult is the success content shape. Result.Content
// on success carries a *LoadSkillResult; the JSON tags drive the
// wire format.
type LoadSkillResult struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Content string `json:"content"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract required `name` argument (string).
//  2. Extract optional `skills_dir` argument (string, override).
//  3. Resolve workspace + HOME roots from environment (HOME for
//     HomeDir; PWD for WorkspaceDir).
//  4. Call skill.Load(name, LoadOptions{...}).
//  5. On success → Result{Status: "ok", Content: LoadSkillResult{Name, Source, Content}}.
//  6. On errors.Is(err, skill.ErrSkillNotFound) → Result{Status: "error", Error: &ToolError{Kind: "skill_not_found", ...}}.
//  7. On any other error → Result{Status: "error", Error: &ToolError{Kind: "execution_failed", ...}}.
func (LoadSkill) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	_ = ctx // reserved for future cancellation hooks; today's load is synchronous.

	// Required `name`.
	nameVal, ok := call.Arguments["name"].(string)
	if !ok || nameVal == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "load_skill: missing or non-string name argument",
			Call:    call,
		}}, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("load_skill: cannot determine home directory: %v", err),
			Call:    call,
		}}, nil
	}

	workspaceDir, err := os.Getwd()
	if err != nil {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("load_skill: cannot determine workspace directory: %v", err),
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
			opts.WorkspaceDir = ""
			opts.HomeDir = ""
		}
	}

	s, err := skill.Load(nameVal, opts)
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return tools.Result{Status: "error", Error: &tools.ToolError{
				Kind:    "skill_not_found",
				Message: fmt.Sprintf("load_skill: skill %q not found under SCOPE §15's locations; call list_skills to see what's discoverable", nameVal),
				Call:    call,
			}}, nil
		}
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "execution_failed",
			Message: fmt.Sprintf("load_skill: %v", err),
			Call:    call,
		}}, nil
	}

	return tools.Result{Status: "ok", Content: &LoadSkillResult{
		Name:    s.Name,
		Source:  s.Source,
		Content: s.Content,
	}}, nil
}