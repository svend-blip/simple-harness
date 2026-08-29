package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureContext is the test helper for handoff 036's
// TestContextShow_* suite. It mirrors captureSessions /
// captureSkill: saves+restores os.Stdout / os.Stderr, redirects
// them to pipes, runs run(args), drains the pipes into buffers,
// and returns the run() exit code + the captured stdout + the
// captured stderr.
//
// The helper is PRIVATE to context_test.go (file-scoped helper,
// not exported). main_test.go and the other test files stay
// FROZEN — this helper is the local equivalent of the
// runCapture pattern.
func captureContext(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW

	code = run(args)

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return code, outBuf.String(), errBuf.String()
}

// TestContextShow_Help: `run([]string{"context", "show",
// "--help"})` returns 0 AND stdout contains the substring
// "context show". (Help text pin.)
func TestContextShow_Help(t *testing.T) {
	code, stdout, _ := captureContext(t, []string{"context", "show", "--help"})
	if code != 0 {
		t.Fatalf("run(context show --help) returned %d, want 0 (stdout=%q)", code, stdout)
	}
	if !strings.Contains(stdout, "context show") {
		t.Fatalf("run(context show --help) stdout missing %q; got %q", "context show", stdout)
	}
}

// TestContextShow_UnknownVerb: dispatcher's verb-rejection path.
// run([]string{"context", "show", "extra"}) with "extra" treated
// as a positional argument returns 1 AND stderr contains "context
// show" (the usage line includes the surface name).
func TestContextShow_UnknownVerb(t *testing.T) {
	code, _, stderr := captureContext(t, []string{"context", "show", "extra"})
	if code == 0 {
		t.Fatalf("run(context show extra) returned 0, want non-zero (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, "context show") {
		t.Fatalf("run(context show extra) stderr missing %q; got %q", "context show", stderr)
	}
}

// TestContextShow_RequiresBaseURL: --base-url empty → exit 2 AND
// stderr contains "--base-url is required". Verifies the SCOPE
// §28 config-error mapping.
func TestContextShow_RequiresBaseURL(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "",
		"--model", "m",
		"--prompt-file", promptPath,
	})
	if code != 2 {
		t.Fatalf("run(context show --base-url empty) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--base-url is required") {
		t.Fatalf("run(context show --base-url empty) stderr missing %q; got %q", "--base-url is required", stderr)
	}
}

// TestContextShow_RequiresModel: --model empty → exit 2 AND
// stderr contains "--model is required". Verifies the SCOPE §28
// config-error mapping.
func TestContextShow_RequiresModel(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "",
		"--prompt-file", promptPath,
	})
	if code != 2 {
		t.Fatalf("run(context show --model empty) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--model is required") {
		t.Fatalf("run(context show --model empty) stderr missing %q; got %q", "--model is required", stderr)
	}
}

// TestContextShow_RequiresPromptFile: --prompt-file missing →
// exit 2 AND stderr contains "--prompt-file is required".
// Verifies the SCOPE §28 config-error mapping.
func TestContextShow_RequiresPromptFile(t *testing.T) {
	code, _, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
	})
	if code != 2 {
		t.Fatalf("run(context show --prompt-file missing) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--prompt-file is required") {
		t.Fatalf("run(context show --prompt-file missing) stderr missing %q; got %q", "--prompt-file is required", stderr)
	}
}

// TestContextShow_EmptyConfig_PrintsTotalAndTask_TG1 is THE
// BINDING PIN for GOAL §6 TG1. The test drives `run([]string{
// "context", "show", ...})` with the minimum flag set
// (--base-url http://127.0.0.1:9 --model m --workspace /tmp
// --permission read_only --prompt-file promptPath) where
// promptPath contains "hi". The assertions: exit code 0 AND
// stdout contains "Total" (case-insensitive), "task"
// (case-insensitive), AND "tool schemas" (case-insensitive — the
// category label for ToolSchemas; per SCOPE §19 the report lists
// at least the categories even when zero entries exist, so the
// rendered output includes a "tool schemas:" zero-tokens line).
func TestContextShow_EmptyConfig_PrintsTotalAndTask_TG1(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context show empty-config) returned %d, want 0 (stderr=%q)", code, stderr)
	}

	lc := strings.ToLower(stdout)
	for _, want := range []string{"total", "task", "tool schemas"} {
		if !strings.Contains(lc, want) {
			t.Fatalf("run(context show empty-config) stdout missing %q (case-insensitive); got %q", want, stdout)
		}
	}
}

// TestContextShow_FullConfig_PrintsAllCategories: drive
// runContextShow with all populated slots
// (--base-url http://127.0.0.1:9 --model m --workspace /tmp
// --permission read_only --system "governance-text" --skill
// cold-start --prompt-file promptPath) where cold-start is a
// fixture SKILL.md at
// <workspace>/.simple-harness/skills/cold-start/SKILL.md. The
// assertions: exit code 0 AND stdout contains "harness system",
// "governance", "skill", "task", "cold-start".
func TestContextShow_FullConfig_PrintsAllCategories(t *testing.T) {
	workspace := t.TempDir()
	writeSkillFixture(t, workspace, "cold-start", "COLD-START-FULL-PIN-cc11\n")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
		"--workspace", workspace,
		"--permission", "read_only",
		"--system", "governance-text",
		"--skill", "cold-start",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context show full-config) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}

	for _, want := range []string{"harness system", "governance", "skill", "task", "cold-start"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run(context show full-config) stdout missing %q; got %q", want, stdout)
		}
	}
}

// TestContextShow_SkillNotFound_Exit2: --skill <unknown> → exit
// 2 AND stderr contains "unknown skill". Verifies the SCOPE §15
// config-error mapping.
func TestContextShow_SkillNotFound_Exit2(t *testing.T) {
	workspace := t.TempDir()
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, _, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
		"--workspace", workspace,
		"--permission", "read_only",
		"--prompt-file", promptPath,
		"--skill", "bogus",
	})
	if code != 2 {
		t.Fatalf("run(context show --skill bogus) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "unknown skill") {
		t.Fatalf("run(context show --skill bogus) stderr missing %q; got %q", "unknown skill", stderr)
	}
}

// TestContextShow_NoModelCall_UnreachableBaseURL: the
// determinism handle. Drive runContextShow with --base-url
// http://127.0.0.1:9 (port 9 is reserved/discard; a connection
// attempt would fail with a network error). Assertions: exit
// code 0 AND stdout contains "Total" AND NO stderr line contains
// "connection refused", "dial", or "no such host" (the model
// client was constructed but NEVER invoked; a future regression
// that adds a r.RunOne(...) call to runContextShow would fail
// because the model client would attempt the connection and
// the stderr would carry the network error).
func TestContextShow_NoModelCall_UnreachableBaseURL(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context show unreachable-base-url) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "total") {
		t.Fatalf("run(context show unreachable-base-url) stdout missing %q; got %q", "total", stdout)
	}
	for _, bad := range []string{"connection refused", "dial", "no such host"} {
		if strings.Contains(strings.ToLower(stderr), bad) {
			t.Fatalf("run(context show unreachable-base-url) stderr contains %q (model client appears to have been invoked); stderr=%q", bad, stderr)
		}
	}
}

// TestContextShow_ReadsSystemFile_IntoGovernanceEntry: drive
// runContextShow with --system-file pointing at a 100-character
// file. The report must include a line with label
// "governance: external" (the per-entry label per handoff 035's
// wiring: r.ledger.Add(contextpkg.ExternalSystem, "external",
// r.cfg.SystemExternal)) AND the Total token count must include
// the system file's contribution ((100+3)/4 = 25 tokens minimum).
//
// The exact byte-pinning of the system file's content is not in
// the report (Report() renders the per-entry label + token
// count, NOT the content). The verification lens is the
// "governance: external" label appearance + the Total token
// count being >= 25 (the system file's contribution).
func TestContextShow_ReadsSystemFile_IntoGovernanceEntry(t *testing.T) {
	// 100-character system file: "GOVERNANCE-MARKER-3d82\n" (23
	// chars) padded with a marker prefix to reach exactly 100
	// characters (Estimate("...100 chars") = (100+3)/4 = 25).
	sysContent := "GOVERNANCE-MARKER-3d82-" + strings.Repeat("X", 77)
	if len(sysContent) != 100 {
		t.Fatalf("sysContent length = %d, want 100", len(sysContent))
	}

	tmp := t.TempDir()
	sysFile := filepath.Join(tmp, "sys.md")
	if err := os.WriteFile(sysFile, []byte(sysContent), 0o644); err != nil {
		t.Fatalf("write sys file: %v", err)
	}
	promptPath := filepath.Join(tmp, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--system-file", sysFile,
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context show --system-file) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "governance: external") {
		t.Fatalf("run(context show --system-file) stdout missing %q (the ExternalSystem label); got %q", "governance: external", stdout)
	}
	// Verify the Total line carries the system file's token
	// contribution. Parse the line "Total   N tokens" and
	// assert N >= 25 (the system file's Estimate = 25; the
	// HarnessSystem + Task entries also contribute tokens,
	// so the Total must be at least 25).
	var totalTokens int
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Total ") {
			continue
		}
		// Format: "Total   N tokens"
		parts := strings.Fields(trimmed)
		if len(parts) < 3 {
			continue
		}
		n, err := fmt.Sscanf(parts[1], "%d", &totalTokens)
		if err != nil || n != 1 {
			continue
		}
		// parts[1] is the int; reset and capture
		totalTokens = 0
		_, err = fmt.Sscanf(parts[1], "%d", &totalTokens)
		if err != nil {
			continue
		}
		break
	}
	if totalTokens < 25 {
		t.Fatalf("run(context show --system-file) Total tokens = %d, want >= 25 (system file's contribution); stdout=%q", totalTokens, stdout)
	}
}
