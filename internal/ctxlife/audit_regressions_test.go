package ctxlife

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/model"
)

func compactionFixture(t *testing.T) (*Manager, *fixedCompactor, []model.Message) {
	t.Helper()
	m := New(1000)
	m.Budget = Budget{ModelLimit: 1000, GenerationReserve: 100, SafetyReserve: 100} // active 800
	m.KeepRecentTurns = 2
	c := &fixedCompactor{summary: "summary of the earlier work"}
	m.Compactor = c
	msgs := []model.Message{sys(filler(50)), user("task " + filler(20))}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, asst(filler(100)))
	}
	return m, c, msgs
}

// TestCompactionSummaryIsNotASecondSystemMessage — the summary was
// inserted as a system message after the user task. Endpoints that
// apply the model's chat template reject a system message that is
// not at the beginning (FreeToken: HTTP 400), which is why the wire
// joins the LEADING system run; a system message in the middle of
// the list cannot be joined. The summary is a pinned non-system
// message, and it is never compacted again.
func TestCompactionSummaryIsNotASecondSystemMessage(t *testing.T) {
	m, c, msgs := compactionFixture(t)
	out, _, err := m.Fit(msgs)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if c.calls != 1 {
		t.Fatalf("compactor called %d times, want 1 (the fixture must compact)", c.calls)
	}
	seenNonSystem := false
	var summaryAt = -1
	for i, msg := range out {
		if msg.Role != "system" {
			seenNonSystem = true
		} else if seenNonSystem {
			t.Fatalf("out[%d] is a system message after a non-system one: %q", i, msg.Content)
		}
		if IsCompacted(msg) {
			summaryAt = i
		}
	}
	if summaryAt < 0 {
		t.Fatal("no compacted summary in the fitted context")
	}
	if prios := m.Classify(out); prios[summaryAt] != Pinned {
		t.Errorf("the summary is %v, want pinned so it is not compacted again", prios[summaryAt])
	}
	// Feeding the fitted view back does not compact again.
	if _, _, err := m.Fit(out); err != nil {
		t.Fatalf("second Fit: %v", err)
	}
	if c.calls != 1 {
		t.Errorf("compactor called %d times after refitting the fitted view, want 1", c.calls)
	}
}

// TestNarrowingReachesTheFloor — widths were halved from the
// configured window while >= the floor, so with KeepRecentTurns 6 and
// a floor of 2 only width 3 was tried: a run that would have fitted
// at the floor failed "will not narrow below 2".
func TestNarrowingReachesTheFloor(t *testing.T) {
	m := New(1000)
	m.Budget = Budget{ModelLimit: 1000, GenerationReserve: 200, SafetyReserve: 150} // active 650
	m.KeepRecentTurns = 6
	msgs := []model.Message{sys(filler(5)), user(filler(5))}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, toolResult(fmt.Sprintf("c%d", i), filler(200)))
	}
	out, acct, err := m.Fit(msgs)
	if err != nil {
		t.Fatalf("Fit: %v (the two-message floor fits the budget)", err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("returned context is %d tokens against %d", acct.Total, acct.Budget)
	}
	if m.Stats.RecentWindowNarrowings != 1 {
		t.Errorf("narrowings = %d, want 1", m.Stats.RecentWindowNarrowings)
	}
	verbatim := 0
	for _, msg := range out {
		if msg.Role == "tool" && !isPruned(msg.Content) {
			verbatim++
		}
	}
	if verbatim != m.minRecent() {
		t.Errorf("%d tool results kept verbatim, want the floor of %d", verbatim, m.minRecent())
	}
}

// TestNoReductionIsCountedWhenNothingWasReduced — Reductions was
// incremented before any reduction ran, so a run that failed with
// nothing reduced reported one.
func TestNoReductionIsCountedWhenNothingWasReduced(t *testing.T) {
	m := New(1000)
	m.Budget = Budget{ModelLimit: 1000, GenerationReserve: 100, SafetyReserve: 100}
	m.PruneToolResults = false
	m.Compactor = nil
	msgs := []model.Message{sys(filler(10)), user(filler(10))}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, asst(filler(100)))
	}
	if _, _, err := m.Fit(msgs); err == nil {
		t.Fatal("Fit succeeded with every reduction disabled")
	}
	if m.Stats.Reductions != 0 {
		t.Errorf("Reductions = %d, want 0: nothing was reduced", m.Stats.Reductions)
	}
}

// TestModelCompactorReportsItsRequests — a compaction is a model
// inference like any other, but it emitted no model_request or usage
// event, so a measurement counting those undercounted calls and
// tokens. The compactor now tells its caller when it asks the model
// and what the model reported.
func TestModelCompactorReportsItsRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a summary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	requests := 0
	var usage *model.Usage
	c := &ModelCompactor{
		Client:    model.NewClient(model.Options{BaseURL: srv.URL, Model: "m", RequestTimeout: 2 * time.Second}),
		Ctx:       context.Background(),
		OnRequest: func() { requests++ },
		OnUsage:   func(u *model.Usage) { usage = u },
	}
	got, err := c.Compact([]model.Message{asst("older work")})
	if err != nil || got != "a summary" {
		t.Fatalf("Compact = %q, %v", got, err)
	}
	if requests != 1 {
		t.Errorf("OnRequest called %d times, want 1", requests)
	}
	if usage == nil || usage.PromptTokens != 40 {
		t.Errorf("OnUsage got %+v, want the reported usage", usage)
	}
}

// TestAnOversizedRecentToolResultIsTruncatedRatherThanFatal — a tool
// result larger than the whole budget sits in the recent window's
// floor, where pruning, compaction and narrowing cannot touch it; the
// run used to fail with "cannot fit". The last-resort reduction cuts
// it to an excerpt that names what was cut. The caller's list is
// untouched.
func TestAnOversizedRecentToolResultIsTruncatedRatherThanFatal(t *testing.T) {
	m := New(1000)
	m.Budget = Budget{ModelLimit: 1000, GenerationReserve: 100, SafetyReserve: 100} // active 800
	msgs := []model.Message{sys(filler(20)), user(filler(10)),
		{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "c1", Name: "read_file", Arguments: map[string]any{"path": "big"}}}},
		toolResult("c1", "HEAD-"+filler(2000)+"-TAIL"),
	}
	out, acct, err := m.Fit(msgs)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("%d tokens against %d", acct.Total, acct.Budget)
	}
	if m.Stats.ToolResultsTruncated != 1 {
		t.Errorf("truncated = %d, want 1", m.Stats.ToolResultsTruncated)
	}
	last := out[len(out)-1]
	if !strings.HasPrefix(last.Content, "HEAD-") || !strings.HasSuffix(last.Content, "-TAIL") || !isTruncated(last.Content) {
		t.Errorf("excerpt lost its head, tail or marker: %.60s ... %.40s", last.Content, last.Content[len(last.Content)-40:])
	}
	if last.ToolCallID != "c1" {
		t.Error("the tool_call_id must survive truncation")
	}
	if isTruncated(msgs[3].Content) {
		t.Error("the caller's message was modified")
	}
	// Refitting the fitted view does not cut again.
	if _, _, err := m.Fit(out); err != nil || m.Stats.ToolResultsTruncated != 1 {
		t.Errorf("second Fit: err=%v truncated=%d", err, m.Stats.ToolResultsTruncated)
	}
}

// TestCompactionIsNotAttemptedWhenItCannotCoverTheDeficit — measured
// 2026-09-19 on a file-reading task: the context was over budget because
// of large tool results inside the recent window, the reducible span was
// a few hundred tokens of one-line remarks and pruned placeholders, and
// Fit still spent a whole model inference compacting it — 13 s and 16 s
// against FreeToken to save ~6 tokens once and nothing the second time,
// 22-29 s each against Ollama, 63 % of that run's wall time. Removing
// the span entirely could not have reached the budget; narrowing the
// window, which costs nothing, is what did.
func TestCompactionIsNotAttemptedWhenItCannotCoverTheDeficit(t *testing.T) {
	c := &fixedCompactor{summary: "objective: x."}
	m := New(4096)
	m.Compactor = c
	budget := m.Account(nil).Budget
	in := []model.Message{sys("instructions"), user("read the files")}
	for i := 0; i < 4; i++ {
		in = append(in, asst("That file holds the configuration."))
	}
	// The recent window: eight messages whose tool results alone
	// exceed the budget.
	for i := 0; i < 4; i++ {
		in = append(in, asst("reading"), toolResult(fmt.Sprintf("c%d", i), filler(budget/3)))
	}

	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("over budget: %d of %d", acct.Total, acct.Budget)
	}
	if c.calls != 0 {
		t.Errorf("the compactor was called %d times for a span that could not cover the deficit", c.calls)
	}
	if m.Stats.RecentWindowNarrowings == 0 {
		t.Errorf("the window was not narrowed; stats %+v", m.Stats)
	}
	_ = out
}

// TestCompactionStillRunsOnceCheaperReductionsHaveMadeItWorthwhile —
// skipping is not giving up: when the free reductions leave a deficit
// the span can cover, the compactor is called then.
func TestCompactionStillRunsOnceCheaperReductionsHaveMadeItWorthwhile(t *testing.T) {
	c := &fixedCompactor{summary: "objective: x."}
	m := New(4096)
	m.Compactor = c
	m.KeepRecentTurns = DefaultMinRecentTurns // no window left to narrow
	budget := m.Account(nil).Budget
	in := []model.Message{sys("instructions"), user("read the files")}
	for i := 0; i < 10; i++ {
		in = append(in, asst(filler(budget/10)))
	}
	// One result twice the budget: at first the deficit exceeds the
	// span; cut to an excerpt, it no longer does.
	in = append(in, asst("reading"), toolResult("big", filler(2*budget)))

	_, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("over budget: %d of %d", acct.Total, acct.Budget)
	}
	if c.calls != 1 || m.Stats.Compactions != 1 {
		t.Errorf("compactor calls %d, compactions %d; want 1 and 1", c.calls, m.Stats.Compactions)
	}
}

// TestNarrowingIsRetriedAfterAnOversizedResultIsCut — measured
// 2026-09-19: a 10.5k-token file read into a 7k budget. Narrowing ran
// first and failed, because the oversized result sits inside the floor
// of the window; the result was then cut to an excerpt, which left the
// context 1 064 tokens over — and Fit gave up ("the window will not
// narrow below 2") without narrowing again, though the older results
// in the window were now the whole deficit and pruning them was free.
func TestNarrowingIsRetriedAfterAnOversizedResultIsCut(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 12
	budget := m.Account(nil).Budget
	in := []model.Message{sys("instructions"), user("read the files")}
	for i := 0; i < 5; i++ {
		in = append(in, asst("reading"), toolResult(fmt.Sprintf("c%d", i), filler(budget/5)))
	}
	in = append(in, asst("reading"), toolResult("big", filler(2*budget)))

	_, acct, err := m.Fit(in)
	if err != nil {
		t.Fatalf("Fit gave up on a context the free reductions can fit: %v", err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("over budget: %d of %d", acct.Total, acct.Budget)
	}
	if m.Stats.ToolResultsTruncated != 1 || m.Stats.RecentWindowNarrowings == 0 {
		t.Errorf("want one truncation and a narrowing; stats %+v", m.Stats)
	}
}

// TestCompactionNeedsRoomForASummary — a span larger than the deficit
// is not enough. Measured 2026-09-19: spans of 150-500 tokens against
// deficits of a few dozen, and summaries of 464-1 424 tokens (the
// instruction asks for six headings). Three inferences, 52 s, ~8 tokens
// saved. A summary has a size of its own; where span minus deficit
// leaves no room for one, the inference cannot pay.
func TestCompactionNeedsRoomForASummary(t *testing.T) {
	c := &fixedCompactor{summary: "objective: x."}
	m := New(4096)
	m.Compactor = c
	budget := m.Account(nil).Budget
	in := []model.Message{sys("instructions"), user("read the files")}
	// A reducible span of ~300 tokens: larger than the deficit
	// below, smaller than the room a summary needs.
	for i := 0; i < 6; i++ {
		in = append(in, asst(filler(50)))
	}
	// The recent window, a little over what is left of the budget.
	for i := 0; i < 4; i++ {
		in = append(in, asst("reading"), toolResult(fmt.Sprintf("c%d", i), filler((budget-250)/4)))
	}
	before := m.Account(in)
	if d := before.Total - before.Budget; d <= 0 || d >= 300 {
		t.Fatalf("fixture: deficit %d, want between 1 and the 300-token span", d)
	}

	_, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("over budget: %d of %d", acct.Total, acct.Budget)
	}
	if c.calls != 0 {
		t.Errorf("the compactor was called %d times with no room for a summary", c.calls)
	}
}

// sizedCompactor records the target it was given.
type sizedCompactor struct {
	fixedCompactor
	targets []int
}

func (c *sizedCompactor) CompactWithin(msgs []model.Message, target int) (string, error) {
	c.targets = append(c.targets, target)
	return c.Compact(msgs)
}

// TestTheCompactorIsToldHowLargeTheSummaryMayBe — the compaction request
// carried no size target, so the summary's size was whatever the model
// produced: 464 to 1 424 tokens, one of them 35 s of generation. The
// manager knows the room (span less deficit) and passes it on, capped
// at MaxSummaryTokens.
func TestTheCompactorIsToldHowLargeTheSummaryMayBe(t *testing.T) {
	// run builds a span of spanTokens and a recent message sized so
	// that the deficit is spanTokens-room, and returns the target.
	run := func(spanTokens, room int) int {
		t.Helper()
		c := &sizedCompactor{fixedCompactor: fixedCompactor{summary: "objective: x."}}
		m := New(4096)
		m.KeepRecentTurns = 2
		m.Compactor = c
		budget := m.Account(nil).Budget
		in := []model.Message{sys("instructions")}
		for i := 0; i < spanTokens/120; i++ {
			in = append(in, asst(filler(120)))
		}
		in = append(in, asst(filler(budget-room)), user("continue"))
		before := m.Account(in)
		gotRoom := m.reducibleTokens(in) - (before.Total - before.Budget)
		if _, _, err := m.Fit(in); err != nil {
			t.Fatal(err)
		}
		if len(c.targets) != 1 {
			t.Fatalf("CompactWithin called %d times, want 1 (room %d)", len(c.targets), gotRoom)
		}
		want := gotRoom
		if want > DefaultMaxSummaryTokens {
			want = DefaultMaxSummaryTokens
		}
		if c.targets[0] != want {
			t.Errorf("target %d, want %d (room %d)", c.targets[0], want, gotRoom)
		}
		return c.targets[0]
	}
	if got := run(3600, 2400); got != DefaultMaxSummaryTokens {
		t.Errorf("a large room: target %d, want the ceiling %d", got, DefaultMaxSummaryTokens)
	}
	if got := run(1200, 700); got >= DefaultMaxSummaryTokens || got < DefaultMinSummaryRoom {
		t.Errorf("a small room: target %d, want it between the floor and the ceiling", got)
	}
}

// TestModelCompactorStatesTheTargetAndCapsTheOutput — what the target
// becomes on the wire. The cap is on all output, and a reasoning model
// spends output before the summary begins: measured on qwen3.6-27b,
// ~1 500 reasoning tokens ahead of a 563-token summary, so a cap of
// twice the target produced no summary at all. Hence the allowance,
// which is dropped only when the compaction's own reasoning effort says
// there will be none.
func TestModelCompactorStatesTheTargetAndCapsTheOutput(t *testing.T) {
	var body struct {
		MaxTokens int    `json:"max_tokens"`
		Reasoning string `json:"reasoning_effort"`
		Messages  []struct{ Role, Content string }
	}
	finish, content := "stop", "a summary"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body.Reasoning = ""
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%q},"finish_reason":%q}]}`+"\n\n", content, finish)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	client := model.NewClient(model.Options{
		BaseURL: srv.URL, Model: "m", MaxOutputTokens: 16384, RequestTimeout: 2 * time.Second})

	// Reasoning left to the model's configuration: allowance included.
	// (This was what an empty ReasoningEffort meant until "none" became
	// the default; it is EffortInherit now.)
	c := &ModelCompactor{Client: client, ReasoningEffort: EffortInherit}
	got, err := c.CompactWithin([]model.Message{asst("older work")}, 600)
	if err != nil || got != "a summary" {
		t.Fatalf("CompactWithin = %q, %v", got, err)
	}
	if want := 600*SummaryCapFactor + ReasoningAllowance; body.MaxTokens != want {
		t.Errorf("max_tokens = %d, want %d", body.MaxTokens, want)
	}
	if body.Reasoning != "" {
		t.Errorf("reasoning_effort = %q, want it left to the client", body.Reasoning)
	}
	if len(body.Messages) == 0 || !strings.Contains(body.Messages[0].Content, "600 tokens") {
		t.Errorf("the instruction does not state the target: %+v", body.Messages)
	}

	// Reasoning switched off for the compaction: no allowance.
	c = &ModelCompactor{Client: client, ReasoningEffort: "none"}
	if _, err := c.CompactWithin([]model.Message{asst("older work")}, 600); err != nil {
		t.Fatal(err)
	}
	if body.MaxTokens != 600*SummaryCapFactor || body.Reasoning != "none" {
		t.Errorf("reasoning off: max_tokens %d, reasoning_effort %q", body.MaxTokens, body.Reasoning)
	}

	// Cut off at the cap: refused, and an empty one names the likely cause.
	finish = "length"
	if _, err := c.CompactWithin([]model.Message{asst("older work")}, 600); err == nil ||
		!strings.Contains(err.Error(), "cut off") {
		t.Errorf("a summary cut off at the cap: err = %v, want it refused", err)
	}
	content = ""
	if _, err := c.CompactWithin([]model.Message{asst("older work")}, 600); err == nil ||
		!strings.Contains(err.Error(), "compaction_reasoning_effort") {
		t.Errorf("the cap spent before any summary: err = %v, want it to name the setting", err)
	}

	// Without a target nothing changes: the configured cap, no size line.
	finish, content = "stop", "a summary"
	if _, err := (&ModelCompactor{Client: client, ReasoningEffort: EffortInherit}).Compact([]model.Message{asst("older work")}); err != nil {
		t.Fatal(err)
	}
	if body.MaxTokens != 16384 || strings.Contains(body.Messages[0].Content, "tokens (about") {
		t.Errorf("Compact without a target: max_tokens %d", body.MaxTokens)
	}
}

// TestCompactionReasoningDefaultsToNoneAndFallsBackWhenRefused — with
// nothing configured the compaction asks for reasoning_effort "none":
// measured, that is 39.5 s -> 8.6 s on Ollama and 43.7 s -> 15.4 s on
// FreeToken. An endpoint that validates the value and does not know this
// one answers 400 (FreeToken does, to values it does not know); the
// compaction is then repeated with the model's own effort, and the
// refusal is remembered so the session pays for it once.
func TestCompactionReasoningDefaultsToNoneAndFallsBackWhenRefused(t *testing.T) {
	type seen struct {
		effort    string
		maxTokens int
	}
	var requests []seen
	refuseNone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int    `json:"max_tokens"`
			Reasoning string `json:"reasoning_effort"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests = append(requests, seen{body.Reasoning, body.MaxTokens})
		if refuseNone && body.Reasoning == "none" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"reasoning_effort must be one of low, medium, high; got 'none'"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"a summary"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	newClient := func() *model.Client {
		return model.NewClient(model.Options{BaseURL: srv.URL, Model: "m", MaxOutputTokens: 16384,
			ReasoningEffort: "medium", RequestTimeout: 2 * time.Second})
	}
	span := []model.Message{asst("older work")}
	const capNone, capReasoning = 600 * SummaryCapFactor, 600*SummaryCapFactor + ReasoningAllowance

	// Accepted: one request, "none", no reasoning allowance.
	c := &ModelCompactor{Client: newClient()}
	if _, err := c.CompactWithin(span, 600); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0] != (seen{"none", capNone}) {
		t.Fatalf("default: requests %+v, want one with none and cap %d", requests, capNone)
	}

	// Refused: repeated with the model's own effort and the allowance;
	// the inference is announced once, not once per attempt.
	requests, refuseNone = nil, true
	announced := 0
	c = &ModelCompactor{Client: newClient(), OnRequest: func() { announced++ }}
	got, err := c.CompactWithin(span, 600)
	if err != nil || got != "a summary" {
		t.Fatalf("fallback: %q, %v", got, err)
	}
	want := []seen{{"none", capNone}, {"medium", capReasoning}}
	if len(requests) != 2 || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("fallback: requests %+v, want %+v", requests, want)
	}
	if announced != 1 {
		t.Errorf("OnRequest called %d times, want 1", announced)
	}
	// Remembered: the next compaction does not try "none" again.
	requests = nil
	if _, err := c.CompactWithin(span, 600); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0] != want[1] {
		t.Errorf("after a refusal: requests %+v, want only %+v", requests, want[1])
	}

	// Configured explicitly: used as given, and a refusal is an error —
	// a setting that does not work should say so, not be papered over.
	requests = nil
	c = &ModelCompactor{Client: newClient(), ReasoningEffort: "none"}
	if _, err := c.CompactWithin(span, 600); err == nil || len(requests) != 1 {
		t.Errorf("explicit none, refused: err %v, requests %+v; want an error after one request", err, requests)
	}

	// "inherit" is the model's own effort, with the allowance.
	requests = nil
	c = &ModelCompactor{Client: newClient(), ReasoningEffort: "inherit"}
	if _, err := c.CompactWithin(span, 600); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0] != want[1] {
		t.Errorf("inherit: requests %+v, want %+v", requests, want[1])
	}
}
