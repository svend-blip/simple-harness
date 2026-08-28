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

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
)

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
}

// Run is a single-turn interactive loop session. It owns the model
// client, the event emitter, and the human-facing stdout writer.
// Construct via New; reuse across multiple RunOne calls (one
// prompt at a time). The Run does NOT spawn goroutines itself;
// RunOne is the unit of work the cmd calls once per prompt.
type Run struct {
	cfg    Config
	client *model.Client
	em     *event.Emitter
	out    io.Writer
}

// New returns a Run with its dependencies wired. The caller supplies
// the emitter (so the cmd can decide where the JSONL sidecar goes)
// and the human-facing stdout writer (the same writer that gets
// the streamed assistant text written to it).
func New(cfg Config, client *model.Client, em *event.Emitter, out io.Writer) *Run {
	return &Run{
		cfg:    cfg,
		client: client,
		em:     em,
		out:    out,
	}
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

	err := r.client.ChatStream(ctx, model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: prompt}},
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
