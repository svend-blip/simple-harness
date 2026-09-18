package ctxlife

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
