// Package loop is the minimal single-turn model loop for Simple
// Harness. It is the architecture's "internal/loop/" component
// (docs/ARCHITECTURE.md lines 275-294), reduced to its V1 minimum:
// take one prompt, stream one response, surface *model.ModelError
// to the caller so the cmd can map it to a SCOPE §28 exit code.
//
// V1 loop does NOT include: tool dispatch, multi-turn loops with
// message-history accumulation, permission enforcement, retries,
// or session/message persistence. The architecture's full SCOPE §3
// multi-turn loop with tool dispatch is deferred to later Runs (Run
// 003+ lands tools; Run 004 lands sessions; the loop-with-tools
// integration lands after both). This handoff ships the smallest
// vertical slice that satisfies SCOPE §§3-4 (interactive mode) and
// §§21-23 (event protocol subset) with the four events the loop
// emits in V1: started, status: STREAMING, assistant_stream,
// status: COMPLETED, completed.
//
// The loop is one-way dependent on internal/model and internal/event
// (it does NOT import them in the reverse direction). The model
// client itself remains a leaf with no awareness of events or
// loops, per ARCHITECTURE.md §"internal/model/"(b).
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/skill"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// HarnessSystem is the canonical minimal harness system prompt
// prepended to every composed message list per SCOPE §14 step 1.
// It is the harness's identity + posture, not project-specific
// content; the project-specific material lives in the skills slot
// (step 3) and the external system slot (step 2). The string is
// intentionally short — it is the harness's voice, not the
// project's content. The cmd imports this const (no string
// duplication) and threads it through loop.Config.System.
const HarnessSystem = "You are running inside Simple Harness, a small terminal-first execution kernel. Respond concisely and complete the user's request. Use the system, skills, and user input below as the full context for this turn; do not assume project state beyond what the loaded skills and external governance describe."

// Config is the seam between the cmd and the loop. The cmd builds
// this from the resolved config.Config (workspace, permission) and
// from the CLI flags; the loop turns it into a model.Options (the
// OpenAI-compat surface the model client needs) and into a session
// identity (the fields stamped onto the `started` event's config
// block — model, endpoint, workspace, permission).
type Config struct {
	// Model is the OpenAI-compat surface the model client consumes.
	// The cmd builds this from cfg.Model and the loop-normalized
	// BaseURL — see NormalizeBaseURL.
	Model model.Options
	// Workspace is the loop's session-identity workspace. The cmd
	// wires this from the --workspace flag (or cwd default).
	Workspace string
	// Permission is the loop's session-identity permission mode.
	// One of "READ_ONLY", "WORKSPACE_WRITE", "FULL_ACCESS". The
	// cmd validates this against the SCOPE §12 enum before the
	// loop sees it (so the loop can trust the value).
	Permission string
	// System is the minimal harness system prompt per SCOPE §14
	// step 1. The loop prepends it as the FIRST message. The
	// string is supplied by the cmd (loop.HarnessSystem, the
	// harness's identity + posture). Empty means skip the
	// harness-system slot.
	System string
	// SystemExternal is the external system/governance prompt
	// from --system or --system-file per SCOPE §14 step 2. The
	// loop prepends it AFTER the harness system and BEFORE the
	// skills. Empty means skip the external-system slot.
	SystemExternal string
	// Skills is the resolved skills' content, in the order they
	// should be composed into the model context per SCOPE §14
	// step 3. Each Skill becomes its own system message AFTER
	// SystemExternal and BEFORE the user task. Skills with empty
	// Content are skipped. Nil/empty means skip the skills slot.
	Skills []skill.Skill
	// Tools is the registered tool registry the loop dispatches
	// against in a multi-turn agent run. Nil means the loop does
	// NOT dispatch tools — the new RunAgent method below emits
	// an explicit configuration error and exits non-zero before
	// calling the model client (so a non-nil Tools is a
	// precondition for tool dispatch, not a soft default).
	//
	// The wiring from cmd/simple-harness/main.go's
	// builtins.RegisterBuiltins(globalRegistry) into this field
	// lands in handoff 041; for THIS handoff the binding pins
	// construct loop.Run with Tools set directly in the test
	// setup, bypassing the cmd surface. The field is purely
	// additive per SCOPE §42.
	//
	// MaxTurns is the upper bound on the agent's
	// model-request / tool-execution cycles. <= 0 means the loop
	// defaults to 8 per the GOAL §2 deliverable 6 default; the
	// loop enforces the limit as the simple-iteration guard:
	// each call to model.Client.ChatStream counts as one turn; if
	// turn count exceeds MaxTurns, the loop emits a
	// "TOOL_DISPATCH_OVERFLOW" status event + a completed event
	// with exit_code 1 per the SCOPE §3 "exceeding a limit must
	// produce an explicit observable result" discipline and
	// returns a sentinel error (*MaxTurnsError) that the cmd
	// maps to exit 1.
	Tools    *tools.Registry
	MaxTurns int
}

// Run is a single-turn interactive loop session. It owns the model
// client, the event emitter, the human-facing stdout writer, and
// the per-Run context accounting ledger. Construct via New; reuse
// across multiple RunOne calls (one prompt at a time). The Run
// does NOT spawn goroutines itself; RunOne is the unit of work
// the cmd calls once per prompt.
type Run struct {
	cfg    Config
	client *model.Client
	em     *event.Emitter
	out    io.Writer
	ledger *contextpkg.Ledger
}

// New returns a Run with its dependencies wired. The caller supplies
// the emitter (so the cmd can decide where the JSONL sidecar goes)
// and the human-facing stdout writer (the same writer that gets
// the streamed assistant text written to it). The context ledger
// is initialized empty; RunOne populates it after ComposeMessages.
func New(cfg Config, client *model.Client, em *event.Emitter, out io.Writer) *Run {
	return &Run{
		cfg:    cfg,
		client: client,
		em:     em,
		out:    out,
		ledger: &contextpkg.Ledger{},
	}
}

// SetSkills replaces the Skills field of the Run's Config with
// the given slice. It is the seam the interactive REPL uses when
// the user invokes the `/skill NAME` mid-session command (SCOPE
// §15's "skill mechanism should allow mid-session swap"): the
// handler updates its local *Skill pointer, calls SetSkills with
// the new single-element slice, and the next RunOne call's
// ComposeMessages sees the updated skills slot.
//
// SetSkills is NOT safe for concurrent use with an in-flight
// RunOne call. The interactive REPL is single-goroutine (the
// scanner goroutine only feeds the prompt loop; the prompt
// loop is the sole caller of RunOne and the sole caller of
// SetSkills, sequentially), so no locking is required.
//
// An empty / nil skills slice clears the skills slot (the next
// RunOne's composed message list omits the skills slot, as if
// --skill had not been set).
func (r *Run) SetSkills(skills []skill.Skill) {
	r.cfg.Skills = skills
}

// Ledger returns the per-Run context accounting ledger. The
// ledger accumulates entries as RunOne builds the composed
// message list; each RunOne call appends HarnessSystem +
// ExternalSystem (if non-empty) + each Skill (with non-empty
// content) + Task to the ledger. ToolSchemas / Conversation /
// ToolResults are tracked as categories but currently zero
// entries (V1: the loop does not yet dispatch tools or maintain
// multi-turn conversation history; future Runs that add those
// surfaces will extend RunOne to populate them).
//
// The ledger is the binding seam for SCOPE §18 context
// observability: the cmd's `context show` command reads it via
// this accessor and renders the SCOPE §19 accounting report;
// the cmd's `context doctor` command reads it and renders the
// SCOPE §20 diagnostics. Run 010 handoff 035 ships this
// accessor + the loop population; handoffs 036 + 037 ship the
// commands that consume it.
//
// NOT safe for concurrent use with an in-flight RunOne call.
// The interactive REPL is single-goroutine; no locking
// required.
func (r *Run) Ledger() *contextpkg.Ledger {
	return r.ledger
}

// PopulateLedger records the four SCOPE §18 content categories
// that are about to be (or have been) sent to the model, on the
// Run's per-Run context accounting ledger. The categories are
// populated in the canonical SCOPE §14 order: HarnessSystem
// (loop.Config.System, if non-empty) + ExternalSystem
// (loop.Config.SystemExternal, if non-empty) + each Skill
// (loop.Config.Skills[i] with non-empty Content) + Task
// (prompt). Empty categories are skipped (matches
// ComposeMessages semantics — a zero-byte skill body
// contributes neither a message nor a ledger entry).
//
// PopulateLedger is the cmd-side accounting seam: the
// `simple-harness context show` command (Run 010 / handoff 036)
// constructs a *Run via loop.New, calls PopulateLedger to
// populate the ledger WITHOUT invoking the model client, then
// prints r.Ledger().Report() to stdout. The surface's contract
// is "inspect a composition without executing it" (GOAL §2
// bound decision 1).
//
// RunOne calls PopulateLedger exactly once per call, between
// the Started/ModelRequest events and the ChatStream
// invocation, so the ledger snapshot matches the messages
// that went to the wire. Calling PopulateLedger multiple
// times accumulates entries; the existing TestRun_Ledger_*
// tests verify the single-call semantics.
//
// NOT safe for concurrent use with an in-flight RunOne call.
// The interactive REPL is single-goroutine; no locking
// required.
func (r *Run) PopulateLedger(prompt string) {
	if r.cfg.System != "" {
		r.ledger.Add(contextpkg.HarnessSystem, "harness", r.cfg.System)
	}
	if r.cfg.SystemExternal != "" {
		r.ledger.Add(contextpkg.ExternalSystem, "external", r.cfg.SystemExternal)
	}
	for _, s := range r.cfg.Skills {
		if s.Content == "" {
			continue
		}
		r.ledger.Add(contextpkg.Skill, s.Name, s.Content)
	}
	r.ledger.Add(contextpkg.Task, "task", prompt)
}

// RunOne executes one turn: emits started, calls
// model.Client.ChatStream with the prompt as a one-message
// [user, prompt] list, emits status: STREAMING on first non-empty
// event, emits assistant_stream for each non-empty delta, writes
// each delta to the human-facing stdout, emits status: COMPLETED
// and completed (exit_code 0) on a clean [DONE], and returns the
// accumulated assistant text.
//
// Error contract: a *model.ModelError from the client is returned
// as-is so the cmd can map ErrHTTP/ErrParse/ErrUpstream to exit
// code 3 and ErrTimeout to exit code 6 (SCOPE §28). Any other error
// (e.g. an emitter write failing) is returned unwrapped so the cmd
// can surface a generic failure (exit code 1).
//
// The accumulated text is returned alongside the error so a partial
// response is observable after a mid-stream failure.
func (r *Run) RunOne(ctx context.Context, prompt string) (string, error) {
	if err := r.em.Started(event.SessionConfig{
		Model:      r.cfg.Model.Model,
		Endpoint:   r.cfg.Model.BaseURL,
		Workspace:  r.cfg.Workspace,
		Permission: r.cfg.Permission,
	}); err != nil {
		return "", err
	}
	if err := r.em.ModelRequest(); err != nil {
		return "", err
	}

	var (
		accumulated strings.Builder
		streamed    bool
	)

	onDelta := func(ev model.StreamEvent) error {
		if !streamed && (ev.Delta != "" || ev.FinishReason != "" ||
			ev.ToolCallDelta != nil || ev.Usage != nil) {
			if err := r.em.Status("STREAMING"); err != nil {
				return err
			}
			streamed = true
		}
		if ev.Delta != "" {
			if _, err := io.WriteString(r.out, ev.Delta); err != nil {
				return err
			}
			accumulated.WriteString(ev.Delta)
			if err := r.em.AssistantStream(ev.Delta); err != nil {
				return err
			}
		}
		return nil
	}

	// SCOPE §18 ledger population: track each category of content
	// that is about to be sent to the model. V1 tracks
	// HarnessSystem + ExternalSystem + Skill(s) + Task. Empty
	// categories are skipped (matches ComposeMessages semantics).
	// The PopulateLedger helper is the cmd-side accounting seam
	// (handoff 036); RunOne calls it once per RunOne invocation.
	r.PopulateLedger(prompt)

	advertisedTools, advertisedToolChoice := toolsToChatRequestTools(r.cfg.Tools)
	err := r.client.ChatStream(ctx, model.ChatRequest{
		Messages:   ComposeMessages(r.cfg, prompt),
		Tools:      advertisedTools,
		ToolChoice: advertisedToolChoice,
	}, onDelta)

	if err != nil {
		// Emit the failure to the sidecar so an external controller
		// sees the failure mode (architecture §"External subscription"
		// and SCOPE §21 invariant — an external controller must not
		// need to scrape terminal output to understand execution).
		var me *model.ModelError
		if errors.As(err, &me) {
			switch me.Kind {
			case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
				_ = r.em.Status("FAILED")
			case model.ErrTimeout:
				_ = r.em.Status("INTERRUPTED")
			}
		} else {
			_ = r.em.Status("FAILED")
		}
		return accumulated.String(), err
	}

	if err := r.em.Status("COMPLETED"); err != nil {
		return accumulated.String(), err
	}
	if err := r.em.Completed(0); err != nil {
		return accumulated.String(), err
	}
	return accumulated.String(), nil
}

// NormalizeBaseURL strips a single trailing "/v1" from url. It is
// the seam where the config-resolved BaseURL (which SCOPE §29
// specifies with the /v1 suffix — see internal/config.Default:
// "http://127.0.0.1:8080/v1") meets the model.Client contract
// (which expects the host root and appends "/v1/chat/completions"
// itself).
//
// If url does not end in "/v1" (after trimming any trailing slash
// from the URL), it is returned unchanged. If url ends in "/v1/"
// (trailing slash), the slash is also stripped — calling this
// twice is safe (the second call is a no-op).
//
// Examples:
//
//	"http://x/v1"     -> "http://x"
//	"http://x/v1/"    -> "http://x"
//	"http://x"        -> "http://x"
//	"http://x/v1/chat" -> "http://x/v1/chat"  (only a trailing /v1 is stripped, not embedded)
func NormalizeBaseURL(url string) string {
	u := strings.TrimRight(url, "/")
	if strings.HasSuffix(u, "/v1") {
		return strings.TrimSuffix(u, "/v1")
	}
	return url
}

// ComposeMessages builds the SCOPE §14 message list:
//
//  1. harness system prompt   (cfg.System, if non-empty)
//  2. external system/governance (cfg.SystemExternal, if non-empty)
//  3. each loaded skill       (cfg.Skills[i], in order, with
//     non-empty Content)
//  4. user task               (prompt, always present)
//
// The order is BINDING per GOAL §2 + SCOPE §14: no permutation
// of the inputs changes the relative positions in the output.
// The harness system is always first; the user task is always
// last; skills always land BETWEEN the external system and the
// user task. This function is the single seam for context
// composition in V1 and is the target of reviewer duty 2
// (permutation-proof ordering test) — a future regression that
// reorders, drops, or duplicates a slot fails the test.
//
// An empty Skills[i].Content is skipped (a skill with a zero-byte
// body contributes no message). An empty prompt is allowed (the
// loop emits a [system*, user=""] message list, which the model
// client will reject — but the loop is not the validator; empty
// prompts are a runtime concern).
//
// The function is pure (no I/O, no goroutines, no global state);
// it is testable as a unit without a model server.
func ComposeMessages(cfg Config, prompt string) []model.Message {
	messages := make([]model.Message, 0, 1+len(cfg.Skills))
	if cfg.System != "" {
		messages = append(messages, model.Message{Role: "system", Content: cfg.System})
	}
	if cfg.SystemExternal != "" {
		messages = append(messages, model.Message{Role: "system", Content: cfg.SystemExternal})
	}
	for _, s := range cfg.Skills {
		if s.Content == "" {
			continue
		}
		messages = append(messages, model.Message{Role: "system", Content: s.Content})
	}
	messages = append(messages, model.Message{Role: "user", Content: prompt})
	return messages
}

// toolsToChatRequestTools builds the model.ToolDefinition list +
// the model-side ToolChoice value from the configured
// tools.Registry. The list is sorted-by-name (registry.Names()
// returns sorted names; the loop re-uses that ordering verbatim)
// so the wire body is deterministic across runs — the registry's
// internal map iteration must NOT leak into the wire. For an
// empty or nil registry the helper returns (nil, nil); the
// ,omitempty on the wire carries the empty case through.
//
// The helper is the single seam where the JSON-schema rendering
// happens: each tool's tools.Schema
// (Required/Properties(PropertyType)/AdditionalProperties) is
// translated into the OpenAI JSON-schema subset
// ({"type":"object","required":[...],"properties":{...},
// "additionalProperties":<bool>}); the marshaled bytes land in
// model.ToolDefinitionFunc.Parameters as a json.RawMessage.
//
// Run 023 / handoff 073 owns this helper. The helper is NOT
// concurrency-safe for concurrent mutation of the registry
// (Register is the only mutator; per handoff 013 Register is
// called once at startup, so concurrent dispatch sees a stable
// registry — the loop's existing dispatch path at line 706 calls
// r.cfg.Tools.Dispatch without a lock for the same reason).
func toolsToChatRequestTools(reg *tools.Registry) ([]model.ToolDefinition, *string) {
	if reg == nil {
		return nil, nil
	}
	names := reg.Names()
	if len(names) == 0 {
		return nil, nil
	}
	defs := make([]model.ToolDefinition, 0, len(names))
	for _, name := range names {
		t, ok := reg.Get(name)
		if !ok {
			continue
		}
		meta := t.Meta()
		schema := t.Schema()
		params, err := schemaToJSONSchema(schema)
		if err != nil {
			// A schema-render failure is a programming error in
			// the tool's Schema declaration; the loop continues
			// without that tool rather than failing the whole
			// request (the SCOPE §31 untrusted-input discipline:
			// structured rejection, never a hard failure). The
			// cmd-side accounting ledger does not carry this
			// signal — the omitted tool is the observable.
			continue
		}
		defs = append(defs, model.ToolDefinition{
			Type: "function",
			Function: model.ToolDefinitionFunc{
				Name:        meta.Name,
				Description: meta.Description,
				Parameters:  params,
			},
		})
	}
	if len(defs) == 0 {
		return nil, nil
	}
	choice := "auto"
	return defs, &choice
}

// schemaToJSONSchema renders a tools.Schema as a JSON-schema object
// for the OpenAI function-calling parameters field. The mapping is
// the minimal subset needed for the wire: object type, required
// array, typed properties map, additionalProperties boolean.
//
// The wire shape:
//   {"type":"object","required":[...],"properties":{"k":{"type":"<type>"},
//    ...},"additionalProperties":<bool>}
//
// An empty Properties map emits "properties":{} (the wire accepts
// this; OpenAI models treat it as a no-arg tool). A nil/empty
// Required emits "required":null via the json.Marshal "omitempty"
// semantics — the implementer may use a pointer-typed Required
// field on a local struct to control the wire shape precisely.
func schemaToJSONSchema(schema tools.Schema) (json.RawMessage, error) {
	props := make(map[string]map[string]string, len(schema.Properties))
	for k, v := range schema.Properties {
		props[k] = map[string]string{"type": string(v)}
	}
	body := struct {
		Type                 string                       `json:"type"`
		Required             []string                     `json:"required,omitempty"`
		Properties           map[string]map[string]string `json:"properties"`
		AdditionalProperties bool                         `json:"additional_properties"`
	}{
		Type:                 "object",
		Required:             schema.Required,
		Properties:           props,
		AdditionalProperties: schema.AdditionalProperties,
	}
	return json.Marshal(body)
}

// --- handoff 040: Run 017 / LOOP-CORE ---

// MaxTurnsError signals that the agent run exhausted the
// configured MaxTurns bound without the model emitting a
// tool-call-free final response. SCOPE §3 "exceeding a limit
// must produce an explicit observable result" — the cmd maps
// this to exit 1 (SCOPE §28, generic failure, since the task
// did not complete).
type MaxTurnsError struct{ Limit int }

func (e *MaxTurnsError) Error() string {
	return fmt.Sprintf("loop: max-turns %d exceeded", e.Limit)
}

// PermissionError signals that the agent run's first
// permission-violating tool call was rejected by the
// perm.Authorize pipeline. The cmd maps this to exit 4
// (SCOPE §28, permission violation). The dispatch pipeline
// ran the validate → authorize steps and the policy denied;
// the underlying tool was not executed.
type PermissionError struct{ Underlying error }

func (e *PermissionError) Error() string {
	return fmt.Sprintf("loop: permission denied: %v", e.Underlying)
}
func (e *PermissionError) Unwrap() error { return e.Underlying }

// ConfigError signals that loop.Config.Tools was nil when
// RunAgent was called — a precondition failure the loop
// refuses to paper over. The cmd maps this to exit 2
// (SCOPE §28, configuration error).
type ConfigError struct{ Reason string }

func (e *ConfigError) Error() string { return "loop: " + e.Reason }

// defaultMaxTurns is the default upper bound on agent-loop
// iterations when loop.Config.MaxTurns is left at its zero
// value. GOAL §2 deliverable 6 names 8 as the default; the
// cmd-side wiring in handoff 041 sets the explicit value
// before calling RunAgent so this constant is the safety net
// for direct test callers that pass 0.
const defaultMaxTurns = 8

// RunAgent executes the SCOPE §3 multi-turn agent cycle:
//
//	model request → stream → tool calls?
//	  ├── no  → final response
//	  └── yes → validate → authorize → execute → record → append → model request
//
// The method is the multi-turn supersession of RunOne: a
// tool-dispatching run uses RunAgent; a non-tool-dispatching
// single-turn caller continues to use RunOne. RunOne's
// behavior is unchanged.
//
// Preconditions (both checked at entry; the second is the
// safety-net defaulting for direct test callers; the cmd-side
// wiring in handoff 041 sets the explicit value before
// invoking RunAgent):
//
//   - r.cfg.Tools != nil — otherwise returns *ConfigError
//     (the loop refuses to silently degrade to a non-
//     dispatching run; this is a precondition failure, not a
//     soft default).
//   - r.cfg.MaxTurns > 0 — defaults to defaultMaxTurns (8)
//     when <= 0; no warning is emitted because the cmd is
//     expected to set the value explicitly and the constant
//     is the documented safety net.
//
// Per-turn flow:
//
//  1. Emit model_request for the first turn (the V1 single-
//     turn pattern).
//  2. Initialize the per-Run message history from
//     ComposeMessages (HarnessSystem + ExternalSystem +
//     Skill(s) + user task).
//  3. For each turn (1 to MaxTurns):
//     a. Call r.client.ChatStream with the running history
//        and the onDelta callback that (i) accumulates
//        per-index tool-call deltas into a per-turn
//        accumulatedCall map, (ii) emits the existing
//        assistant_stream event for any ev.Delta text, (iii)
//        emits status: STREAMING on first non-empty event.
//     b. If zero tool calls accumulated (all per-index
//        accumulators are nil/empty), this is a "no tool
//        calls" final response → emits status: COMPLETED +
//        completed(exit_code: 0) and returns the accumulated
//        text.
//     c. If one or more tool calls accumulated, dispatch
//        each in sequence via r.cfg.Tools.Dispatch(ctx,
//        call, ws, pol, perm.Authorize). On error, append a
//        tool-result message with status="error" + the
//        structured error JSON to the message history and
//        continue to the next turn (the SCOPE §31
//        "untrusted input" discipline: structured rejection
//        is the harness's contract with the model, not a
//        hard failure).
//     d. If a permission violation surfaces, emit
//        status: FAILED + completed(exit_code: 4) and
//        return *PermissionError.
//     e. After all tool calls in the current turn are
//        dispatched, increment the turn counter and loop
//        back to step 3a.
//
// On exhaustion: emit status: FAILED with reason
// "TOOL_DISPATCH_OVERFLOW: max-turns <N> exceeded" +
// completed(exit_code: 1) and return *MaxTurnsError.
//
// The ledger interactions are limited to the first turn's
// PopulateLedger(prompt) call (same as RunOne's V1 single-
// turn behavior); multi-turn tool-call messages are tracked
// in the message history only, not the ledger.
func (r *Run) RunAgent(ctx context.Context, prompt string) (string, error) {
	if r.cfg.Tools == nil {
		_ = r.em.Status("FAILED")
		_ = r.em.Completed(2)
		return "", &ConfigError{Reason: "RunAgent requires loop.Config.Tools to be non-nil"}
	}
	maxTurns := r.cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}

	if err := r.em.Started(event.SessionConfig{
		Model:      r.cfg.Model.Model,
		Endpoint:   r.cfg.Model.BaseURL,
		Workspace:  r.cfg.Workspace,
		Permission: r.cfg.Permission,
	}); err != nil {
		return "", err
	}

	// Populate the per-Run ledger exactly once per RunAgent call
	// (matches the V1 RunOne's single PopulateLedger call per
	// invocation; multi-turn tool-call messages accumulate in the
	// model-facing message history but not in the accounting
	// ledger — the ledger tracks prompt composition, not
	// conversation history).
	r.PopulateLedger(prompt)

	history := ComposeMessages(r.cfg, prompt)
	advertisedTools, advertisedToolChoice := toolsToChatRequestTools(r.cfg.Tools)

	var (
		accumulatedText strings.Builder
		streamed        bool
	)

	// Pre-build the workspace + policy once per RunAgent (the
	// workspace path is stable; the policy is stable for the
	// whole agent run; per-call re-construction is wasteful).
	ws, err := workspaceFromPath(r.cfg.Workspace)
	if err != nil {
		_ = r.em.Status("FAILED")
		_ = r.em.Completed(2)
		return "", &ConfigError{Reason: fmt.Sprintf("invalid workspace %q: %v", r.cfg.Workspace, err)}
	}
	pol, err := policyFromPermission(r.cfg.Permission)
	if err != nil {
		_ = r.em.Status("FAILED")
		_ = r.em.Completed(2)
		return "", &ConfigError{Reason: fmt.Sprintf("invalid permission %q: %v", r.cfg.Permission, err)}
	}

	// Implementer's chosen semantic for the MaxTurns overflow:
	// the check fires at the START of each turn (BEFORE the
	// ChatStream call). The sequence per iteration is:
	//   1. Emit model_request (the SCOPE §21 / GOAL §2 signal
	//      that the harness is about to invoke the model client).
	//   2. Check if the current turn number exceeds MaxTurns; if
	//      so, emit the overflow status + status: FAILED +
	//      completed(exit_code: 1) and return *MaxTurnsError.
	//   3. Otherwise call ChatStream; process the response; if
	//      the model returned zero tool calls, return success;
	//      otherwise dispatch the calls, append the results to
	//      the message history, and increment the turn counter.
	//
	// This means with MaxTurns=N the loop emits N+1
	// model_request events when every turn returns tool calls
	// (turn 1, 2, ..., N are within the bound and fire
	// ChatStream; turn N+1 fires model_request + overflow but
	// no ChatStream). The binding pin's "exactly 3
	// model_request events for MaxTurns=2" assertion documents
	// this chosen semantic.
	for turn := 1; ; turn++ {
		if err := r.em.ModelRequest(); err != nil {
			return "", err
		}
		if turn > maxTurns {
			// Overflow: the bound was exceeded. Emit the
			// structured overflow signal so the JSONL sidecar
			// carries the explicit reason per SCOPE §3.
			_ = r.em.Status(fmt.Sprintf("TOOL_DISPATCH_OVERFLOW: max-turns %d exceeded", maxTurns))
			_ = r.em.Status("FAILED")
			_ = r.em.Completed(1)
			return accumulatedText.String(), &MaxTurnsError{Limit: maxTurns}
		}

		var perIndexAccum map[int]*model.ToolCall
		var firstNonEmpty bool

		onDelta := func(ev model.StreamEvent) error {
			if !firstNonEmpty && (ev.Delta != "" || ev.FinishReason != "" ||
				ev.ToolCallDelta != nil || ev.Usage != nil) {
				if err := r.em.Status("STREAMING"); err != nil {
					return err
				}
				firstNonEmpty = true
				streamed = true
			}
			if ev.Delta != "" {
				if _, err := io.WriteString(r.out, ev.Delta); err != nil {
					return err
				}
				accumulatedText.WriteString(ev.Delta)
				if err := r.em.AssistantStream(ev.Delta); err != nil {
					return err
				}
			}
			if ev.ToolCallDelta != nil {
				if perIndexAccum == nil {
					perIndexAccum = make(map[int]*model.ToolCall)
				}
				if err := model.AccumulateToolCallFragment(perIndexAccum, ev.ToolCallDelta); err != nil {
					return err
				}
			}
			return nil
		}

		if err := r.client.ChatStream(ctx, model.ChatRequest{
			Messages:   history,
			Tools:      advertisedTools,
			ToolChoice: advertisedToolChoice,
		}, onDelta); err != nil {
			var me *model.ModelError
			if errors.As(err, &me) {
				switch me.Kind {
				case model.ErrHTTP, model.ErrParse, model.ErrUpstream:
					_ = r.em.Status("FAILED")
				case model.ErrTimeout:
					_ = r.em.Status("INTERRUPTED")
				}
			} else {
				_ = r.em.Status("FAILED")
			}
			return accumulatedText.String(), err
		}

		// No tool calls accumulated → final response (single-turn
		// success path; matches the V1 RunOne happy path).
		if len(perIndexAccum) == 0 {
			if !streamed {
				// No events at all — still emit STREAMING so the
				// sidecar carries the documented sequence.
				_ = r.em.Status("STREAMING")
			}
			if err := r.em.Status("COMPLETED"); err != nil {
				return accumulatedText.String(), err
			}
			if err := r.em.Completed(0); err != nil {
				return accumulatedText.String(), err
			}
			return accumulatedText.String(), nil
		}

		// At least one tool call accumulated. Append an
		// assistant message carrying every completed call to the
		// message history so the model sees its own tool-call on
		// the next turn, then dispatch each call in order.
		//
		// Wire-shape discipline (Run 023 amendment 4): the
		// OpenAI chat-completions spec requires the assistant
		// tool_calls message BEFORE the per-call tool messages so
		// the server can correlate the tool_call_id fields on
		// the follow-up. We assemble one model.ToolCall per
		// per-index accumulator entry in index order; the
		// Type:"function" tag matches the OpenAI function-
		// calling wire shape. The message's Content is left
		// empty (omitempty elides it on the wire).
		toolCalls := make([]model.ToolCall, 0, len(perIndexAccum))
		for idx := 0; idx < len(perIndexAccum); idx++ {
			call, ok := perIndexAccum[idx]
			if !ok || call == nil {
				continue
			}
			toolCalls = append(toolCalls, model.ToolCall{
				Index:     call.Index,
				ID:        call.ID,
				Type:      "function",
				Name:      call.Name,
				Arguments: call.Arguments,
			})
		}
		history = append(history, model.Message{
			Role:      "assistant",
			ToolCalls: toolCalls,
		})

		anyPermissionViolation := false
		var permUnderlying error
		for idx := 0; idx < len(perIndexAccum); idx++ {
			call, ok := perIndexAccum[idx]
			if !ok || call == nil {
				continue
			}
			// EMIT tool_call BEFORE the dispatch (the dispatch
			// pipeline runs the permission check, which may
			// reject the call — the tool_call event captures
			// the attempt regardless).
			if err := r.em.ToolCall(call.ID, call.Name); err != nil {
				_ = r.em.Status("FAILED")
				_ = r.em.Completed(1)
				return accumulatedText.String(), err
			}
			// Convert the assembled model.ToolCall into the
			// FROZEN tools.Call dispatch contract (no ID
			// field — the loop carries the ID for event
			// emission; the dispatch pipeline uses Name +
			// Arguments only).
			toolsCall := tools.Call{
				Name:      call.Name,
				Arguments: call.Arguments,
			}
			result := r.cfg.Tools.Dispatch(ctx, toolsCall, ws, pol, perm.Authorize)
			if result.Status == "error" && result.Error != nil && result.Error.Kind == "permission_denied" {
				// Permission-denied: NO tool_result event
				// fires — the permission denial IS the
				// terminal event for the call (per
				// handoff 042 / step (1)(b)(6); the
				// tool_call + status:FAILED +
				// completed(exit_code: 4) sequence is
				// the documented observable).
				anyPermissionViolation = true
				permUnderlying = fmt.Errorf("%s: %s", result.Error.Kind, result.Error.Message)
				break
			}
			// EMIT tool_result AFTER the dispatch returns
			// (for non-permission-denied calls). Content is
			// the JSON-encoded result body — the success
			// content (tools.Result.Content) for "ok", the
			// structured ToolError for "error". An empty
			// string elides the Content field via the
			// Emitter.ToolResult omitempty guard.
			evContent, encErr := encodeToolResultContent(result)
			if encErr != nil {
				_ = r.em.Status("FAILED")
				_ = r.em.Completed(1)
				return accumulatedText.String(), fmt.Errorf("loop: encode tool result for %s: %w", call.Name, encErr)
			}
			if err := r.em.ToolResult(call.ID, result.Status, evContent); err != nil {
				_ = r.em.Status("FAILED")
				_ = r.em.Completed(1)
				return accumulatedText.String(), err
			}
			// Encode the result as a tool message so the model
			// sees the outcome on the next turn.
			encoded, encErr := encodeToolResult(&toolsCall, result)
			if encErr != nil {
				_ = r.em.Status("FAILED")
				_ = r.em.Completed(1)
				return accumulatedText.String(), fmt.Errorf("loop: encode tool result for %s: %w", call.Name, encErr)
			}
			history = append(history, model.Message{
				Role:       "tool",
				Content:    encoded,
				ToolCallID: call.ID,
			})
		}

		if anyPermissionViolation {
			_ = r.em.Status("FAILED")
			_ = r.em.Completed(4)
			return accumulatedText.String(), &PermissionError{Underlying: permUnderlying}
		}
	}
}

// encodeToolResult produces the message-body string for a tool
// dispatch outcome. On success, the result content is JSON-encoded
// verbatim; on error, the structured ToolError is JSON-encoded so
// the model receives a parseable description of what went wrong.
// The encoding is the simplest viable form for THIS handoff —
// handoff 041 may extend this to a richer wire shape (an explicit
// tool_call_id field, etc.).
func encodeToolResult(call *tools.Call, result tools.Result) (string, error) {
	if result.Status == "ok" {
		body := map[string]any{
			"name":    call.Name,
			"status":  "ok",
			"content": result.Content,
		}
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	body := map[string]any{
		"name":  call.Name,
		"status": "error",
		"error": result.Error,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// accumulateToolCallFragment + parseToolCallArgs: REMOVED in
// handoff 042. The loop's per-fragment merge + JSON-parsing
// helpers are folded away in favor of the model-side counterparts
// model.AccumulateToolCallFragment + model.ParseToolCallArgs
// shipped in handoff 041 (per the verdict-040 carry-forward note
// in the handoff 041 ledger entry: "handoff 042 either migrates
// the loop's caller to this exported helper or folds the private
// duplicate away"). The loop's caller now uses
// model.AccumulateToolCallFragment directly; the loop-side parse
// is unnecessary because the per-fragment merge already performs
// per-delta unmarshalling for the streaming path.

// encodeToolResultContent produces the content string for the
// tool_result event emitted after each non-permission-denied
// dispatch. For "ok" results it JSON-encodes result.Content
// (the success body the tool returned — e.g. ApplyPatchResult
// for apply_patch); for "error" results it JSON-encodes the
// structured ToolError so an external controller sees the
// parseable kind/message/call shape. A nil Content (rare; the
// V1 tools always populate one) returns "" so the
// Emitter.ToolResult helper's `if content != ""` guard elides
// the Content field on the wire (matches the SCOPE §42
// additive-evolution discipline: absent fields are absent).
func encodeToolResultContent(result tools.Result) (string, error) {
	switch result.Status {
	case "ok":
		if result.Content == nil {
			return "", nil
		}
		b, err := json.Marshal(result.Content)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if result.Error == nil {
		return "", nil
	}
	b, err := json.Marshal(result.Error)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// workspaceFromPath constructs a tools.Workspace (= path.Workspace)
// from the configured workspace directory. Returns the workspace
// or an error if the path is invalid (e.g. a non-existent
// workspace root when path.New's EvalSymlinks rejects it).
func workspaceFromPath(workspaceDir string) (tools.Workspace, error) {
	if workspaceDir == "" {
		return tools.Workspace{}, fmt.Errorf("empty workspace directory")
	}
	// path.New is the canonical constructor; the loop's import
	// surface is the tools.Workspace alias which IS
	// path.Workspace (per tools/types.go). Symlink evaluation
	// and workspace-root stability semantics are honored via
	// the canonical path.New path.
	return path.New(workspaceDir)
}

// policyFromPermission parses the loop's permission string (one
// of "READ_ONLY", "WORKSPACE_WRITE", "FULL_ACCESS") into a
// tools.Policy. Returns the policy or an error if the string is
// unknown.
func policyFromPermission(permStr string) (tools.Policy, error) {
	mode, err := perm.ParseMode(strings.ToLower(permStr))
	if err != nil {
		return nil, err
	}
	return perm.NewPolicy(mode), nil
}
