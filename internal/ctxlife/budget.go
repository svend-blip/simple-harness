// Package ctxlife is the Bounded Context Lifecycle: it decides what
// active context is sent to the model for the next inference, so
// that a long-running session stays inside a safe budget instead of
// growing until the runtime refuses it.
//
// The addendum's central distinction is between durable execution
// history and active model context. Nothing here deletes anything.
// The manager takes the full candidate message list, decides what a
// bounded working set of it is, and returns that — the caller's own
// history, and whatever session persistence it has, are untouched.
//
// The package sits between the loop and the model client and owns
// only that decision:
//
//	RunAgent -> ctxlife.Manager -> model client
//
// It is stdlib-only apart from the harness's own model and context
// packages, both of which are themselves stdlib-only. Accounting and
// reporting go through the existing internal/context Ledger rather
// than a second set of counters, per the addendum's instruction to
// extend the observability that exists.
package ctxlife

import "fmt"

// Budget is the addendum's §6 derivation: what of the model's
// context limit may safely be spent on input.
//
//	model context limit
//	- generation reserve
//	- operational safety reserve
//	= maximum active input budget
//
// The reserves exist because a limit is a limit on the whole
// exchange. A harness that spent all of it on input would leave the
// model no room to answer, and would discover that as a truncated
// reply rather than as a budget error.
type Budget struct {
	// ModelLimit is the effective context limit in tokens. Zero
	// means unknown, which is a state the manager refuses to
	// bound against rather than guessing at.
	ModelLimit int
	// GenerationReserve is room kept for the model's own output.
	GenerationReserve int
	// SafetyReserve absorbs the difference between this
	// harness's estimate and the runtime's real tokenizer.
	// Estimation is approximate by design; this is the margin
	// that makes an approximate estimate safe to act on.
	SafetyReserve int
}

// Default reserve policy. These are the addendum's §6 example
// figures, and they are deliberately generous: the addendum's
// instruction is that estimation "MUST prefer safety over
// maximizing theoretical context utilization", and a harness that
// gets this wrong fails a long run near its end, having done the
// work.
const (
	DefaultGenerationReserve = 16384
	DefaultSafetyReserve     = 4096

	// Below this, the fixed reserves would consume most of the
	// window, so they are scaled down proportionally instead.
	// Without this a 8k-context model would be left 0 tokens of
	// input budget and every run would fail at the first turn.
	smallModelLimit = 65536
)

// DefaultBudget derives a budget from a model context limit.
//
// For ordinary limits the reserves are the constants above. For a
// small window they are scaled to a quarter and a sixteenth of the
// limit respectively, which keeps the same shape — most of the
// window for input, a substantial slice for output, a small margin
// for estimation error — without the fixed figures swallowing it.
func DefaultBudget(modelLimit int) Budget {
	if modelLimit <= 0 {
		return Budget{}
	}
	gen, safety := DefaultGenerationReserve, DefaultSafetyReserve
	if modelLimit < smallModelLimit {
		gen = modelLimit / 4
		safety = modelLimit / 16
	}
	return Budget{ModelLimit: modelLimit, GenerationReserve: gen, SafetyReserve: safety}
}

// Active is the number of tokens that may be spent on input.
// Returns 0 when the limit is unknown or the reserves exceed it;
// callers treat 0 as "not bounded here" and must say so rather than
// silently sending an unbounded request.
func (b Budget) Active() int {
	if b.ModelLimit <= 0 {
		return 0
	}
	active := b.ModelLimit - b.GenerationReserve - b.SafetyReserve
	if active < 0 {
		return 0
	}
	return active
}

// Known reports whether this budget can bound anything.
func (b Budget) Known() bool { return b.Active() > 0 }

// Validate reports why a budget cannot be used, if it cannot.
func (b Budget) Validate() error {
	if b.ModelLimit <= 0 {
		return fmt.Errorf("ctxlife: model context limit is unknown; set one " +
			"in configuration or through --limit so the context can be bounded")
	}
	if b.GenerationReserve < 0 || b.SafetyReserve < 0 {
		return fmt.Errorf("ctxlife: reserves must not be negative "+
			"(generation %d, safety %d)", b.GenerationReserve, b.SafetyReserve)
	}
	if b.Active() <= 0 {
		return fmt.Errorf("ctxlife: the reserves (generation %d + safety %d) "+
			"consume the whole %d-token limit, leaving no room for input",
			b.GenerationReserve, b.SafetyReserve, b.ModelLimit)
	}
	return nil
}

// String renders the budget the way §16 asks for it.
func (b Budget) String() string {
	if !b.Known() {
		return "context limit unknown; context is not bounded"
	}
	return fmt.Sprintf("limit %d - generation %d - safety %d = %d active",
		b.ModelLimit, b.GenerationReserve, b.SafetyReserve, b.Active())
}
