package event

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A "usage" event carries the per-request token counts (2026-09-02).
func TestUsageEventShape(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter(&buf, "sess-1")
	if err := em.Usage(UsageBlock{PromptTokens: 10, CompletionTokens: 20, ReasoningTokens: 15}); err != nil {
		t.Fatalf("Usage: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v (%q)", err, buf.String())
	}
	if ev["event"] != "usage" {
		t.Fatalf("event = %v, want usage", ev["event"])
	}
	u, _ := ev["usage"].(map[string]any)
	if u["reasoning_tokens"] != float64(15) || u["completion_tokens"] != float64(20) {
		t.Fatalf("usage payload = %v", u)
	}
}
