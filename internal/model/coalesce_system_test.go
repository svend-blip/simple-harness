package model

import "testing"

// Two leading system messages (base prompt + skill) must reach the wire
// as ONE system message: FreeToken's chat template rejects a second one
// with HTTP 400 "System message must be at the beginning" (2026-09-03).
func TestCoalesceLeadingSystem_JoinsHeadRunInOrder(t *testing.T) {
	in := []Message{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "skill"},
		{Role: "user", Content: "hi"},
	}
	out := coalesceLeadingSystem(in)
	if len(out) != 2 {
		t.Fatalf("want 2 messages, got %d: %+v", len(out), out)
	}
	if out[0].Role != "system" || out[0].Content != "base\n\nskill" {
		t.Fatalf("merged system wrong: %+v", out[0])
	}
	if out[1].Role != "user" || out[1].Content != "hi" {
		t.Fatalf("user message altered: %+v", out[1])
	}
}

func TestCoalesceLeadingSystem_SingleOrNoneUnchanged(t *testing.T) {
	one := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	if got := coalesceLeadingSystem(one); len(got) != 2 || got[0].Content != "s" {
		t.Fatalf("single system altered: %+v", got)
	}
	none := []Message{{Role: "user", Content: "u"}}
	if got := coalesceLeadingSystem(none); len(got) != 1 {
		t.Fatalf("no-system altered: %+v", got)
	}
}

func TestCoalesceLeadingSystem_LaterSystemLeftAlone(t *testing.T) {
	in := []Message{
		{Role: "system", Content: "a"},
		{Role: "user", Content: "u"},
		{Role: "system", Content: "late"},
	}
	out := coalesceLeadingSystem(in)
	if len(out) != 3 || out[2].Role != "system" || out[2].Content != "late" {
		t.Fatalf("later system must be untouched: %+v", out)
	}
}

func TestToWireMessages_SendsOneSystemMessage(t *testing.T) {
	wire := toWireMessages([]Message{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "skill"},
		{Role: "user", Content: "hi"},
	})
	if len(wire) != 2 || wire[0].Role != "system" || wire[1].Role != "user" {
		t.Fatalf("wire shape wrong: %+v", wire)
	}
}
