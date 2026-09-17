package ctxlife

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/model"
)

// -- helpers ------------------------------------------------------

func sys(content string) model.Message  { return model.Message{Role: "system", Content: content} }
func user(content string) model.Message { return model.Message{Role: "user", Content: content} }
func asst(content string) model.Message {
	return model.Message{Role: "assistant", Content: content}
}
func toolResult(id, content string) model.Message {
	return model.Message{Role: "tool", ToolCallID: id, Content: content}
}

// filler returns text of exactly n estimated tokens, so a test can
// state the size it means rather than counting characters. The
// estimator is (len+3)/4, so four characters is one token — an
// earlier version of this used five-character words and produced
// 1.25x what it claimed, which made every fixture fit a budget the
// test meant it to exceed.
func filler(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	return strings.Repeat("abc ", tokens)
}

type fixedCompactor struct {
	summary string
	err     error
	calls   int
	saw     [][]model.Message
}

func (c *fixedCompactor) Compact(msgs []model.Message) (string, error) {
	c.calls++
	c.saw = append(c.saw, msgs)
	return c.summary, c.err
}

// -- the budget ---------------------------------------------------

func TestDefaultBudgetLeavesRoomForOutputAndForEstimationError(t *testing.T) {
	b := DefaultBudget(131072)
	if b.Active() != 131072-DefaultGenerationReserve-DefaultSafetyReserve {
		t.Fatalf("active budget %d", b.Active())
	}
	if b.Active() >= b.ModelLimit {
		t.Fatal("the whole limit was offered to input")
	}
}

func TestASmallModelWindowIsNotSwallowedByFixedReserves(t *testing.T) {
	// With the fixed reserves an 8k model would be left nothing.
	b := DefaultBudget(8192)
	if b.Active() <= 0 {
		t.Fatalf("an 8k model got %d tokens of input budget", b.Active())
	}
	if b.Active() > 8192 {
		t.Fatal("the budget exceeds the limit")
	}
}

func TestAnUnknownLimitIsNotGuessedAt(t *testing.T) {
	b := DefaultBudget(0)
	if b.Known() {
		t.Fatal("an unknown limit produced a budget")
	}
	if err := b.Validate(); err == nil {
		t.Fatal("an unknown limit validated")
	}
}

func TestReservesLargerThanTheLimitAreRefusedWithTheirFigures(t *testing.T) {
	b := Budget{ModelLimit: 1000, GenerationReserve: 900, SafetyReserve: 200}
	err := b.Validate()
	if err == nil {
		t.Fatal("impossible reserves validated")
	}
	for _, want := range []string{"900", "200", "1000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic does not name %s: %v", want, err)
		}
	}
}

// -- §24.1 context below budget is passed without reduction -------

func TestContextBelowBudgetIsPassedThroughUntouched(t *testing.T) {
	m := New(131072)
	in := []model.Message{sys("instructions"), user("do the thing"), asst("done")}
	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("messages changed: %d -> %d", len(in), len(out))
	}
	if m.Stats.Reductions != 0 || m.Stats.ToolResultsPruned != 0 {
		t.Fatalf("something was reduced unnecessarily: %+v", m.Stats)
	}
	if !acct.WithinBudget() {
		t.Fatal("a tiny context was reported over budget")
	}
}

// -- §24.2 old tool results are pruned when required --------------

func TestOldToolResultsArePrunedWhenTheBudgetRequiresIt(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 2
	in := []model.Message{sys("instructions")}
	for i := 0; i < 10; i++ {
		in = append(in, asst(fmt.Sprintf("calling %d", i)),
			toolResult(fmt.Sprintf("c%d", i), filler(400)))
	}
	in = append(in, user("continue"))

	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("nothing was pruned")
	}
	if !acct.WithinBudget() {
		t.Fatalf("still over budget after pruning: %d of %d", acct.Total, acct.Budget)
	}
	if len(out) != len(in) {
		t.Fatal("pruning changed the message count; it replaces content in place")
	}
	if m.Stats.TokensPruned <= 0 {
		t.Fatal("pruning reported no tokens saved")
	}
}

// -- §24.3 recent tool results remain -----------------------------

func TestRecentToolResultsStayVerbatim(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 4
	newest := filler(400)
	in := []model.Message{sys("instructions")}
	for i := 0; i < 10; i++ {
		in = append(in, asst("calling"), toolResult("c", filler(400)))
	}
	in[len(in)-1] = toolResult("newest", newest)
	in = append(in, user("continue"))

	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, msg := range out {
		if msg.ToolCallID == "newest" {
			found = true
			if msg.Content != newest {
				t.Fatal("the newest tool result was pruned")
			}
		}
	}
	if !found {
		t.Fatal("the newest tool result disappeared")
	}
}

func TestPruningStopsAsSoonAsTheBudgetIsMet(t *testing.T) {
	m := New(16384)
	m.KeepRecentTurns = 2
	in := []model.Message{sys("instructions")}
	for i := 0; i < 20; i++ {
		in = append(in, asst("calling"), toolResult(fmt.Sprintf("c%d", i), filler(800)))
	}
	in = append(in, user("continue"))
	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("nothing was pruned")
	}
	if m.Stats.ToolResultsPruned >= 18 {
		t.Fatalf("pruned %d of 18 reducible results; it should stop when it fits",
			m.Stats.ToolResultsPruned)
	}
}

func TestAnAlreadyPrunedResultIsNotPrunedAgain(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 1
	in := []model.Message{sys("instructions")}
	for i := 0; i < 6; i++ {
		in = append(in, toolResult("c", fmt.Sprintf(prunedTemplate, 5000)))
	}
	in = append(in, user("continue"))
	_, _, _ = m.Fit(in)
	if m.Stats.ToolResultsPruned != 0 {
		t.Fatalf("re-pruned %d placeholders", m.Stats.ToolResultsPruned)
	}
}

func TestAPrunedResultSaysWhatWasThere(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 1
	in := []model.Message{sys("i")}
	for i := 0; i < 10; i++ {
		in = append(in, toolResult("c", filler(400)))
	}
	in = append(in, user("continue"))
	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	var sawPlaceholder bool
	for _, msg := range out {
		if isPruned(msg.Content) {
			sawPlaceholder = true
			if !strings.Contains(msg.Content, "tokens") {
				t.Fatal("the placeholder does not say how much was removed")
			}
			if msg.Role != "tool" || msg.ToolCallID == "" {
				t.Fatal("pruning broke the tool-result envelope")
			}
		}
	}
	if !sawPlaceholder {
		t.Fatal("no placeholder was produced")
	}
}

// -- §24.4 compaction when pruning is insufficient -----------------

func TestCompactionHappensWhenPruningCannotReachTheBudget(t *testing.T) {
	c := &fixedCompactor{summary: "objective: x. decisions: y. next: z."}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("instructions")}
	for i := 0; i < 30; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("continue"))

	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Fatalf("the compactor was called %d times", c.calls)
	}
	if m.Stats.Compactions != 1 {
		t.Fatalf("compactions %d", m.Stats.Compactions)
	}
	if !acct.WithinBudget() {
		t.Fatalf("over budget after compaction: %d of %d", acct.Total, acct.Budget)
	}
	if len(out) >= len(in) {
		t.Fatal("compaction did not shorten the message list")
	}
}

func TestPruningIsTriedBeforeCompaction(t *testing.T) {
	c := &fixedCompactor{summary: "summary"}
	m := New(8192)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("instructions")}
	for i := 0; i < 12; i++ {
		in = append(in, asst("calling"), toolResult("c", filler(700)))
	}
	in = append(in, user("continue"))
	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("pruning did not run")
	}
	if c.calls != 0 {
		t.Fatal("compaction ran even though pruning was enough")
	}
}

// -- §24.5 recent conversation stays verbatim after compaction ----

func TestRecentConversationSurvivesCompactionVerbatim(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	m := New(4096)
	m.KeepRecentTurns = 3
	m.Compactor = c
	in := []model.Message{sys("instructions")}
	for i := 0; i < 30; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, asst("second to last"), asst("last"), user("the task"))

	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(out))
	for _, msg := range out {
		joined = append(joined, msg.Content)
	}
	text := strings.Join(joined, "\n")
	for _, want := range []string{"second to last", "last", "the task"} {
		if !strings.Contains(text, want) {
			t.Errorf("recent message %q did not survive compaction", want)
		}
	}
}

func TestTheCompactorOnlySeesReducibleHistory(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	m := New(4096)
	m.KeepRecentTurns = 3
	m.Compactor = c
	in := []model.Message{sys("PINNED INSTRUCTIONS")}
	for i := 0; i < 30; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, asst("recent one"), asst("recent two"), user("THE TASK"))
	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	for _, msg := range c.saw[0] {
		if msg.Content == "PINNED INSTRUCTIONS" || msg.Content == "THE TASK" ||
			msg.Content == "recent one" || msg.Content == "recent two" {
			t.Fatalf("the compactor was handed %q, which is not reducible", msg.Content)
		}
	}
}

// -- §24.6 pinned context survives --------------------------------

func TestPinnedContextSurvivesEveryReduction(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("SYSTEM ONE"), sys("GOVERNANCE TWO")}
	for i := 0; i < 30; i++ {
		in = append(in, asst("calling"), toolResult("c", filler(120)))
	}
	in = append(in, user("THE CURRENT TASK"))

	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, msg := range out {
		texts = append(texts, msg.Content)
	}
	text := strings.Join(texts, "\n")
	for _, want := range []string{"SYSTEM ONE", "GOVERNANCE TWO", "THE CURRENT TASK"} {
		if !strings.Contains(text, want) {
			t.Errorf("pinned content %q was removed to satisfy the budget", want)
		}
	}
}

func TestTheCurrentTaskIsPinnedEvenWhenItIsOld(t *testing.T) {
	m := New(131072)
	m.KeepRecentTurns = 1
	in := []model.Message{sys("i"), user("THE TASK"), asst("a"), asst("b"), asst("c")}
	prios := m.Classify(in)
	if prios[1] != Pinned {
		t.Fatalf("the last user message got priority %v", prios[1])
	}
}

func TestSystemMessagesArePinnedWhereverTheyAppear(t *testing.T) {
	m := New(131072)
	m.KeepRecentTurns = 0
	in := []model.Message{asst("a"), sys("injected later"), asst("b")}
	prios := m.Classify(in)
	if prios[1] != Pinned {
		t.Fatal("a system message in the middle was not pinned")
	}
}

// -- §24.7 the result is within budget ----------------------------

func TestTheReturnedContextIsAlwaysWithinBudgetOrAnError(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	for _, size := range []int{5, 20, 60, 200} {
		m := New(4096)
		m.KeepRecentTurns = 2
		m.Compactor = c
		in := []model.Message{sys("i")}
		for i := 0; i < size; i++ {
			in = append(in, asst("calling"), toolResult("c", filler(100)))
		}
		in = append(in, user("task"))
		out, acct, err := m.Fit(in)
		if err != nil {
			continue // an explicit failure is an allowed outcome
		}
		if !acct.WithinBudget() {
			t.Fatalf("size %d: returned %d tokens against a %d budget",
				size, acct.Total, acct.Budget)
		}
		if got := m.Account(out).Total; got > acct.Budget {
			t.Fatalf("size %d: remeasuring the result gives %d over %d",
				size, got, acct.Budget)
		}
	}
}

// -- §24.8 durable history is not destroyed -----------------------

func TestTheCallersOwnMessagesAreNeverModified(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	original := filler(400)
	in := []model.Message{sys("i")}
	for i := 0; i < 20; i++ {
		in = append(in, asst("calling"), toolResult("c", original))
	}
	in = append(in, user("task"))
	before := make([]model.Message, len(in))
	copy(before, in)

	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i].Content != before[i].Content {
			t.Fatalf("message %d was modified in the caller's slice", i)
		}
	}
}

// -- §24.9 explicit failure when pinned alone is too large --------

func TestPinnedContextLargerThanTheBudgetFailsExplicitly(t *testing.T) {
	m := New(4096)
	in := []model.Message{sys(filler(5000)), user("task")}
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("oversized pinned context did not fail")
	}
	var pe *PinnedExceedsBudgetError
	if !errors.As(err, &pe) {
		t.Fatalf("wrong error type: %T %v", err, err)
	}
	if pe.Budget <= 0 || pe.Pinned <= pe.Budget {
		t.Fatalf("the error does not carry usable figures: %+v", pe)
	}
	if !strings.Contains(err.Error(), "exceeds the active budget") {
		t.Fatalf("the message does not say what happened: %v", err)
	}
}

func TestToolSchemasCountAgainstTheBudget(t *testing.T) {
	m := New(4096)
	m.ToolSchemaTokens = 5000
	in := []model.Message{sys("i"), user("task")}
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("an oversized tool surface did not fail")
	}
	if !strings.Contains(err.Error(), "tool schemas") {
		t.Fatalf("the diagnostic does not mention tool schemas: %v", err)
	}
}

func TestFailureWithoutACompactorSaysSo(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 200
	in := []model.Message{sys("i")}
	for i := 0; i < 60; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("task"))
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("an unreducible context did not fail")
	}
	if !strings.Contains(err.Error(), "no compactor") {
		t.Fatalf("the diagnostic does not name the missing compactor: %v", err)
	}
}

func TestACompactionFailureIsReportedRatherThanHidden(t *testing.T) {
	c := &fixedCompactor{err: errors.New("the model refused")}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 60; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("task"))
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("a failed compaction produced no error")
	}
	if m.Stats.CompactionFailures != 1 {
		t.Fatalf("compaction failures %d", m.Stats.CompactionFailures)
	}
	if !strings.Contains(err.Error(), "compaction failed") {
		t.Fatalf("the diagnostic does not say compaction failed: %v", err)
	}
}

func TestASummaryNoSmallerThanWhatItReplacesIsRefused(t *testing.T) {
	c := &fixedCompactor{summary: filler(10000)}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 30; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("task"))
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("a summary larger than its input was accepted")
	}
	if m.Stats.Compactions != 0 {
		t.Fatal("a refused compaction was counted as one")
	}
}

func TestAnEmptySummaryIsRefused(t *testing.T) {
	c := &fixedCompactor{summary: "   "}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 40; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("task"))
	if _, _, err := m.Fit(in); err == nil {
		t.Fatal("an empty summary was accepted")
	}
}

// -- an unknown limit ---------------------------------------------

func TestAnUnknownLimitPassesThroughRatherThanRefusingToRun(t *testing.T) {
	m := New(0)
	in := []model.Message{sys("i"), user("task"), asst(filler(10000))}
	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatalf("an unknown limit refused to run: %v", err)
	}
	if len(out) != len(in) {
		t.Fatal("an unknown limit reduced something")
	}
	if acct.WithinBudget() {
		t.Fatal("an unknown budget reported itself satisfied")
	}
}

// -- accounting and observability ---------------------------------

func TestAccountingSeparatesTheCategoriesItReports(t *testing.T) {
	m := New(131072)
	m.KeepRecentTurns = 1
	m.ToolSchemaTokens = 100
	in := []model.Message{sys(filler(50)), asst(filler(30)),
		toolResult("c", filler(40)), user(filler(10))}
	a := m.Account(in)
	if a.Pinned == 0 || a.Reducible == 0 {
		t.Fatalf("categories not separated: %+v", a)
	}
	if a.ToolSchemas != 100 {
		t.Fatalf("tool schemas %d", a.ToolSchemas)
	}
	if a.ToolResults == 0 {
		t.Fatal("tool results were not counted")
	}
	if a.Total != a.Pinned+a.Recent+a.Reducible+a.ToolSchemas {
		t.Fatalf("the total does not add up: %+v", a)
	}
}

func TestUtilizationIsZeroRatherThanInfiniteWithoutABudget(t *testing.T) {
	a := Accounting{Total: 100}
	if a.Utilization() != 0 {
		t.Fatal("utilization against an unknown budget was not zero")
	}
}

func TestThePeakIsRemembered(t *testing.T) {
	m := New(131072)
	m.Fit([]model.Message{sys("i"), user(filler(500))})
	peak := m.Stats.PeakActiveTokens
	m.Fit([]model.Message{sys("i"), user("small")})
	if m.Stats.PeakActiveTokens != peak {
		t.Fatalf("the peak moved down to %d", m.Stats.PeakActiveTokens)
	}
	if m.Stats.Inferences != 2 {
		t.Fatalf("inferences %d", m.Stats.Inferences)
	}
}

func TestACompactedSummaryIsRecognisable(t *testing.T) {
	c := &fixedCompactor{summary: "the summary"}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 40; i++ {
		in = append(in, asst(filler(120)))
	}
	in = append(in, user("task"))
	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, msg := range out {
		if IsCompacted(msg) {
			found = true
		}
	}
	if !found {
		t.Fatal("the summary is not recognisable as one, so it could be compacted again")
	}
}

func TestToolCallArgumentsAreCountedNotIgnored(t *testing.T) {
	bare := model.Message{Role: "assistant", Content: "x"}
	withCall := model.Message{Role: "assistant", Content: "x", ToolCalls: []model.ToolCall{
		{Name: "write_file", ArgumentsRaw: filler(200)}}}
	if MessageTokens(withCall) <= MessageTokens(bare)+100 {
		t.Fatal("a 200-token tool call cost nothing")
	}
}

func TestParsedArgumentsAreCountedWhenTheRawFormIsAbsent(t *testing.T) {
	msg := model.Message{Role: "assistant", ToolCalls: []model.ToolCall{
		{Name: "t", Arguments: map[string]any{"path": filler(100)}}}}
	if MessageTokens(msg) < 100 {
		t.Fatalf("parsed arguments cost %d tokens", MessageTokens(msg))
	}
}

// -- §16 observability --------------------------------------------

func TestTheReportNamesEveryFigureTheAddendumAsksFor(t *testing.T) {
	m := New(131072)
	m.ToolSchemaTokens = 8870
	in := []model.Message{sys(filler(500)), asst(filler(300)),
		toolResult("c", filler(400)), user("task")}
	a := m.Account(in)
	text := m.Report(a)
	for _, want := range []string{
		"Model context limit", "Active input budget", "Generation reserve",
		"Safety reserve", "Pinned", "Recent verbatim", "Reducible",
		"Tool schemas", "Active context", "Budget utilization",
		"Tool results pruned", "Compactions", "Peak active context",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report does not show %q", want)
		}
	}
}

func TestTheReportSaysWhenTheContextIsNotBounded(t *testing.T) {
	m := New(0)
	text := m.Report(m.Account([]model.Message{user("task")}))
	if !strings.Contains(text, "unknown") || !strings.Contains(text, "not bounded") {
		t.Fatalf("an unbounded context was reported as if it were bounded:\n%s", text)
	}
}

func TestTheSummaryReportsWhatReductionDid(t *testing.T) {
	c := &fixedCompactor{summary: "compact"}
	m := New(4096)
	m.KeepRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 20; i++ {
		in = append(in, asst("calling"), toolResult("c", filler(400)))
	}
	in = append(in, user("task"))
	_, a, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Summary(a)
	if !strings.Contains(s, "pruned") {
		t.Fatalf("the summary does not mention pruning: %s", s)
	}
	if !strings.Contains(s, "/") {
		t.Fatalf("the summary does not show the budget: %s", s)
	}
}

func TestUtilizationIsCheckableAgainstTheFiguresBesideIt(t *testing.T) {
	m := New(131072)
	in := []model.Message{sys(filler(1000)), user("task")}
	a := m.Account(in)
	want := float64(a.Total) / float64(a.Budget)
	if a.Utilization() != want {
		t.Fatalf("utilization %v does not match %d/%d", a.Utilization(), a.Total, a.Budget)
	}
}

// -- §24.10/§24.11 the wire stays valid after reduction -----------

// pairingIsValid checks the invariant every OpenAI-compatible
// endpoint enforces: every tool message answers a tool_call that is
// present earlier in the list, and every tool_call is answered.
// A reduction that breaks this produces a 400 from the runtime,
// which is the worst way to find out.
func pairingIsValid(msgs []model.Message) error {
	open := map[string]bool{}
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls {
			open[call.ID] = true
		}
		if msg.Role == "tool" {
			if msg.ToolCallID == "" {
				return fmt.Errorf("a tool message carries no tool_call_id")
			}
			if !open[msg.ToolCallID] {
				return fmt.Errorf("tool result %q answers a call that is not "+
					"in the list", msg.ToolCallID)
			}
			delete(open, msg.ToolCallID)
		}
	}
	if len(open) > 0 {
		for id := range open {
			return fmt.Errorf("tool call %q was never answered", id)
		}
	}
	return nil
}

func callAndResult(id string, resultSize int) []model.Message {
	return []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{
			{ID: id, Name: "read_file", ArgumentsRaw: `{"path":"x"}`}}},
		{Role: "tool", ToolCallID: id, Content: filler(resultSize)},
	}
}

func TestPruningKeepsEveryToolCallAnsweredAndEveryAnswerCalled(t *testing.T) {
	m := New(4096)
	m.KeepRecentTurns = 3
	in := []model.Message{sys("instructions")}
	for i := 0; i < 20; i++ {
		in = append(in, callAndResult(fmt.Sprintf("call-%d", i), 400)...)
	}
	in = append(in, user("continue"))
	if err := pairingIsValid(in); err != nil {
		t.Fatalf("the fixture is already invalid: %v", err)
	}

	out, _, err := m.Fit(in)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("nothing was pruned, so this proves nothing")
	}
	if err := pairingIsValid(out); err != nil {
		t.Fatalf("pruning broke the tool-call pairing: %v", err)
	}
}

func TestCompactionKeepsEveryToolCallAnsweredAndEveryAnswerCalled(t *testing.T) {
	c := &fixedCompactor{summary: "objective x, next y"}
	// Every recent-window size is tried, because the defect this
	// guards is a boundary that falls between a call and its
	// answer — which only happens at some sizes.
	for keep := 0; keep <= 8; keep++ {
		m := New(4096)
		m.KeepRecentTurns = keep
		m.Compactor = c
		in := []model.Message{sys("instructions")}
		for i := 0; i < 24; i++ {
			in = append(in, callAndResult(fmt.Sprintf("call-%d", i), 300)...)
		}
		in = append(in, user("continue"))

		out, _, err := m.Fit(in)
		if err != nil {
			continue // an explicit failure is allowed
		}
		if err := pairingIsValid(out); err != nil {
			t.Fatalf("keep=%d: reduction broke the tool-call pairing: %v", keep, err)
		}
	}
}

func TestAToolCallAndItsAnswerAreNeverSplitByTheRecentWindow(t *testing.T) {
	for keep := 0; keep <= 8; keep++ {
		m := New(131072)
		m.KeepRecentTurns = keep
		in := []model.Message{sys("i")}
		for i := 0; i < 6; i++ {
			in = append(in, callAndResult(fmt.Sprintf("c%d", i), 10)...)
		}
		in = append(in, user("task"))
		prios := m.Classify(in)
		for i, msg := range in {
			if msg.Role != "tool" {
				continue
			}
			// The assistant message that made this call is the
			// one before it in these fixtures.
			if prios[i] != prios[i-1] {
				t.Fatalf("keep=%d: call at %d is %v but its answer at %d is %v; "+
					"a reduction can then remove one and leave the other",
					keep, i-1, prios[i-1], i, prios[i])
			}
		}
	}
}

// -- §14 step 12: the last safe reduction -------------------------

// The case this exists for, found by a real benchmark rather than
// imagined: a 16k budget, a model reading source files, and a recent
// window of eight whose tool results alone exceed the budget. Pruning
// and compaction both ran and the run still failed.
func TestARecentWindowTooLargeForTheBudgetIsNarrowedRatherThanFailing(t *testing.T) {
	c := &fixedCompactor{summary: "objective x"}
	m := New(16384)
	m.KeepRecentTurns = 8
	m.Compactor = c
	in := []model.Message{sys("instructions")}
	for i := 0; i < 12; i++ {
		in = append(in, callAndResult(fmt.Sprintf("c%d", i), 3000)...)
	}
	in = append(in, user("continue"))

	out, acct, err := m.Fit(in)
	if err != nil {
		t.Fatalf("a run that could have continued failed instead: %v", err)
	}
	if !acct.WithinBudget() {
		t.Fatalf("still over budget: %d of %d", acct.Total, acct.Budget)
	}
	if m.Stats.RecentWindowNarrowings == 0 {
		t.Fatal("it fit without narrowing, so this proves nothing")
	}
	if err := pairingIsValid(out); err != nil {
		t.Fatalf("narrowing broke the tool-call pairing: %v", err)
	}
}

func TestNarrowingLeavesTheConfiguredWindowUnchangedForTheNextInference(t *testing.T) {
	c := &fixedCompactor{summary: "x"}
	m := New(16384)
	m.KeepRecentTurns = 8
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 12; i++ {
		in = append(in, callAndResult(fmt.Sprintf("c%d", i), 3000)...)
	}
	in = append(in, user("task"))
	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	if m.KeepRecentTurns != 8 {
		t.Fatalf("the policy was changed to %d; narrowing is a decision about "+
			"one inference, not a new setting", m.KeepRecentTurns)
	}
}

func TestTheWindowIsNeverNarrowedBelowItsFloor(t *testing.T) {
	// Every message is enormous, so no width fits and the floor is
	// reached. A model that cannot see what it just did cannot
	// continue doing it, so this must fail rather than empty the
	// window.
	c := &fixedCompactor{summary: "x"}
	m := New(4096)
	m.KeepRecentTurns = 8
	m.MinRecentTurns = 2
	m.Compactor = c
	in := []model.Message{sys("i")}
	for i := 0; i < 10; i++ {
		in = append(in, callAndResult(fmt.Sprintf("c%d", i), 4000)...)
	}
	in = append(in, user("task"))
	_, _, err := m.Fit(in)
	if err == nil {
		t.Fatal("the window was narrowed past its floor")
	}
	if !strings.Contains(err.Error(), "will not narrow below 2") {
		t.Fatalf("the diagnostic does not say where it stopped: %v", err)
	}
}

func TestNarrowingIsNotReachedWhenPruningAloneSuffices(t *testing.T) {
	m := New(16384)
	m.KeepRecentTurns = 4
	in := []model.Message{sys("i")}
	for i := 0; i < 20; i++ {
		in = append(in, callAndResult(fmt.Sprintf("c%d", i), 800)...)
	}
	in = append(in, user("task"))
	if _, _, err := m.Fit(in); err != nil {
		t.Fatal(err)
	}
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("pruning did not run, so this proves nothing")
	}
	if m.Stats.RecentWindowNarrowings != 0 {
		t.Fatal("the window was narrowed even though pruning was enough")
	}
}
