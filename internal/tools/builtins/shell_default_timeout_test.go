package builtins

import (
	"context"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// A call without timeout_ms inherits DefaultTimeout: a helper backgrounded
// from the shell would otherwise hold the pipe open forever (measured
// 3 h 24 min on 2026-09-02).
func TestShell_DefaultTimeoutAppliesWhenCallerOmitsTimeoutMs(t *testing.T) {
	prev := DefaultTimeout
	DefaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { DefaultTimeout = prev })

	start := time.Now()
	res, err := Shell{}.Execute(context.Background(), tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "sleep 5 & sleep 5"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "timeout" {
		t.Fatalf("TerminationReason = %q, want timeout", sr.TerminationReason)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("call took %v; the default deadline did not fire", elapsed)
	}
}

// An explicit timeout_ms still wins over the default, in both directions.
func TestShell_ExplicitTimeoutMsOverridesDefault(t *testing.T) {
	prev := DefaultTimeout
	DefaultTimeout = 50 * time.Millisecond
	t.Cleanup(func() { DefaultTimeout = prev })

	res, err := Shell{}.Execute(context.Background(), tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "sleep 0.3; echo done", "timeout_ms": 5000},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	sr := res.Content.(ShellResult)
	if sr.TerminationReason != "" || sr.ExitCode != 0 {
		t.Fatalf("explicit timeout_ms lost to the default: reason=%q exit=%d", sr.TerminationReason, sr.ExitCode)
	}
}

// DefaultTimeout zero keeps the historical behaviour: no deadline.
func TestShell_ZeroDefaultMeansNoDeadline(t *testing.T) {
	prev := DefaultTimeout
	DefaultTimeout = 0
	t.Cleanup(func() { DefaultTimeout = prev })

	res, err := Shell{}.Execute(context.Background(), tools.Call{
		Name:      "shell",
		Arguments: map[string]any{"command": "sleep 0.2; echo ok"},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	sr := res.Content.(ShellResult)
	if sr.TerminationReason != "" {
		t.Fatalf("no deadline expected, got reason %q", sr.TerminationReason)
	}
}
