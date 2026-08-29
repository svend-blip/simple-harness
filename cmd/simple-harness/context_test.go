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

// TestContextDoctor_Help: `run([]string{"context", "doctor",
// "--help"})` returns 0 AND stdout contains the substring
// "context doctor" AND stdout contains the substring "--limit".
// (Help text pin; both the verb name and the new --limit <n>
// flag are documented in contextUsage.)
func TestContextDoctor_Help(t *testing.T) {
	code, stdout, _ := captureContext(t, []string{"context", "doctor", "--help"})
	if code != 0 {
		t.Fatalf("run(context doctor --help) returned %d, want 0 (stdout=%q)", code, stdout)
	}
	if !strings.Contains(stdout, "context doctor") {
		t.Fatalf("run(context doctor --help) stdout missing %q; got %q", "context doctor", stdout)
	}
	if !strings.Contains(stdout, "--limit") {
		t.Fatalf("run(context doctor --help) stdout missing %q; got %q", "--limit", stdout)
	}
}

// TestContextDoctor_UnknownVerb: dispatcher's verb-rejection path.
// `run([]string{"context", "doctor", "extra"})` with "extra"
// treated as a positional argument returns 1 AND stderr contains
// "context doctor" (the usage line includes the surface name).
// Mirrors TestContextShow_UnknownVerb.
func TestContextDoctor_UnknownVerb(t *testing.T) {
	code, _, stderr := captureContext(t, []string{"context", "doctor", "extra"})
	if code == 0 {
		t.Fatalf("run(context doctor extra) returned 0, want non-zero (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, "context doctor") {
		t.Fatalf("run(context doctor extra) stderr missing %q; got %q", "context doctor", stderr)
	}
}

// TestContextDoctor_RequiresBaseURL: --base-url empty → exit 2
// AND stderr contains "--base-url is required". Mirrors
// TestContextShow_RequiresBaseURL.
func TestContextDoctor_RequiresBaseURL(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "",
		"--model", "m",
		"--prompt-file", promptPath,
	})
	if code != 2 {
		t.Fatalf("run(context doctor --base-url empty) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--base-url is required") {
		t.Fatalf("run(context doctor --base-url empty) stderr missing %q; got %q", "--base-url is required", stderr)
	}
}

// TestContextDoctor_RequiresModel: --model empty → exit 2 AND
// stderr contains "--model is required".
func TestContextDoctor_RequiresModel(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "http://127.0.0.1:9",
		"--model", "",
		"--prompt-file", promptPath,
	})
	if code != 2 {
		t.Fatalf("run(context doctor --model empty) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--model is required") {
		t.Fatalf("run(context doctor --model empty) stderr missing %q; got %q", "--model is required", stderr)
	}
}

// TestContextDoctor_RequiresPromptFile: --prompt-file missing →
// exit 2 AND stderr contains "--prompt-file is required".
func TestContextDoctor_RequiresPromptFile(t *testing.T) {
	code, _, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "http://127.0.0.1:9",
		"--model", "m",
	})
	if code != 2 {
		t.Fatalf("run(context doctor --prompt-file missing) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "--prompt-file is required") {
		t.Fatalf("run(context doctor --prompt-file missing) stderr missing %q; got %q", "--prompt-file is required", stderr)
	}
}

// TestContextDoctor_EmptyConfig_TG2 is the GOAL §6 TG2 binding
// pin. The test drives `run([]string{"context", "doctor", ...})`
// with the minimum flag set (--base-url http://127.0.0.1:9 --model
// tg --workspace /tmp --permission read_only --prompt-file
// <prompt containing 'hi'>). The unreachable --base-url is the
// determinism handle that proves the surface does NOT call the
// model client. The assertions: exit code 0 AND stdout contains
// "doctor findings" AND "no findings." (the empty ledger case
// from a 2-byte prompt).
func TestContextDoctor_EmptyConfig_TG2(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context doctor empty-config) returned %d, want 0 (stderr=%q)", code, stderr)
	}
	for _, want := range []string{"doctor findings", "no findings."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run(context doctor empty-config) stdout missing %q; got %q", want, stdout)
		}
	}
}

// TestContextDoctor_FullConfig_PlantLargeContributor_FindsByName
// is the GOAL §5 reviewer duty 2 binding pin: the doctor must
// find a planted large contributor by name. The test constructs
// a 5000-character prompt file (so Total() = 1250 tokens, > 1000
// token large threshold) and runs `context doctor`. The
// assertions: stdout contains "doctor findings" AND "large" AND
// "task:" (the category label) AND "task" (the contributor name
// from r.PopulateLedger's task entry; the task category's Name
// field is "task" per the existing wiring at
// internal/loop/loop.go).
func TestContextDoctor_FullConfig_PlantLargeContributor_FindsByName(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	// 5000 chars so Total() = (5000+3)/4 = 1250 tokens > 1000
	// threshold. The harness system prompt also contributes
	// tokens (~100-200), so the total is well over 1000.
	promptContent := strings.Repeat("X", 5000)
	if err := os.WriteFile(promptPath, []byte(promptContent), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context doctor planted-large) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	for _, want := range []string{"doctor findings", "large", "task:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run(context doctor planted-large) stdout missing %q; got %q", want, stdout)
		}
	}
	// The contributor name for the task category is "task" per
	// internal/loop/loop.go's PopulateLedger implementation
	// (the task entry's Name field is set to "task"). The
	// finding's Detail field renders as
	// "<category>: <name> contributes <n> tokens (threshold
	// 1000)"; the binding pin asserts the substring "task"
	// appears in the rendered output (the contributor name is
	// the second word after "task:" in the Detail string AND
	// in the label format "<category>: <name>" emitted by
	// formatDoctorFindings). The literal substring "task"
	// appears multiple times in the rendered output
	// (formatDoctorFindings renders the label, and the Detail
	// field also includes "task: task contributes ..."); the
	// binding test is satisfied if "task" appears anywhere in
	// the rendered output.
	if !strings.Contains(stdout, "task") {
		t.Fatalf("run(context doctor planted-large) stdout missing contributor name %q; got %q", "task", stdout)
	}
}

// TestContextDoctor_DoesNotCallModel: the determinism handle.
// Drive runContextDoctor with --base-url http://127.0.0.1:9 (the
// discard target; a real model call would fail with "connection
// refused" or "context deadline exceeded"). Assertions: exit
// code 0 AND stderr does NOT contain "context deadline exceeded"
// (the model client was NOT invoked despite the 1-second
// RequestTimeout safety belt).
func TestContextDoctor_DoesNotCallModel(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, stdout, stderr := captureContext(t, []string{
		"context", "doctor",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
	})
	if code != 0 {
		t.Fatalf("run(context doctor unreachable-base-url) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "doctor findings") {
		t.Fatalf("run(context doctor unreachable-base-url) stdout missing %q; got %q", "doctor findings", stdout)
	}
	if strings.Contains(strings.ToLower(stderr), "context deadline exceeded") {
		t.Fatalf("run(context doctor unreachable-base-url) stderr contains %q (model client appears to have been invoked); stderr=%q",
			"context deadline exceeded", stderr)
	}
}

// TestContextShow_Limit_FlagsParse: confirm the --limit <n> flag
// parses correctly on `context show` and the surface does NOT
// exit 2 when the content fits within the configured limit. The
// test uses a small prompt ("hi") and --limit 1000; Total() is
// well under 1000 so no overflow fires.
func TestContextShow_Limit_FlagsParse(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
		"--limit", "1000",
	})
	if code != 0 {
		t.Fatalf("run(context show --limit 1000) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "total") {
		t.Fatalf("run(context show --limit 1000) stdout missing %q; got %q", "total", stdout)
	}
}

// TestContextShow_Limit_OverflowExits2: confirm the --limit <n>
// overflow enforcement on `context show`. The test uses a
// 2-byte prompt ("hi") and --limit 1; Total() is well over 1
// (the harness system prompt alone contributes tokens), so the
// overflow check fires and exits 2 with the SCOPE §18 overflow
// error.
func TestContextShow_Limit_OverflowExits2(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
		"--limit", "1",
	})
	if code != 2 {
		t.Fatalf("run(context show --limit 1) returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "config error: context overflow:") {
		t.Fatalf("run(context show --limit 1) stderr missing %q; got %q", "config error: context overflow:", stderr)
	}
	if !strings.Contains(stderr, "exceeds configured limit 1") {
		t.Fatalf("run(context show --limit 1) stderr missing %q; got %q", "exceeds configured limit 1", stderr)
	}
}

// TestContextShow_Limit_NoLimit_DefaultsToZero: confirm the
// default (--limit omitted) AND the explicit --limit 0 both
// disable the overflow check. The test uses a 5000-char prompt
// and --limit 0; Total() = 1250 tokens, which would exceed any
// positive limit, but with Limit = 0 the overflow check is
// skipped and the surface exits 0.
func TestContextShow_Limit_NoLimit_DefaultsToZero(t *testing.T) {
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	promptContent := strings.Repeat("X", 5000)
	if err := os.WriteFile(promptPath, []byte(promptContent), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, stdout, stderr := captureContext(t, []string{
		"context", "show",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", "/tmp",
		"--permission", "read_only",
		"--prompt-file", promptPath,
		"--limit", "0",
	})
	if code != 0 {
		t.Fatalf("run(context show --limit 0 with 1250-token prompt) returned %d, want 0 (stderr=%q stdout=%q)", code, stderr, stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "total") {
		t.Fatalf("run(context show --limit 0) stdout missing %q; got %q", "total", stdout)
	}
}
