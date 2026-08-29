// Package context is the canonical SCOPE §18 context-tracking
// surface for Simple Harness. It owns the data structure that the
// loop populates as it composes a message list and that the cmd-side
// `context show` / `context doctor` commands (Run 010 / handoff 036
// + 037) consume to render the SCOPE §19 accounting report and the
// SCOPE §20 diagnostics.
//
// The package is intentionally small and stdlib-only. It is the
// single source of truth for context-tracking state in V1; the
// loop's Run.Ledger() accessor hands out a *Ledger and the cmd-side
// commands render the snapshot. The package's four-callable-method
// contract:
//
//   - Add(name, content) appends an Entry to the Ledger with the
//     canonical Estimate(content) token count. Empty content is
//     skipped (the loop's ComposeMessages skip semantics carry).
//   - Total returns the sum of all TokenEstimate values across the
//     Ledger's Entries.
//   - Report returns the SCOPE §19 accounting report (multi-line
//     string with one line per entry, a separator, a Total line,
//     and a Context limit line if Limit > 0).
//   - Doctor returns the SCOPE §20 diagnostics — large contributors
//     (entries whose TokenEstimate exceeds the 1000-token threshold),
//     duplicates (entries whose Content appears more than once across
//     the Ledger), and tool-schema overhead (ToolSchemas entries
//     whose TokenEstimate exceeds 500).
//
// The seven Category constants map verbatim to SCOPE §18's seven
// categories. V1 populates HarnessSystem + ExternalSystem + Skill(s)
// + Task; ToolSchemas / Conversation / ToolResults are tracked as
// categories but currently have zero entries (the loop does not yet
// dispatch tools or maintain multi-turn conversation history).
//
// Estimates are honest: where exact tokenization is unavailable
// (SCOPE §20 standing constraint), the Estimate function is clearly
// identified as approximate (±25% tolerance vs exact tokenizers for
// English text). Future Runs that wire tool dispatch / multi-turn
// conversation history / exact tokenization will extend this
// package's surface; the contracts here are stable.
package context

import (
	"fmt"
	"sort"
	"strings"
)

// Category is the canonical SCOPE §18 enumeration of context
// categories tracked by the Ledger. The seven constants correspond
// to SCOPE §19's sketch categories verbatim.
type Category string

const (
	// HarnessSystem is the minimal harness instructions
	// (loop.HarnessSystem). One entry per RunOne call.
	HarnessSystem Category = "harness system"
	// ExternalSystem is the external governance / system
	// instructions from --system or --system-file. One entry
	// per RunOne call when non-empty.
	ExternalSystem Category = "governance"
	// Skill is a single loaded skill. One entry per non-empty
	// skill, in composition order.
	Skill Category = "skill"
	// Task is the user prompt for the current RunOne call. One
	// entry per RunOne call.
	Task Category = "task"
	// Conversation is the assistant-side message history
	// (multi-turn conversation). V1: zero entries — multi-turn
	// is not yet wired.
	Conversation Category = "conversation"
	// ToolSchemas is the tool schema surface sent to the
	// model. V1: zero entries — tool dispatch is not yet wired
	// into RunOne.
	ToolSchemas Category = "tool schemas"
	// ToolResults is the tool result surface returned to the
	// model. V1: zero entries — tool dispatch is not yet wired
	// into RunOne.
	ToolResults Category = "tool results"
)

// Entry is one category's worth of content tracked by the Ledger.
// Name is the human-readable label (e.g. "cold-start" for a skill,
// "harness" for the harness system, "external" for an external
// system file). Content is the raw text. TokenEstimate is the
// result of Estimate(Content) UNLESS the caller supplied a non-zero
// value via AddWithTokens.
type Entry struct {
	Category      Category
	Name          string
	Content       string
	TokenEstimate int
}

// Finding is one doctor diagnostic. Findings surface the major
// context contributors (large entries), duplicates (same Content
// appearing more than once), and tool-schema overhead. Category
// and Name are zero-valued for duplicate findings (a duplicate may
// span categories).
type Finding struct {
	Category Category
	Name     string
	Type     string // "large" | "duplicate" | "schema"
	Detail   string
}

// Ledger is the canonical SCOPE §18 context-tracking surface. The
// loop's Run.Ledger() accessor hands out a *Ledger; the cmd-side
// commands render the snapshot. Entries is appended to as
// RunOne composes the message list. Limit is the configured context
// limit (0 means unknown — populated by the --limit <n> flag in
// handoff 036).
type Ledger struct {
	Entries []Entry
	Limit   int
}

// Estimate returns a rough token estimate for the given text. The
// estimator uses a simple heuristic: 1 token per 4 characters of
// text, rounding UP (i.e. (len(text) + 3) / 4).
//
// Approximate; ±25% tolerance vs exact tokenizers for English text.
// Where exact tokenization is not available, clearly identified
// estimates are acceptable (SCOPE §20 standing constraint). The
// function is intentionally simple — it does NOT use a real
// tokenizer library; future Runs that wire exact tokenization will
// extend this surface (likely by adding a WithTokens field or a
// Tokenizer interface).
//
// Deterministic: same input → same output. Empty string → 0.
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// Add appends an Entry to the Ledger with TokenEstimate =
// Estimate(Content). Empty Content is skipped (matches
// loop.ComposeMessages skip semantics — a zero-byte skill body
// contributes neither a message nor a ledger entry).
func (l *Ledger) Add(category Category, name, content string) {
	if content == "" {
		return
	}
	l.Entries = append(l.Entries, Entry{
		Category:      category,
		Name:          name,
		Content:       content,
		TokenEstimate: Estimate(content),
	})
}

// AddWithTokens appends an Entry to the Ledger with the
// caller-supplied tokens value. Used when the caller knows the
// token count from a more accurate source (e.g. a model client
// that reports usage). Empty Content is still skipped.
func (l *Ledger) AddWithTokens(category Category, name, content string, tokens int) {
	if content == "" {
		return
	}
	l.Entries = append(l.Entries, Entry{
		Category:      category,
		Name:          name,
		Content:       content,
		TokenEstimate: tokens,
	})
}

// Total returns the sum of all TokenEstimate values across the
// Ledger's Entries. Returns 0 for an empty ledger.
func (l *Ledger) Total() int {
	total := 0
	for _, e := range l.Entries {
		total += e.TokenEstimate
	}
	return total
}

// ByCategory returns the totals per Category as a map. Categories
// with zero entries are absent from the map. Returns an empty map
// for an empty ledger.
func (l *Ledger) ByCategory() map[Category]int {
	by := make(map[Category]int)
	for _, e := range l.Entries {
		by[e.Category] += e.TokenEstimate
	}
	return by
}

// Overflow returns a non-nil error if Limit > 0 AND Total() >
// Limit. Per SCOPE §18: "fail predictably if the request cannot
// fit rather than silently corrupting the conversation." Returns
// nil when Limit is 0 (unknown) or when the total fits within the
// configured limit.
func (l *Ledger) Overflow() error {
	if l.Limit <= 0 {
		return nil
	}
	total := l.Total()
	if total > l.Limit {
		return fmt.Errorf("context overflow: total %d tokens exceeds configured limit %d", total, l.Limit)
	}
	return nil
}

// Report returns the SCOPE §19 accounting report as a multi-line
// string. One line per entry (sorted by Category then Name), a
// "-----------------------------------------" separator line, a
// "Total" line, and a "Context limit" line if Limit > 0. Each
// entry line format: "%-30s %6d tokens" where the first column is
// "<Category>: <Name>" padded to 30 chars.
//
// The exact width (30) and column alignment are binding for
// handoff 036's `context show` output (the GOAL §6 TG1 grep is
// "Total", "task", "tool schemas" — case-insensitive — so the
// formatted output must include those substrings).
func (l *Ledger) Report() string {
	entries := make([]Entry, len(l.Entries))
	copy(entries, l.Entries)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Category != entries[j].Category {
			return entries[i].Category < entries[j].Category
		}
		return entries[i].Name < entries[j].Name
	})

	var b strings.Builder
	for _, e := range entries {
		label := fmt.Sprintf("%s: %s", e.Category, e.Name)
		fmt.Fprintf(&b, "%-30s %6d tokens\n", label, e.TokenEstimate)
	}
	b.WriteString("-----------------------------------------\n")
	fmt.Fprintf(&b, "%-30s %6d tokens\n", "Total", l.Total())
	if l.Limit > 0 {
		fmt.Fprintf(&b, "Context limit: %d tokens\n", l.Limit)
	}
	return b.String()
}

// Doctor returns SCOPE §20 diagnostics as a slice of Finding.
// Three diagnostic categories:
//
//   - large: any entry whose TokenEstimate exceeds 1000 tokens.
//     The finding's Category and Name identify the contributor.
//   - duplicate: any unique Content that appears more than once
//     across the Ledger's Entries. One finding per unique
//     duplicate Content. Category and Name are zero-valued.
//   - schema: if any ToolSchemas entry has TokenEstimate > 500,
//     one finding with Type "schema" surfaces the overhead.
//
// Returns nil (not an empty slice) when no findings apply.
func (l *Ledger) Doctor() []Finding {
	var findings []Finding

	for _, e := range l.Entries {
		if e.TokenEstimate > 1000 {
			findings = append(findings, Finding{
				Category: e.Category,
				Name:     e.Name,
				Type:     "large",
				Detail:   fmt.Sprintf("%s: %s contributes %d tokens (threshold 1000)", e.Category, e.Name, e.TokenEstimate),
			})
		}
	}

	counts := make(map[string]int)
	for _, e := range l.Entries {
		counts[e.Content]++
	}
	for content, n := range counts {
		if n > 1 {
			findings = append(findings, Finding{
				Type:   "duplicate",
				Detail: fmt.Sprintf("content appears %d times in the ledger (duplicates detected by exact match)", n),
			})
			_ = content // content retained for future cross-category duplicate attribution
		}
	}

	for _, e := range l.Entries {
		if e.Category == ToolSchemas && e.TokenEstimate > 500 {
			findings = append(findings, Finding{
				Category: e.Category,
				Name:     e.Name,
				Type:     "schema",
				Detail:   fmt.Sprintf("tool schemas consume %d tokens (consider whether all tools are needed)", e.TokenEstimate),
			})
		}
	}

	return findings
}