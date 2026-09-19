package ctxlife

import (
	"fmt"
	"strings"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/model"
)

// Priority is §8's retention order. A message's priority decides
// what may happen to it, and nothing else does: the reduction steps
// read priority and never guess from content.
type Priority int

const (
	// Pinned must remain. System instructions, governance, the
	// tool surface, the current task. §8: pinned context must not
	// be silently removed to satisfy a budget, and if pinned
	// context alone cannot fit, the run fails and says so.
	Pinned Priority = iota
	// Recent is verbatim working history — the last few turns,
	// which the model needs in full to continue what it is doing.
	Recent
	// Reducible is older history: prunable tool results first,
	// then compactable conversation.
	Reducible
)

func (p Priority) String() string {
	switch p {
	case Pinned:
		return "pinned"
	case Recent:
		return "recent"
	default:
		return "reducible"
	}
}

// Compactor turns older conversation into a compact working summary.
// It is an interface because §10 allows compaction to use the active
// model or another configured model, and because a manager with no
// compactor must still prune — the absence of one is a configuration,
// not an error.
type Compactor interface {
	// Compact returns a summary of the given messages. The
	// returned text becomes one system message in the active
	// context. An error means compaction did not happen; the
	// caller reports that rather than pretending it did.
	Compact(messages []model.Message) (string, error)
}

// Stats is §16's observable surface. Every field is a count or a
// token figure the manager actually produced, not a derived rate.
type Stats struct {
	Inferences             int
	Reductions             int
	ToolResultsPruned      int
	ToolResultsTruncated   int
	TokensPruned           int
	Compactions            int
	TokensCompacted        int
	CompactionFailures     int
	RecentWindowNarrowings int
	PeakActiveTokens       int
	LastActiveTokens       int
}

// Accounting is §4's full picture of one candidate context.
type Accounting struct {
	Pinned       int
	Recent       int
	Reducible    int
	Compacted    int // tokens held by compaction summaries (pinned)
	ToolSchemas  int
	ToolResults  int
	Conversation int
	Total        int
	Budget       int
	Messages     int
}

// Utilization is the share of the active budget in use, or 0 when
// there is no budget to be a share of.
func (a Accounting) Utilization() float64 {
	if a.Budget <= 0 {
		return 0
	}
	return float64(a.Total) / float64(a.Budget)
}

// WithinBudget reports whether this candidate may be sent as it is.
// An unknown budget is *not* within budget in the sense that matters
// — it is unbounded, which callers must handle explicitly.
func (a Accounting) WithinBudget() bool { return a.Budget > 0 && a.Total <= a.Budget }

// Manager decides what active context goes to the model.
//
// It holds no history of its own. Each call takes the caller's full
// candidate list and returns a bounded view of it; the caller keeps
// its own messages, and whatever durable session record exists is
// never touched. That is the §3 and §20 distinction made structural
// rather than promised.
type Manager struct {
	Budget Budget

	// KeepRecentTurns is how many trailing messages are held
	// verbatim and never reduced. §9 and §10 both require recent
	// history to survive, and this is the one number that says
	// how much "recent" is.
	KeepRecentTurns int

	// PruneToolResults enables §9. On by default because it is
	// the cheapest reduction and needs no inference.
	PruneToolResults bool

	// MinRecentTurns is the number of trailing messages that stay
	// verbatim even when the window is narrowed to make a run fit.
	// Zero means DefaultMinRecentTurns. A model that cannot see
	// what it just did cannot continue doing it.
	MinRecentTurns int

	// MinSummaryRoom is what must be left of the reducible span, once
	// the deficit is taken out of it, before a compaction inference is
	// spent on it. Zero means DefaultMinSummaryRoom.
	MinSummaryRoom int

	// Compactor enables §10. Nil means conversation compaction is
	// unavailable, which is reported rather than worked around.
	Compactor Compactor

	// ToolSchemaTokens is the tool surface's cost, which the loop
	// knows and the message list does not carry.
	ToolSchemaTokens int

	// LimitSource says where Budget.ModelLimit came from (a flag, the
	// configuration, the runtime), for the report.
	LimitSource string

	Stats Stats
}

// Defaults chosen so that the harness behaves safely with no
// configuration at all, per §17.
const (
	DefaultKeepRecentTurns = 8
	// DefaultMinSummaryRoom is the room a compaction summary needs.
	// The smallest summary measured against real models was 464
	// tokens (the instruction asks for six headings); the largest,
	// 1 424.
	DefaultMinSummaryRoom = 512
	DefaultMinRecentTurns = 2

	// The placeholder that replaces a pruned tool result. It says
	// what was there and how big it was, so a reader of the
	// active context can tell that something was removed rather
	// than that a tool returned nothing — those are very
	// different, and only one of them is a bug.
	prunedTemplate = "[Earlier tool result pruned from active context: ~%d tokens]"

	compactedHeader = "COMPACTED WORKING HISTORY"
)

// New returns a manager with the addendum's safe defaults for a
// given model context limit.
func New(modelLimit int) *Manager {
	return &Manager{
		Budget:           DefaultBudget(modelLimit),
		KeepRecentTurns:  DefaultKeepRecentTurns,
		PruneToolResults: true,
	}
}

// -- accounting ---------------------------------------------------

// Classify assigns each message a priority.
//
// System messages are pinned wherever they appear: they are the
// harness's and the governance's instructions, and §8 forbids
// removing them to make room. The final user message is pinned too —
// it is the current task, and a harness that dropped the question to
// fit the history would be answering the wrong thing. Everything in
// the trailing KeepRecentTurns window is Recent; the rest is
// Reducible.
func (m *Manager) Classify(messages []model.Message) []Priority {
	out := make([]Priority, len(messages))
	keep := m.KeepRecentTurns
	if keep < 0 {
		keep = 0
	}
	recentFrom := len(messages) - keep
	lastUser := -1
	for i, msg := range messages {
		if msg.Role == "user" {
			lastUser = i
		}
	}
	for i, msg := range messages {
		switch {
		case msg.Role == "system":
			out[i] = Pinned
		case IsCompacted(msg):
			// A summary of what was already compacted: reducing
			// it again would summarise a summary.
			out[i] = Pinned
		case i == lastUser:
			out[i] = Pinned
		case i >= recentFrom:
			out[i] = Recent
		default:
			out[i] = Reducible
		}
	}
	keepToolPairsTogether(messages, out)
	return out
}

// keepToolPairsTogether gives a tool call and its answer the same
// priority, taking the more-retained of the two.
//
// Without this the recent window can fall between them, and then a
// reduction removes one and leaves the other: an assistant message
// whose tool_calls have no answers, or a tool message answering a
// call that is no longer in the list. Every OpenAI-compatible
// endpoint rejects both with a 400, which is the worst way to find
// out — mid-run, on a request the harness built itself.
func keepToolPairsTogether(messages []model.Message, prios []Priority) {
	callerOf := make(map[string]int)
	for i, msg := range messages {
		for _, call := range msg.ToolCalls {
			callerOf[call.ID] = i
		}
	}
	for i, msg := range messages {
		if msg.Role != "tool" || msg.ToolCallID == "" {
			continue
		}
		j, ok := callerOf[msg.ToolCallID]
		if !ok {
			continue // an orphan already; reduction cannot make it worse
		}
		// Lower Priority values are more retained.
		if prios[j] < prios[i] {
			prios[i] = prios[j]
		} else if prios[i] < prios[j] {
			prios[j] = prios[i]
		}
	}
}

// MessageTokens is the estimated cost of one message on the wire,
// including its tool calls and the small per-message envelope the
// chat format adds. Estimation goes through the existing
// context.Estimate so that this package and `context show` cannot
// disagree about what a token is.
func MessageTokens(msg model.Message) int {
	n := contextpkg.Estimate(msg.Content)
	for _, call := range msg.ToolCalls {
		n += contextpkg.Estimate(call.Name)
		// ArgumentsRaw is what actually went on the wire when it
		// is present; the parsed map is a reconstruction and can
		// differ in whitespace. Prefer the real bytes.
		if call.ArgumentsRaw != "" {
			n += contextpkg.Estimate(call.ArgumentsRaw)
		} else {
			n += estimateArgs(call.Arguments)
		}
	}
	// Role, delimiters and the tool-call scaffolding. Small, but
	// a long conversation is mostly envelopes.
	return n + 4
}

// estimateArgs costs a parsed argument map without re-marshalling
// it. Marshalling here would be exact but would also mean this
// function could fail, and an accounting function that can fail is
// one that callers will be tempted to ignore.
func estimateArgs(args map[string]any) int {
	if len(args) == 0 {
		return 0
	}
	n := 2 // the braces
	for k, v := range args {
		n += contextpkg.Estimate(k) + 4 // quotes, colon, comma
		n += contextpkg.Estimate(fmt.Sprint(v))
	}
	return n
}

// Account measures a candidate message list against the budget.
func (m *Manager) Account(messages []model.Message) Accounting {
	a := Accounting{Budget: m.Budget.Active(), Messages: len(messages),
		ToolSchemas: m.ToolSchemaTokens}
	prios := m.Classify(messages)
	for i, msg := range messages {
		n := MessageTokens(msg)
		switch prios[i] {
		case Pinned:
			a.Pinned += n
		case Recent:
			a.Recent += n
		default:
			a.Reducible += n
		}
		if IsCompacted(msg) {
			a.Compacted += n
		}
		if msg.Role == "tool" {
			a.ToolResults += n
		} else if msg.Role != "system" {
			a.Conversation += n
		}
	}
	a.Total = a.Pinned + a.Recent + a.Reducible + a.ToolSchemas
	return a
}

// -- the lifecycle ------------------------------------------------

// PinnedExceedsBudgetError is §21's explicit failure: the context
// cannot be made safe by any reduction this manager is allowed to
// perform, because what must remain is already too large.
type PinnedExceedsBudgetError struct {
	Pinned      int
	ToolSchemas int
	Budget      int
}

func (e *PinnedExceedsBudgetError) Error() string {
	return fmt.Sprintf("ctxlife: pinned context (%d tokens) plus tool schemas "+
		"(%d) exceeds the active budget (%d); reduce the instructions, the "+
		"skills or the tool surface, or raise the model context limit",
		e.Pinned, e.ToolSchemas, e.Budget)
}

// Fit returns the active context to send for the next inference.
//
// The order is §14's: account, and pass the candidate through
// untouched if it fits; otherwise prune old tool results and
// remeasure; otherwise compact older conversation and remeasure;
// otherwise fail explicitly. Each step is skipped if it cannot help,
// and none of them touches the caller's slice — the returned list is
// always a new one when anything changed.
func (m *Manager) Fit(messages []model.Message) ([]model.Message, Accounting, error) {
	m.Stats.Inferences++

	acct := m.Account(messages)
	if !m.Budget.Known() {
		// Nothing to bound against. Report it and pass through:
		// refusing to run because a limit was not configured
		// would be worse than the unbounded behaviour that
		// preceded this package.
		m.Stats.LastActiveTokens = acct.Total
		m.trackPeak(acct.Total)
		return messages, acct, nil
	}
	if acct.WithinBudget() {
		m.Stats.LastActiveTokens = acct.Total
		m.trackPeak(acct.Total)
		return messages, acct, nil
	}

	// §21: check the floor before doing work that cannot reach it.
	if acct.Pinned+acct.ToolSchemas > acct.Budget {
		return nil, acct, &PinnedExceedsBudgetError{
			Pinned: acct.Pinned, ToolSchemas: acct.ToolSchemas, Budget: acct.Budget}
	}

	// A reduction is counted when one was applied, not when one
	// was attempted: a Fit that failed with nothing reduced used
	// to report one.
	reduced := false
	defer func() {
		if reduced {
			m.Stats.Reductions++
		}
		m.Stats.LastActiveTokens = acct.Total
		m.trackPeak(acct.Total)
	}()
	working := messages

	if m.PruneToolResults {
		pruned, n, tokens := m.pruneToolResults(working)
		if n > 0 {
			reduced = true
			working = pruned
			m.Stats.ToolResultsPruned += n
			m.Stats.TokensPruned += tokens
			acct = m.Account(working)
			if acct.WithinBudget() {
				m.Stats.LastActiveTokens = acct.Total
				m.trackPeak(acct.Total)
				return working, acct, nil
			}
		}
	}

	// Compaction is a model inference; every other reduction is free.
	// It is attempted here only when it could reach the budget by
	// itself: when the reducible span, less the deficit, still leaves
	// room for a summary. Otherwise the free reductions below go
	// first, and compaction is tried again after them if the context
	// still does not fit.
	//
	// Measured 2026-09-19 on a file-reading task: the deficit sat in
	// large tool results inside the recent window, the span was a few
	// hundred tokens of one-line remarks and pruned placeholders, and
	// the summaries came back at 464-1 424 tokens. Each inference cost
	// 13-35 s and the run's compactions saved ~8 tokens between them
	// — 28 % of the wall time against FreeToken, 63 % against Ollama —
	// before narrowing the window did the actual work.
	tryCompact := func() bool {
		compacted, n, tokens, err := m.compact(working)
		switch {
		case err != nil:
			m.Stats.CompactionFailures++
			// Not a reason to stop, but a reason the final
			// failure below must be able to name.
		case n > 0:
			reduced = true
			working = compacted
			m.Stats.Compactions++
			m.Stats.TokensCompacted += tokens
			acct = m.Account(working)
		}
		return acct.WithinBudget()
	}
	compactionDeferred := false
	if m.Compactor != nil {
		if m.compactionCanPay(working, acct) {
			if tryCompact() {
				return working, acct, nil
			}
		} else {
			compactionDeferred = true
		}
	}

	// §14 step 12: additional safe reduction, when the alternative
	// is failing a run that could have continued.
	//
	// The recent verbatim window is a fixed count of messages, and
	// with large tool results a window of eight can exceed a small
	// budget on its own — measured against a real model reading
	// source files at a 16k limit, where pruning and compaction both
	// ran and the run still failed. Narrowing the window makes those
	// results prunable too.
	//
	// It narrows rather than disappears: MinRecentTurns messages stay
	// verbatim whatever happens, because a model that cannot see what
	// it just did cannot continue doing it. Reaching that floor and
	// still not fitting is a real failure, and is reported as one.
	if m.PruneToolResults && m.KeepRecentTurns > m.minRecent() {
		if narrowed, n, tokens, ok := m.narrowAndPrune(working, &acct); ok {
			reduced = true
			working = narrowed
			m.Stats.RecentWindowNarrowings++
			m.Stats.ToolResultsPruned += n
			m.Stats.TokensPruned += tokens
			m.Stats.LastActiveTokens = acct.Total
			m.trackPeak(acct.Total)
			return working, acct, nil
		}
	}

	// §14 step 12, the last safe reduction: a single tool result can
	// be larger than the whole budget (a 15k-token file read into a
	// 11k-token budget, measured 2026-09-18), and the recent window's
	// floor keeps it verbatim, so nothing above could touch it. The
	// oversized result is cut to an excerpt — head and tail, with a
	// marker saying what was cut and that the full result is in the
	// session history — rather than failing a run that can continue
	// by reading the file again in ranges.
	if m.PruneToolResults {
		if truncated, n, tokens := m.truncateOversized(working); n > 0 {
			reduced = true
			working = truncated
			m.Stats.ToolResultsTruncated += n
			m.Stats.TokensPruned += tokens
			acct = m.Account(working)
			if acct.WithinBudget() {
				return working, acct, nil
			}
			// Narrowing was tried above while the oversized result
			// was still whole, and could not succeed: that result
			// sits inside the window's floor. Now that it is an
			// excerpt, the older results in the window may be the
			// whole deficit, and pruning them is free.
			if m.KeepRecentTurns > m.minRecent() {
				if narrowed, n, tokens, ok := m.narrowAndPrune(working, &acct); ok {
					working = narrowed
					m.Stats.RecentWindowNarrowings++
					m.Stats.ToolResultsPruned += n
					m.Stats.TokensPruned += tokens
					return working, acct, nil
				}
			}
		}
	}

	// The free reductions were not enough. If compaction was put off
	// above and the deficit is now one the span can cover, it is time.
	if compactionDeferred && m.compactionCanPay(working, acct) {
		if tryCompact() {
			return working, acct, nil
		}
	}

	return nil, acct, m.cannotFit(acct)
}

// truncatedMarker is the note left in the middle of a cut tool result.
const truncatedMarker = "[... tool result truncated from ~%d tokens to fit the active context; " +
	"the full result is in the session history — read it again in smaller ranges ...]"

// oversizedCap is the largest a single tool result may be in the
// active context before the last-resort truncation applies: a quarter
// of the active budget, never below 256 tokens.
func (m *Manager) oversizedCap() int {
	cap := m.Budget.Active() / 4
	if cap < 256 {
		cap = 256
	}
	return cap
}

// truncateOversized cuts every non-pinned tool result larger than the
// cap to a head-and-tail excerpt. Deterministic, no inference. Returns
// the new list, the number of results cut and the tokens saved.
func (m *Manager) truncateOversized(messages []model.Message) ([]model.Message, int, int) {
	cap := m.oversizedCap()
	prios := m.Classify(messages)
	var out []model.Message
	count, saved := 0, 0
	for i, msg := range messages {
		n := MessageTokens(msg)
		if prios[i] == Pinned || msg.Role != "tool" || n <= cap ||
			isPruned(msg.Content) || isTruncated(msg.Content) {
			if out != nil {
				out = append(out, msg)
			}
			continue
		}
		if out == nil {
			out = make([]model.Message, 0, len(messages))
			out = append(out, messages[:i]...)
		}
		replacement := msg
		replacement.Content = excerpt(msg.Content, cap, contextpkg.Estimate(msg.Content))
		out = append(out, replacement)
		count++
		saved += n - MessageTokens(replacement)
	}
	if out == nil {
		return messages, 0, 0
	}
	return out, count, saved
}

// excerpt keeps the first ~60% and the last ~30% of what the cap
// allows, in characters (four per estimated token), around the marker.
func excerpt(content string, capTokens, originalTokens int) string {
	budgetChars := capTokens * 4
	marker := fmt.Sprintf(truncatedMarker, originalTokens)
	budgetChars -= len(marker) + 2
	if budgetChars < 200 {
		budgetChars = 200
	}
	head := budgetChars * 6 / 10
	tail := budgetChars * 3 / 10
	if head+tail >= len(content) {
		return content
	}
	return content[:head] + "\n" + marker + "\n" + content[len(content)-tail:]
}

func isTruncated(content string) bool {
	return strings.Contains(content, "[... tool result truncated from ~")
}

// minRecent is the floor the recent window will not go below.
func (m *Manager) minRecent() int {
	if m.MinRecentTurns > 0 {
		return m.MinRecentTurns
	}
	return DefaultMinRecentTurns
}

// narrowAndPrune shrinks the verbatim window step by step, pruning at
// each step, and stops at the first width that fits. It restores the
// configured width before returning either way: the narrowing is a
// decision about this inference, not a change to the policy.
func (m *Manager) narrowAndPrune(messages []model.Message, acct *Accounting,
) ([]model.Message, int, int, bool) {
	original := m.KeepRecentTurns
	defer func() { m.KeepRecentTurns = original }()

	totalPruned, totalTokens := 0, 0
	floor := m.minRecent()
	// Halve the window down to the floor, and try the floor itself:
	// halving past it used to skip it (6 -> 3 -> 1, floor 2), so a
	// run that fitted at the floor failed "will not narrow below 2".
	for width := original / 2; width >= floor; {
		m.KeepRecentTurns = width
		pruned, n, tokens := m.pruneToolResults(messages)
		totalPruned += n
		totalTokens += tokens
		if n > 0 {
			messages = pruned
		}
		got := m.Account(messages)
		if got.WithinBudget() {
			*acct = got
			return messages, totalPruned, totalTokens, true
		}
		if width == floor {
			break
		}
		if width/2 < floor {
			width = floor
		} else {
			width /= 2
		}
	}
	return messages, totalPruned, totalTokens, false
}

func (m *Manager) trackPeak(total int) {
	if total > m.Stats.PeakActiveTokens {
		m.Stats.PeakActiveTokens = total
	}
}

// cannotFit builds §21's diagnostic: what is left, and which of the
// allowed reductions were unavailable or did not help.
func (m *Manager) cannotFit(a Accounting) error {
	var why []string
	if !m.PruneToolResults {
		why = append(why, "tool-result pruning is disabled")
	} else if a.Reducible == 0 {
		why = append(why, "there is no reducible history left to prune")
	}
	if m.Compactor == nil {
		why = append(why, "no compactor is configured")
	} else if m.Stats.CompactionFailures > 0 {
		why = append(why, "compaction failed")
	}
	if a.Recent > a.Budget/2 {
		why = append(why, fmt.Sprintf("the %d most recent messages held verbatim "+
			"are %d tokens, and the window will not narrow below %d",
			m.KeepRecentTurns, a.Recent, m.minRecent()))
	}
	if len(why) == 0 {
		why = append(why, "every allowed reduction has been applied")
	}
	return fmt.Errorf("ctxlife: cannot fit %d tokens into the %d-token active "+
		"budget (pinned %d, recent %d, reducible %d, tool schemas %d): %s",
		a.Total, a.Budget, a.Pinned, a.Recent, a.Reducible, a.ToolSchemas,
		strings.Join(why, "; "))
}

// -- §9 tool-result pruning ---------------------------------------

// pruneToolResults replaces reducible tool results with placeholders.
//
// Deterministic, and no inference: it walks oldest-first and stops as
// soon as the budget is met, so a run that needs one result pruned
// does not lose ten. Already-pruned results are skipped rather than
// re-counted.
func (m *Manager) pruneToolResults(messages []model.Message) ([]model.Message, int, int) {
	prios := m.Classify(messages)
	target := m.Budget.Active()
	current := m.Account(messages).Total

	var out []model.Message
	count, saved := 0, 0
	for i, msg := range messages {
		if current <= target || prios[i] != Reducible || msg.Role != "tool" ||
			isPruned(msg.Content) {
			if out != nil {
				out = append(out, msg)
			}
			continue
		}
		if out == nil {
			out = make([]model.Message, 0, len(messages))
			out = append(out, messages[:i]...)
		}
		before := MessageTokens(msg)
		replacement := msg
		replacement.Content = fmt.Sprintf(prunedTemplate, contextpkg.Estimate(msg.Content))
		after := MessageTokens(replacement)
		out = append(out, replacement)
		count++
		saved += before - after
		current -= before - after
	}
	if out == nil {
		return messages, 0, 0
	}
	return out, count, saved
}

func isPruned(content string) bool {
	return strings.HasPrefix(content, "[Earlier tool result pruned")
}

// -- §10 conversation compaction ----------------------------------

// reducibleTokens is the size of the span compaction would replace:
// the most it could free, were the summary empty.
func (m *Manager) reducibleTokens(messages []model.Message) int {
	total := 0
	for i, prio := range m.Classify(messages) {
		if prio == Reducible {
			total += MessageTokens(messages[i])
		}
	}
	return total
}

// compactionCanPay reports whether replacing the reducible span with a
// summary could bring the context within budget: the span must cover
// the deficit and still leave room for the summary itself.
func (m *Manager) compactionCanPay(messages []model.Message, acct Accounting) bool {
	room := m.MinSummaryRoom
	if room <= 0 {
		room = DefaultMinSummaryRoom
	}
	return m.reducibleTokens(messages)-(acct.Total-acct.Budget) >= room
}

// compact replaces the reducible span with one summary message.
//
// The summary is a system message placed where the span was, so the
// order of the conversation is preserved: pinned instructions, then
// what happened earlier in compact form, then recent turns verbatim,
// then the current task. §10's sketch, in message form.
func (m *Manager) compact(messages []model.Message) ([]model.Message, int, int, error) {
	prios := m.Classify(messages)
	var span []model.Message
	var spanStart = -1
	for i, msg := range messages {
		if prios[i] == Reducible {
			if spanStart < 0 {
				spanStart = i
			}
			span = append(span, msg)
		}
	}
	if len(span) == 0 {
		return messages, 0, 0, nil
	}

	summary, err := m.Compactor.Compact(span)
	if err != nil {
		return messages, 0, 0, fmt.Errorf("ctxlife: compaction failed: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return messages, 0, 0, fmt.Errorf("ctxlife: the compactor returned nothing")
	}

	before := 0
	for _, msg := range span {
		before += MessageTokens(msg)
	}
	// A user-role message, not a system one: it sits after the
	// task, and a system message that is not at the beginning is
	// rejected by runtimes that apply the model's chat template
	// (FreeToken: HTTP 400). Classify pins it by its header.
	replacement := model.Message{Role: "user",
		Content: compactedHeader + "\n" + strings.TrimSpace(summary)}
	after := MessageTokens(replacement)
	if after >= before {
		// A summary longer than what it summarises is not a
		// reduction. Refusing it is cheaper than discovering it
		// in the remeasurement.
		return messages, 0, 0, fmt.Errorf(
			"ctxlife: the compacted summary (%d tokens) is not smaller than the "+
				"%d tokens it replaces", after, before)
	}

	out := make([]model.Message, 0, len(messages)-len(span)+1)
	placed := false
	for i, msg := range messages {
		if prios[i] == Reducible {
			if !placed {
				out = append(out, replacement)
				placed = true
			}
			continue
		}
		_ = spanStart
		out = append(out, msg)
	}
	return out, len(span), before - after, nil
}

// IsCompacted reports whether a message is one of this manager's
// summaries, which a caller needs in order to avoid compacting a
// summary of a summary.
func IsCompacted(msg model.Message) bool {
	return strings.HasPrefix(msg.Content, compactedHeader)
}

// ToolSchemaTokens estimates what an advertised tool surface costs on
// the wire. The loop builds that surface and the message list does
// not carry it, so §4's "complete context" accounting needs this
// figure to come from the caller.
func ToolSchemaTokens(defs []model.ToolDefinition) int {
	n := 0
	for _, def := range defs {
		n += contextpkg.Estimate(def.Type)
		n += contextpkg.Estimate(def.Function.Name)
		n += contextpkg.Estimate(def.Function.Description)
		n += contextpkg.Estimate(string(def.Function.Parameters))
		n += 8 // the JSON scaffolding around each definition
	}
	return n
}
