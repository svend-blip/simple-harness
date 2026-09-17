package ctxlife

import (
	"fmt"
	"strings"
)

// Report renders §16's diagnostic surface: what the budget is, where
// the active context is going, and what reduction has done so far.
//
// Every figure is one the manager measured. Nothing here is derived
// from something else that was derived — utilization is the only
// ratio, and it is computed from two numbers printed beside it, so a
// reader can check it.
func (m *Manager) Report(a Accounting) string {
	var b strings.Builder
	w := func(label string, value int) {
		fmt.Fprintf(&b, "%-28s%10d\n", label+":", value)
	}

	if !m.Budget.Known() {
		b.WriteString("Model context limit:            unknown\n")
		b.WriteString("Active input budget:            unbounded\n\n")
		w("Active context", a.Total)
		b.WriteString("\nThe context is not bounded: no model context limit is\n" +
			"configured, so no reduction can be applied. Set one to enable\n" +
			"the bounded context lifecycle.\n")
		return b.String()
	}

	w("Model context limit", m.Budget.ModelLimit)
	w("Generation reserve", m.Budget.GenerationReserve)
	w("Safety reserve", m.Budget.SafetyReserve)
	w("Active input budget", a.Budget)
	b.WriteString("\n")
	w("Pinned", a.Pinned)
	w("Recent verbatim", a.Recent)
	w("Reducible", a.Reducible)
	w("Tool schemas", a.ToolSchemas)
	b.WriteString(strings.Repeat("-", 38) + "\n")
	w("Active context", a.Total)
	fmt.Fprintf(&b, "%-28s%9.1f%%\n", "Budget utilization:", a.Utilization()*100)

	b.WriteString("\n")
	w("Messages", a.Messages)
	w("Of which tool results", a.ToolResults)
	w("Of which conversation", a.Conversation)

	b.WriteString("\n")
	w("Inferences", m.Stats.Inferences)
	w("Reductions", m.Stats.Reductions)
	w("Tool results pruned", m.Stats.ToolResultsPruned)
	w("Tokens pruned", m.Stats.TokensPruned)
	w("Compactions", m.Stats.Compactions)
	w("Tokens compacted", m.Stats.TokensCompacted)
	if m.Stats.CompactionFailures > 0 {
		w("Compaction failures", m.Stats.CompactionFailures)
	}
	w("Peak active context", m.Stats.PeakActiveTokens)
	return b.String()
}

// Summary is the one-line form, for a status surface that cannot
// spend twenty lines on this.
func (m *Manager) Summary(a Accounting) string {
	if !m.Budget.Known() {
		return fmt.Sprintf("context %d tokens (unbounded: no model limit configured)",
			a.Total)
	}
	s := fmt.Sprintf("context %d/%d tokens (%.0f%%)", a.Total, a.Budget,
		a.Utilization()*100)
	var did []string
	if m.Stats.ToolResultsPruned > 0 {
		did = append(did, fmt.Sprintf("%d tool results pruned (~%d tokens)",
			m.Stats.ToolResultsPruned, m.Stats.TokensPruned))
	}
	if m.Stats.Compactions > 0 {
		did = append(did, fmt.Sprintf("%d compactions (~%d tokens)",
			m.Stats.Compactions, m.Stats.TokensCompacted))
	}
	if len(did) > 0 {
		s += "; " + strings.Join(did, ", ")
	}
	return s
}
