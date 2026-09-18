package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/svend-blip/simple-harness/internal/session"
	"github.com/svend-blip/simple-harness/internal/tools"
	"github.com/svend-blip/simple-harness/internal/tools/builtins"
)

// withBuiltins swaps in a fresh registry carrying the builtin tools for
// the duration of the test (main() registers them; run() does not).
func withBuiltins(t *testing.T) {
	t.Helper()
	saved := globalRegistry
	t.Cleanup(func() { globalRegistry = saved })
	fresh := tools.NewRegistry()
	builtins.RegisterBuiltins(fresh)
	globalRegistry = fresh
}

// toolThenTextServer answers the first request with a read_file tool
// call on `file` and every later request with a final text.
func toolThenTextServer(t *testing.T, file string) *httptest.Server {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&n, 1) == 1 {
			args, _ := json.Marshal(map[string]any{"path": file})
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":"Looking. ","tool_calls":[{"index":0,"id":"call_rf","function":{"name":"read_file","arguments":%q}}]}}]}`+"\n\n", string(args))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"FINAL-ANSWER"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readMessages(t *testing.T, stateDir string) []session.Message {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("state dir %s: %v entries=%d", stateDir, err, len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, entries[0].Name(), "messages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []session.Message
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m session.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("messages.jsonl line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func readSessionJSON(t *testing.T, stateDir string) session.Session {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("state dir %s: %v entries=%d", stateDir, err, len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, entries[0].Name(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s session.Session
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRun_StdinPrompt_IsExecuted — `--prompt-file -` is documented as
// "read the prompt from stdin". It returned exit 0 without reading
// stdin, calling the model, or emitting an event: a documented no-op
// reported as success.
func TestRun_StdinPrompt_IsExecuted(t *testing.T) {
	var captured struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = inR
	go func() {
		_, _ = io.WriteString(inW, "PROMPT-FROM-STDIN")
		_ = inW.Close()
	}()

	stateDir := t.TempDir()
	code, out, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "m",
		"--workspace", t.TempDir(),
		"--state-dir", stateDir,
		"--prompt-file", "-",
		"--output", "jsonl",
	)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stderr=%q)", code, errOut)
	}
	if len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Content != "PROMPT-FROM-STDIN" {
		t.Errorf("model did not receive the stdin prompt: %+v", captured.Messages)
	}
	if !strings.Contains(out, `"event":"completed"`) {
		t.Errorf("no completed event on stdout: %q", out)
	}
}

// TestRun_SessionJSON_ExitCodeMatchesTheProcessExit — session.json
// recorded exit_code 1 / status failed for a permission violation the
// process exited 4 on, and completed / 0 for a --limit overflow the
// process exited 2 on. The record must say what the process did.
func TestRun_SessionJSON_ExitCodeMatchesTheProcessExit(t *testing.T) {
	withBuiltins(t)
	ws := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		args, _ := json.Marshal(map[string]any{"path": filepath.Join(ws, "x.txt"), "content": "y"})
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","function":{"name":"write_file","arguments":%q}}]}}]}`+"\n\n", string(args))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	promptFile := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(promptFile, []byte("write"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	code, _, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "m",
		"--workspace", ws,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
		"--permission", "read_only",
	)
	if code != 4 {
		t.Fatalf("exit %d, want 4 (stderr=%q)", code, errOut)
	}
	s := readSessionJSON(t, stateDir)
	if s.ExitCode != 4 || s.Status != session.StatusFailed {
		t.Errorf("session.json exit_code=%d status=%q, want 4/failed", s.ExitCode, s.Status)
	}
}

// TestRun_MessagesJSONL_RecordsTheExecutionHistory — the contract says
// messages.jsonl carries every message including tool results; it
// carried the user prompt and one concatenated assistant line.
func TestRun_MessagesJSONL_RecordsTheExecutionHistory(t *testing.T) {
	withBuiltins(t)
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := toolThenTextServer(t, file)
	promptFile := filepath.Join(t.TempDir(), "p.md")
	if err := os.WriteFile(promptFile, []byte("read it"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	code, _, errOut := driveRun(t,
		"--base-url", srv.URL,
		"--model", "m",
		"--workspace", ws,
		"--state-dir", stateDir,
		"--prompt-file", promptFile,
		"--output", "jsonl",
	)
	if code != 0 {
		t.Fatalf("exit %d (stderr=%q)", code, errOut)
	}
	msgs := readMessages(t, stateDir)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := []string{"user", "assistant", "tool", "assistant"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "read_file" || msgs[1].Content != "Looking. " {
		t.Errorf("assistant tool-call record = %+v", msgs[1])
	}
	if msgs[2].ToolCallID != "call_rf" || !strings.Contains(msgs[2].Content, "hello") {
		t.Errorf("tool result record = %+v", msgs[2])
	}
	if msgs[3].Content != "FINAL-ANSWER" {
		t.Errorf("final assistant record = %+v", msgs[3])
	}
}

// TestInteractive_DispatchesToolCalls — the REPL advertised the tool
// registry to the model but ran the single-turn loop, which drops
// tool calls; a model that answered with one produced an empty
// response and a COMPLETED status.
func TestInteractive_DispatchesToolCalls(t *testing.T) {
	withBuiltins(t)
	ws := t.TempDir()
	file := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := toolThenTextServer(t, file)
	t.Setenv("SIMPLE_HARNESS_BASE_URL", srv.URL+"/v1")
	code, out, stateDir := driveInteractive(t, "read it\n/exit\n", "--workspace", ws)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "FINAL-ANSWER") {
		t.Errorf("stdout %q lacks the final answer the tool round should have produced", out)
	}
	entries, _ := os.ReadDir(stateDir)
	if len(entries) != 1 {
		t.Fatalf("sessions = %d", len(entries))
	}
	events, _ := os.ReadFile(filepath.Join(stateDir, entries[0].Name(), "events.jsonl"))
	if !bytes.Contains(events, []byte(`"event":"tool_call"`)) || !bytes.Contains(events, []byte(`"event":"tool_result"`)) {
		t.Errorf("events.jsonl carries no tool_call/tool_result: %s", events)
	}
	msgs := readMessages(t, stateDir)
	var sawTool bool
	for _, m := range msgs {
		if m.Role == "tool" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Errorf("messages.jsonl has no tool message: %+v", msgs)
	}
}

// TestUnknownSubcommandIsRejected — `simple-harness bogus` fell through
// to interactive mode, waited on stdin, and exited 0 leaving a session
// directory behind.
func TestUnknownSubcommandIsRejected(t *testing.T) {
	origStdin, origStderr := os.Stdin, os.Stderr
	t.Cleanup(func() { os.Stdin, os.Stderr = origStdin, origStderr })
	inR, inW, _ := os.Pipe()
	_ = inW.Close()
	os.Stdin = inR
	errR, errW, _ := os.Pipe()
	os.Stderr = errW
	stateDir := t.TempDir()
	code := run([]string{"bogus", "--state-dir", stateDir})
	_ = errW.Close()
	var eb bytes.Buffer
	_, _ = io.Copy(&eb, errR)
	if code != 2 {
		t.Fatalf("exit %d, want 2 (stderr=%q)", code, eb.String())
	}
	if !strings.Contains(eb.String(), "bogus") {
		t.Errorf("stderr %q does not name the unknown subcommand", eb.String())
	}
	if entries, _ := os.ReadDir(stateDir); len(entries) != 0 {
		t.Errorf("a session directory was created for an unknown subcommand")
	}
}
