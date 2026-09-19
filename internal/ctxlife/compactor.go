package ctxlife

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

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
	// ReasoningEffort is the compaction request's own
	// reasoning_effort (config: context.compaction_reasoning_effort).
	// Empty means "none", falling back to the model's own effort if
	// the endpoint refuses the value; EffortInherit means the model's
	// own from the start; anything else is sent as given. Measured
	// with "none": 39.5 s -> 8.6 s on Ollama (qwen3.6-27b), 43.7 s ->
	// 15.4 s on FreeToken (Qwen3.8-Flash-Next).
	ReasoningEffort string
	// noneRefused remembers that the endpoint refused "none".
	noneRefused atomic.Bool
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

// SummaryCapFactor and ReasoningAllowance make up the output cap of a
// sized compaction request: target x SummaryCapFactor + ReasoningAllowance.
//
// The factor is for the summary itself: the target is stated to the
// model in tokens it cannot count, and the harness's estimate runs below
// the runtime's count (~9 % measured on code), so the cap bounds the cost
// without cutting a summary that is merely a little long.
//
// The allowance is for reasoning, which a reasoning model spends out of
// the same cap before the summary begins. Measured on qwen3.6-27b
// through Ollama: ~1 500 reasoning tokens ahead of a 563-token summary,
// and a cap of twice the target returned no summary at all. When the
// compaction's own ReasoningEffort is "none" there is none to allow for,
// and the allowance is dropped.
const (
	SummaryCapFactor   = 2
	ReasoningAllowance = 4096
)

// EffortInherit as ModelCompactor.ReasoningEffort means the model's own
// configured effort, which is what an empty value meant before "none"
// became the default.
const EffortInherit = "inherit"

// refusedAsInvalid reports whether the endpoint rejected the request as
// malformed — the answer to a parameter value it does not accept.
func refusedAsInvalid(err error) bool {
	var me *model.ModelError
	if !errors.As(err, &me) || me.Kind != model.ErrHTTP {
		return false
	}
	return me.StatusCode == 400 || me.StatusCode == 422
}

// Compact implements Compactor.
func (c *ModelCompactor) Compact(messages []model.Message) (string, error) {
	return c.CompactWithin(messages, 0)
}

// CompactWithin implements SizedCompactor. A target above zero is stated
// in the instruction and bounds the request's output at
// SummaryCapFactor times itself.
func (c *ModelCompactor) CompactWithin(messages []model.Message, target int) (string, error) {
	instruction := c.Instruction
	if instruction == "" {
		instruction = CompactionInstruction
	}
	ctx := c.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if target > 0 {
		instruction += fmt.Sprintf("\n\nThe whole summary must be at most %d tokens (about %d words). "+
			"Leave a heading out when there is nothing to put under it.", target, target*3/4)
	}
	req := model.ChatRequest{Messages: []model.Message{
		{Role: "system", Content: instruction},
		{Role: "user", Content: renderForCompaction(messages)},
	}}
	// The reasoning effort of this request. Unset means "none", with
	// one fallback: an endpoint that validates the value and does not
	// know this one refuses the request (FreeToken answers 400 to
	// values it does not know), and the compaction is then repeated
	// with the model's own effort. The refusal is remembered, so a
	// session pays for it once. A value set in the configuration is
	// used as given, and a refusal of it is an error: a setting that
	// does not work should say so.
	effort, auto := c.ReasoningEffort, false
	switch {
	case effort == EffortInherit:
		effort = ""
	case effort == "":
		auto = !c.noneRefused.Load()
		if auto {
			effort = "none"
		}
	}
	shape := func(effort string) {
		req.ReasoningEffort = effort
		if target > 0 {
			req.MaxTokens = target * SummaryCapFactor
			if effort != "none" {
				req.MaxTokens += ReasoningAllowance
			}
		}
	}
	shape(effort)

	// Announced once: a refused attempt is answered in milliseconds
	// and is not an inference, and an event stream with a
	// model_request that never gets its usage misleads whoever pairs
	// them.
	if c.OnRequest != nil {
		c.OnRequest()
	}
	var out strings.Builder
	cutOff := false
	send := func() error {
		out.Reset()
		cutOff = false
		return c.Client.ChatStream(ctx, req, func(ev model.StreamEvent) error {
			out.WriteString(ev.Delta)
			if ev.FinishReason == "length" {
				cutOff = true
			}
			if ev.Usage != nil && c.OnUsage != nil {
				c.OnUsage(ev.Usage)
			}
			return nil
		})
	}
	err := send()
	if err != nil && auto && refusedAsInvalid(err) {
		c.noneRefused.Store(true)
		shape("")
		err = send()
	}
	if err != nil {
		return "", fmt.Errorf("ctxlife: the compaction request failed: %w", err)
	}
	if cutOff {
		// A summary that stops mid-sentence at the output cap is
		// missing whatever came last — by the instruction's order,
		// the next step. It is refused rather than used.
		if strings.TrimSpace(out.String()) == "" {
			return "", fmt.Errorf("ctxlife: the compaction was cut off at the output cap "+
				"(max_tokens %d) before any summary text arrived — a reasoning model "+
				"spends the cap on reasoning first; context.compaction_reasoning_effort "+
				"sets the compaction request's own effort", req.MaxTokens)
		}
		return "", fmt.Errorf("ctxlife: the compaction summary was cut off at the output cap (max_tokens %d)", req.MaxTokens)
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
