package ctxlife

import (
	"context"
	"fmt"
	"strings"

	"github.com/svend-blip/simple-harness/internal/model"
)

// ModelCompactor turns older conversation into a working summary by
// asking a model. §10 allows the active model or another configured
// one; which it is, is the caller's choice and this type does not
// care.
//
// The summary is working memory, not a record. §10 and §12 are
// explicit that it must not become authoritative project state, and
// the instruction below says so to the model as well as to the
// reader: a compactor that quietly invited the model to restate
// project status would produce exactly the thing the addendum
// forbids, and it would look like a good summary while doing it.
type ModelCompactor struct {
	Client *model.Client
	// Instruction overrides the default prompt. Empty uses
	// CompactionInstruction.
	Instruction string
	// Ctx is the context for the compaction request. Nil means
	// context.Background().
	Ctx context.Context
	// OnRequest is called before each compaction inference and
	// OnUsage with the usage the model reported, when it did. A
	// compaction is an inference like any other; without these the
	// harness emitted no model_request or usage event for it and a
	// measurement counting those undercounted.
	OnRequest func()
	OnUsage   func(*model.Usage)
}

// CompactionInstruction is §11's list, as an instruction. It names
// what must survive and what should go, and it states the boundary
// that keeps a summary from being mistaken for project truth.
const CompactionInstruction = `You are compacting the older part of a working session so it fits in a
bounded context. Produce a compact working summary under these headings,
omitting any heading with nothing to say:

Objective
Constraints
Important findings
Decisions relevant to current work
Files or components affected
Tests and validation state
Known failures or unresolved issues
Current working state
Immediate next action

Keep: the current objective, constraints, unresolved issues, decisions that
still bind, resources modified, validation state, and what happens next.

Omit: conversational repetition, superseded hypotheses, obsolete intermediate
reasoning, large raw tool outputs whose result is already reflected in later
state, and redundant explanation.

This summary is lossy working memory, not a project record. Do not restate
project status as if it were authoritative, and do not invent anything that
is not in the material below. Write only the summary.`

// Compact implements Compactor.
func (c *ModelCompactor) Compact(messages []model.Message) (string, error) {
	if c.Client == nil {
		return "", fmt.Errorf("ctxlife: no model client to compact with")
	}
	if len(messages) == 0 {
		return "", fmt.Errorf("ctxlife: nothing to compact")
	}
	instruction := c.Instruction
	if instruction == "" {
		instruction = CompactionInstruction
	}
	ctx := c.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	req := model.ChatRequest{Messages: []model.Message{
		{Role: "system", Content: instruction},
		{Role: "user", Content: renderForCompaction(messages)},
	}}

	if c.OnRequest != nil {
		c.OnRequest()
	}
	var out strings.Builder
	err := c.Client.ChatStream(ctx, req, func(ev model.StreamEvent) error {
		out.WriteString(ev.Delta)
		if ev.Usage != nil && c.OnUsage != nil {
			c.OnUsage(ev.Usage)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ctxlife: the compaction request failed: %w", err)
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", fmt.Errorf("ctxlife: the compaction request returned nothing")
	}
	return summary, nil
}

// renderForCompaction flattens the span into something a model can
// read. Tool results that were already pruned are rendered as the
// placeholder rather than dropped, so the summary can say that a
// result existed and was large — which is sometimes the only thing
// worth knowing about it.
func renderForCompaction(messages []model.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "tool":
			b.WriteString("TOOL RESULT: ")
		case "assistant":
			b.WriteString("ASSISTANT: ")
		case "user":
			b.WriteString("USER: ")
		default:
			b.WriteString(strings.ToUpper(msg.Role) + ": ")
		}
		b.WriteString(msg.Content)
		for _, call := range msg.ToolCalls {
			args := call.ArgumentsRaw
			if args == "" && len(call.Arguments) > 0 {
				args = fmt.Sprint(call.Arguments)
			}
			fmt.Fprintf(&b, "\n  called %s(%s)", call.Name, truncate(args, 200))
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
