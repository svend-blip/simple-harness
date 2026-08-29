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
	"errors"
	"io"
	"strings"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/skill"
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

	err := r.client.ChatStream(ctx, model.ChatRequest{
		Messages: ComposeMessages(r.cfg, prompt),
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
