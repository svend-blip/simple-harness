package config

import (
	"encoding/json"
	"testing"
)

// The addendum's §17 requirement is that safe behaviour works with no
// configuration. These test the absent cases at least as hard as the
// present ones, because absence is what most deployments will have.

func TestAConfigThatSaysNothingAboutContextIsStillBounded(t *testing.T) {
	var c ContextConfig
	if !c.Bounded() {
		t.Fatal("the zero value turned the lifecycle off")
	}
	if !c.PruningEnabled() || !c.CompactionEnabled() {
		t.Fatal("the zero value turned a reduction off")
	}
}

func TestUnboundedMustBeAskedForExplicitly(t *testing.T) {
	if (ContextConfig{Policy: "unbounded"}).Bounded() {
		t.Fatal("unbounded was not honoured")
	}
	if (ContextConfig{Policy: "UNBOUNDED"}).Bounded() {
		t.Fatal("the policy name is case-sensitive")
	}
	if (ContextConfig{Policy: "  unbounded  "}).Bounded() {
		t.Fatal("surrounding whitespace defeated the policy name")
	}
}

func TestATypoInThePolicyNameLeavesTheSafeBehaviourInPlace(t *testing.T) {
	// A misspelled "unbouded" must not silently remove bounding.
	// Failing safe here costs a user an unbounded run they asked
	// for; failing unsafe costs them the run.
	for _, typo := range []string{"unbouded", "none", "off", "bounded", ""} {
		if !(ContextConfig{Policy: typo}).Bounded() {
			t.Errorf("policy %q disabled the lifecycle", typo)
		}
	}
}

func TestAnExplicitFalseIsToldApartFromAnAbsentKey(t *testing.T) {
	var absent, explicit ContextConfig
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"tool_result_pruning":false,`+
		`"compaction":false}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if !absent.PruningEnabled() || !absent.CompactionEnabled() {
		t.Fatal("an absent key was read as false")
	}
	if explicit.PruningEnabled() || explicit.CompactionEnabled() {
		t.Fatal("an explicit false was ignored")
	}
}

func TestTheContextSectionRoundTrips(t *testing.T) {
	on := true
	in := ContextConfig{Policy: "bounded", ModelLimit: 131072,
		GenerationReserve: 16384, SafetyReserve: 4096, KeepRecentTurns: 8,
		ToolResultPruning: &on}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ContextConfig
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ModelLimit != in.ModelLimit || out.KeepRecentTurns != in.KeepRecentTurns {
		t.Fatalf("round trip lost fields: %+v", out)
	}
}

func TestAConfigFileWithAContextSectionParses(t *testing.T) {
	var c Config
	err := json.Unmarshal([]byte(`{"model":{"model":"qwen"},
		"context":{"policy":"bounded","model_limit":131072}}`), &c)
	if err != nil {
		t.Fatal(err)
	}
	if c.Context.ModelLimit != 131072 {
		t.Fatalf("model limit %d", c.Context.ModelLimit)
	}
	if !c.Context.Bounded() {
		t.Fatal("an explicit bounded policy was not read as bounded")
	}
}

func TestAConfigFileWithoutAContextSectionStillParses(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"model":{"model":"qwen"}}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.Context.Bounded() {
		t.Fatal("a config predating this addendum lost its bounding")
	}
	if c.Context.ModelLimit != 0 {
		t.Fatal("an absent limit was invented")
	}
}
