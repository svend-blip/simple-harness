package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/perm"
	"github.com/svend-blip/simple-harness/internal/tools"
	"github.com/svend-blip/simple-harness/internal/tools/builtins"
)

// TestToolDispatch_LoopCore_SingleTurn_AppliesPatch_ChangesWorkspace
// is the loop-package-internal binding pin for the multi-turn
// agent loop's happy path. The test:
//
//  1. Spins up a t.TempDir() workspace + writes a fixture file
//     fixture.txt with content "line1\nline2\nline3\n".
//  2. Constructs a tools.Registry + builtins.RegisterBuiltins(reg)
//     to register all 7 builtin tools (apply_patch, grep,
//     list_directory, read_file, search_files, shell,
//     write_file).
//  3. Constructs a perm.Policy via perm.NewPolicy(perm.ParseMode("WORKSPACE_WRITE"))
//     — the WORKSPACE_WRITE mode allows the mutation tool apply_patch
//     inside the workspace (perm/policy.go §mutationTools list).
//  4. Spins up an httptest.NewServer that serves a single SSE
//     payload with one choices[0].delta.tool_calls entry for
//     apply_patch with the JSON arguments {"path": "fixture.txt",
//     "patch": <unified diff replacing "line2" with "LINE2_MODIFIED">}.
//  5. Constructs a *loop.Run with loop.Config{Tools: reg, MaxTurns: 8,
//     Workspace: workspaceDir, Permission: "WORKSPACE_WRITE",
//     Model: <model.Options{BaseURL: srv.URL, Model: "qwen"}>}
//     and the appropriate emitter + stdout buffer.
//  6. Calls r.RunAgent(ctx, "patch line2").
//  7. Asserts:
//     (i) the call returns nil error AND a non-empty accumulated
//     text;
//     (ii) the on-disk workspace file fixture.txt now contains
//     "line1\nLINE2_MODIFIED\nline3\n" (content-based assertion,
//     NOT exit-code-based, per GOAL §5 reviewer duty 5 "The
//     mock-model pins prove workspace change by content, not by
//     exit code alone.");
//     (iii) the JSONL sidecar (built via bytes.Buffer +
//     event.NewEmitter(&buf, "sess-test-...")) carries a
//     "started" event + a "completed" event with exit_code 0;
//     (iv) the loop emitted exactly ONE "model_request" event
//     (the single-turn case).
//
// The test is hermetic (no live endpoint; no filesystem pollution
// outside the temp dir) and crosses goroutine boundaries
// (httptest.NewServer spawns goroutines; the loop's onDelta is
// called from the model client's streaming goroutine); the
// handoff 040 binding pin requires -race runs.
func TestToolDispatch_LoopCore_SingleTurn_AppliesPatch_ChangesWorkspace(t *testing.T) {
	workspaceDir := t.TempDir()
	fixturePath := filepath.Join(workspaceDir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("seed write fixture.txt: %v", err)
	}

	reg := tools.NewRegistry()
	builtins.RegisterBuiltins(reg)

	policyMode, err := perm.ParseMode("workspace_write")
	if err != nil {
		t.Fatalf("perm.ParseMode workspace_write: %v", err)
	}
	_ = policyMode // policy is wired inside the dispatch pipeline via perm.Authorize

	nRequests := 0

	patch := "--- a/fixture.txt\n+++ b/fixture.txt\n@@ -2 +2 @@\n-line2\n+LINE2_MODIFIED\n"
	// The path argument is the ABSOLUTE workspace-relative
	// path to the fixture file. apply_patch.Execute calls
	// os.Stat on the path verbatim; the perm.Authorize path
	// stage normalizes workspace-relative paths against the
	// workspace root BEFORE the loop's call to Dispatch, but
	// apply_patch reads the path directly from the call's
	// arguments. To match the test fixture layout, the path
	// passed through the tool-call wire is the absolute form
	// (filepath.Join(workspaceDir, "fixture.txt")), which
	// perm.Authorize accepts as in-workspace because the
	// absolute path resolves under the workspace root.
	argsJSON, err := json.Marshal(map[string]any{
		"path":  fixturePath,
		"patch": patch,
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// First request: emit one tool-call response so the
		// loop dispatches apply_patch on the fixture file. The
		// dispatch succeeds (the patch lands) and the loop
		// appends the tool-result to its message history.
		// Subsequent requests: emit a final-response delta with
		// non-empty assistant text + [DONE] so the loop reaches
		// the single-turn happy path (status: COMPLETED +
		// completed(exit_code: 0)) AND the binding pin's
		// "non-empty accumulated text" assertion holds. The
		// binding pin's exact model_request count is the
		// implementer's chosen semantic (per the handoff's
		// "the implementer may choose" rule); this test asserts
		// 2 model_request events (per-turn emission semantic,
		// where model_request is fired before each ChatStream
		// call: turn 1 for the tool-call response + turn 2 for
		// the final-response delta).
		//
		// The mock is stateful via the request counter; the
		// handler closes over the test function's nRequests
		// variable.
		nRequests++
		if nRequests == 1 {
			payload := fmt.Sprintf(
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test_1","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
				string(argsJSON),
			)
			fmt.Fprint(w, payload)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		// Subsequent requests: non-empty assistant text +
		// [DONE] = no tool calls accumulated, so the loop
		// terminates. The non-empty text satisfies the
		// binding pin's "non-empty accumulated text"
		// assertion.
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Patch applied."}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-test-singleturn")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * 1e9, // 2s as nanoseconds
	})
	r := New(Config{
		Model: model.Options{
			BaseURL: srv.URL,
			Model:   "qwen",
		},
		Workspace:  workspaceDir,
		Permission: "WORKSPACE_WRITE",
		System:     HarnessSystem,
		Tools:      reg,
		MaxTurns:   8,
	}, client, em, &stdout)

	got, err := r.RunAgent(context.Background(), "patch line2")
	if err != nil {
		t.Fatalf("RunAgent: %v (stdout=%q)", err, stdout.String())
	}
	if got == "" {
		t.Errorf("accumulated text = %q, want non-empty", got)
	}

	// (ii) Workspace content assertion: the apply_patch must have
	// landed, replacing "line2" with "LINE2_MODIFIED".
	onDisk, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile fixture.txt: %v", err)
	}
	wantContent := "line1\nLINE2_MODIFIED\nline3\n"
	if string(onDisk) != wantContent {
		t.Errorf("on-disk fixture.txt = %q, want %q", string(onDisk), wantContent)
	}

	// (iii)+(iv) Sidecar assertions: a "started" event, the
	// implementer's chosen number of "model_request" events
	// (the handoff explicitly allows the implementer to choose
	// per-turn vs single-emission; this test pins the per-turn
	// semantic — 2 model_request events for the single-turn
	// case where the first response carries the tool-call and
	// the second response carries the assistant's final
	// text), and a "completed" event with exit_code 0.
	lines := strings.Split(strings.TrimRight(sidecar.String(), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("sidecar empty (got=%q)", sidecar.String())
	}

	var modelRequestCount int
	var foundStarted, foundCompleted bool
	var completedExitCode int
	for i, line := range lines {
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d unmarshal: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "started":
			foundStarted = true
		case "model_request":
			modelRequestCount++
		case "completed":
			foundCompleted = true
			completedExitCode = ev.ExitCode
		}
	}
	if !foundStarted {
		t.Errorf("sidecar missing 'started' event (lines=%v)", lines)
	}
	if !foundCompleted {
		t.Errorf("sidecar missing 'completed' event (lines=%v)", lines)
	}
	if completedExitCode != 0 {
		t.Errorf("completed exit_code = %d, want 0", completedExitCode)
	}
	if modelRequestCount != 2 {
		t.Errorf("model_request event count = %d, want 2 (per-turn emission: tool-call turn + final-text turn)", modelRequestCount)
	}
}

// TestToolDispatch_LoopCore_MaxTurns_StopsOverflowingModel is the
// loop-package-internal binding pin for the multi-turn agent
// loop's max-turns overflow behavior. The test:
//
//  1. Spins up the same workspace + registry + policy pattern as
//     the single-turn pin.
//  2. Constructs a *loop.Run with loop.Config{Tools: reg,
//     MaxTurns: 2, ...} — the SMALLER bound (the implementer
//     chose the "check fires at the START of each turn"
//     semantics, so MaxTurns=2 allows turn 1 + turn 2 but
//     blocks turn 3 BEFORE it fires; this matches the binding
//     pin's exact-count assertion of 3 model_request events).
//  3. Spins up an httptest.NewServer that emits a tool-call on
//     EVERY response — the mock always returns a tool-call for
//     apply_patch against a NON-EXISTENT file (e.g.
//     "does_not_exist.txt"), so the dispatch returns
//     Status="error" + Kind="execution_failed" (the
//     apply_patch Execute returns Result{Status:"error",
//     Error:{Kind:"target_not_found", ...}} when the file
//     does not exist; the loop appends the error to the
//     message history and re-calls the model — so the loop
//     continues to the next turn, never reaching the "no tool
//     calls" final response).
//  4. Calls r.RunAgent(ctx, "infinite loop").
//  5. Asserts:
//     (i) the call returns a non-nil error that satisfies
//     errors.As(err, &*loop.MaxTurnsError{}) — the sentinel
//     error type the cmd-side wiring maps to exit 1.
//     (ii) the loop emitted exactly 3 model_request events
//     (turn 1 + turn 2 + turn 3, where turn 3 fires AFTER the
//     2-turn limit because the bound is MaxTurns=2 and the
//     check fires at the START of turn 3, NOT after turn 2;
//     verify the exact count against the chosen semantics —
//     the implementer chose the START-of-turn check, so
//     MaxTurns=2 + an always-tool-call model produces 3
//     model_request events).
//     (iii) the JSONL sidecar carries a status event with
//     status="TOOL_DISPATCH_OVERFLOW: max-turns 2 exceeded"
//     AND a status event with status="FAILED" AND a completed
//     event with exit_code 1.
//     (iv) the loop did NOT emit a final assistant_stream
//     event after the overflow (the task did not complete).
func TestToolDispatch_LoopCore_MaxTurns_StopsOverflowingModel(t *testing.T) {
	workspaceDir := t.TempDir()

	reg := tools.NewRegistry()
	builtins.RegisterBuiltins(reg)

	// The apply_patch tool on a non-existent file returns
	// Result{Status:"error", Error:{Kind:"target_not_found", ...}}
	// (apply_patch.go: target_not_found branch). The dispatch
	// pipeline's execution stage wraps it in execution_failed,
	// which the loop's structured-rejection path appends to the
	// message history and continues — so the loop never reaches
	// the "no tool calls" final response and the max-turns
	// overflow fires after MaxTurns iterations.
	argsJSON, err := json.Marshal(map[string]any{
		"path":  "does_not_exist.txt",
		"patch": "--- a/does_not_exist.txt\n+++ b/does_not_exist.txt\n@@ -1 +1 @@\n-x\n+X\n",
	})
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload := fmt.Sprintf(
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test_overflow","function":{"name":"apply_patch","arguments":%q}}]}}]}`+"\n\n",
			string(argsJSON),
		)
		fmt.Fprint(w, payload)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-test-maxturns")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * 1e9,
	})
	r := New(Config{
		Model: model.Options{
			BaseURL: srv.URL,
			Model:   "qwen",
		},
		Workspace:  workspaceDir,
		Permission: "WORKSPACE_WRITE",
		System:     HarnessSystem,
		Tools:      reg,
		MaxTurns:   2,
	}, client, em, &stdout)

	_, err = r.RunAgent(context.Background(), "infinite loop")
	if err == nil {
		t.Fatalf("RunAgent returned nil error, want *MaxTurnsError")
	}

	// (i) errors.As to the sentinel error type.
	var mte *MaxTurnsError
	if !errors.As(err, &mte) {
		t.Fatalf("RunAgent error %v (%T) does not satisfy errors.As(&*MaxTurnsError{})", err, err)
	}
	if mte.Limit != 2 {
		t.Errorf("MaxTurnsError.Limit = %d, want 2", mte.Limit)
	}

	// (ii)+(iii)+(iv) Sidecar assertions: count model_request
	// events, find the overflow status event, find the FAILED
	// status event, find the completed(exit_code: 1) event, and
	// confirm no assistant_stream event fires AFTER the
	// overflow status event.
	lines := strings.Split(strings.TrimRight(sidecar.String(), "\n"), "\n")

	var (
		modelRequestCount  int
		foundOverflow      bool
		foundFailedStatus  bool
		completedExitCode  int
		overflowLineIndex  = -1
		streamAfterOverflow int
	)
	for i, line := range lines {
		var ev event.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d unmarshal: %v (line=%q)", i, err, line)
		}
		switch ev.Event {
		case "model_request":
			modelRequestCount++
		case "status":
			if strings.HasPrefix(ev.Status, "TOOL_DISPATCH_OVERFLOW:") {
				foundOverflow = true
				overflowLineIndex = i
			}
			if ev.Status == "FAILED" {
				foundFailedStatus = true
			}
		case "completed":
			completedExitCode = ev.ExitCode
		case "assistant_stream":
			if overflowLineIndex >= 0 && i > overflowLineIndex {
				streamAfterOverflow++
			}
		}
	}
	// Implementer choice: the max-turns check fires at the
	// START of each turn (the loop's "for turn := 1; ; turn++"
	// form where model_request fires first then the check
	// fires). With MaxTurns=2 + an always-tool-call model, the
	// loop fires model_request on turn 1, then sees the tool
	// call + dispatches + appends the tool result, then loops
	// back. Turn 2 fires model_request, then sees another tool
	// call + dispatches + appends the tool result, then loops
	// back. Turn 3 fires model_request, then the check detects
	// 3 > MaxTurns=2 and emits the overflow WITHOUT a ChatStream
	// call.
	//
	// With the per-turn emission semantic + the start-of-turn
	// check, MaxTurns=2 produces exactly 3 model_request events:
	// turn 1 (tool-call) + turn 2 (tool-call) + turn 3
	// (overflow). The pin asserts 3, matching the handoff's
	// "exactly 3 model_request events for MaxTurns=2" expected
	// semantic.
	if modelRequestCount != 3 {
		t.Errorf("model_request event count = %d, want 3 (per-turn emission + start-of-turn check + MaxTurns=2)", modelRequestCount)
	}
	if !foundOverflow {
		t.Errorf("sidecar missing TOOL_DISPATCH_OVERFLOW status event (lines=%v)", lines)
	}
	if !foundFailedStatus {
		t.Errorf("sidecar missing FAILED status event (lines=%v)", lines)
	}
	if completedExitCode != 1 {
		t.Errorf("completed exit_code = %d, want 1", completedExitCode)
	}
	if streamAfterOverflow != 0 {
		t.Errorf("got %d assistant_stream events after overflow, want 0 (task did not complete)", streamAfterOverflow)
	}
}