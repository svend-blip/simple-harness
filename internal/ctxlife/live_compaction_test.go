package ctxlife

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/model"
)

// TestLive_SizedCompaction measures a compaction with and without a size
// target against a real endpoint. It runs only when told where one is:
//
//	SIMPLE_HARNESS_LIVE_BASE_URL=http://localhost:11434 \
//	SIMPLE_HARNESS_LIVE_MODEL=qwen3.6-27b-64k go test -run TestLive_ -v ./internal/ctxlife
//
// The material is this package's own source, as a conversation that read
// it: enough that a summary has something to leave out.
func TestLive_SizedCompaction(t *testing.T) {
	base, name := os.Getenv("SIMPLE_HARNESS_LIVE_BASE_URL"), os.Getenv("SIMPLE_HARNESS_LIVE_MODEL")
	if base == "" || name == "" {
		t.Skip("no live endpoint configured")
	}
	var span []model.Message
	for _, file := range []string{"budget.go", "report.go", "compactor.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		span = append(span,
			model.Message{Role: "assistant", Content: "Reading " + file + " to see what it is responsible for."},
			model.Message{Role: "tool", ToolCallID: file, Content: string(src)},
			model.Message{Role: "assistant", Content: file + " read; noting its role before the next file."})
	}
	spanTokens := 0
	for _, msg := range span {
		spanTokens += MessageTokens(msg)
	}
	const target = 600
	for _, tc := range []struct {
		label     string
		target    int
		reasoning string
	}{{"no target", 0, ""}, {"target 600", target, ""}, {"target 600, reasoning none", target, "none"}} {
		var usage *model.Usage
		c := &ModelCompactor{
			Client: model.NewClient(model.Options{BaseURL: base, Model: name,
				MaxOutputTokens: 8192, RequestTimeout: 10 * time.Minute}),
			Ctx:             context.Background(),
			ReasoningEffort: tc.reasoning,
			OnUsage:         func(u *model.Usage) { usage = u },
		}
		start := time.Now()
		summary, err := c.CompactWithin(span, tc.target)
		took := time.Since(start)
		out := 0
		if usage != nil {
			out = usage.CompletionTokens
		}
		t.Logf("%-27s span %d tokens -> summary %d est. tokens, %d output tokens reported, %.1fs, err=%v",
			tc.label, spanTokens, contextpkg.Estimate(summary), out, took.Seconds(), err)
		if tc.target > 0 {
			if err != nil {
				t.Errorf("sized compaction failed: %v", err)
			}
			if got := contextpkg.Estimate(summary); got > tc.target*SummaryCapFactor {
				t.Errorf("summary of %d tokens exceeds the cap %d", got, tc.target*SummaryCapFactor)
			}
			if !strings.Contains(summary, " ") {
				t.Errorf("summary is not prose: %q", summary)
			}
		}
	}
}
