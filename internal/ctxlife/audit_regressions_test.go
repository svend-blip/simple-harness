package ctxlife

import (
	"context"
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
