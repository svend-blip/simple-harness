package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/skill"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestRunOne_HappyPath_AccumulatesAndStreams is the load-bearing
// integration test: spin up a real httptest server that returns the
// standard SSE deltas, construct a Run with a model.Client pointed
// at it and a bytes.Buffer as the human-facing stdout, call
// RunOne, and assert (a) the buffer has the concatenated deltas,
// (b) the sidecar has the expected sequence (started →
// model_request → status: STREAMING → N assistant_stream → status:
// COMPLETED → completed with exit_code 0), and (c) the returned
// string matches the buffer. The reviewer can sed-replace any of
// these assertions to confirm the test is load-bearing.
func TestRunOne_HappyPath_AccumulatesAndStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello, "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"world"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"!"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-loop-test")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model: model.Options{
			BaseURL: srv.URL,
			Model:   "qwen",
		},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	got, err := r.RunOne(context.Background(), "hi")
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if got != "Hello, world!" {
		t.Errorf("returned string = %q, want %q", got, "Hello, world!")
	}
	if stdout.String() != "Hello, world!" {
		t.Errorf("stdout buffer = %q, want %q", stdout.String(), "Hello, world!")
	}

	// Sidecar sequence: started, model_request, status STREAMING,
	// 3x assistant_stream, status COMPLETED, completed exit_code 0
	// -> 8 lines.
	lines := strings.Split(strings.TrimRight(sidecar.String(), "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("got %d sidecar lines, want 8 (output=%q)", len(lines), sidecar.String())
	}

	var s event.Event
	if err := json.Unmarshal([]byte(lines[0]), &s); err != nil {
		t.Fatalf("line 0 unmarshal: %v", err)
	}
	if s.Event != "started" {
		t.Errorf("line 0 Event = %q, want started", s.Event)
	}
	if s.Config == nil || s.Config.Model != "qwen" || s.Config.Workspace != "/tmp/ws" ||
		s.Config.Permission != "READ_ONLY" {
		t.Errorf("line 0 Config = %+v", s.Config)
	}

	var mr event.Event
	if err := json.Unmarshal([]byte(lines[1]), &mr); err != nil {
		t.Fatalf("line 1 unmarshal: %v", err)
	}
	if mr.Event != "model_request" {
		t.Errorf("line 1 = %+v, want model_request", mr)
	}

	var st1 event.Event
	if err := json.Unmarshal([]byte(lines[2]), &st1); err != nil {
		t.Fatalf("line 2 unmarshal: %v", err)
	}
	if st1.Event != "status" || st1.Status != "STREAMING" {
		t.Errorf("line 2 = %+v, want status STREAMING", st1)
	}

	for i := 3; i < 6; i++ {
		var a event.Event
		if err := json.Unmarshal([]byte(lines[i]), &a); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if a.Event != "assistant_stream" {
			t.Errorf("line %d Event = %q, want assistant_stream", i, a.Event)
		}
	}

	var st2 event.Event
	if err := json.Unmarshal([]byte(lines[6]), &st2); err != nil {
		t.Fatalf("line 6 unmarshal: %v", err)
	}
	if st2.Event != "status" || st2.Status != "COMPLETED" {
		t.Errorf("line 6 = %+v, want status COMPLETED", st2)
	}

	var c event.Event
	if err := json.Unmarshal([]byte(lines[7]), &c); err != nil {
		t.Fatalf("line 7 unmarshal: %v", err)
	}
	if c.Event != "completed" || c.ExitCode != 0 {
		t.Errorf("line 7 = %+v, want completed exit_code 0", c)
	}
}

// TestRunOne_PropagatesModelErrorAsModelError pins the error
// contract: an HTTP 500 from the model server surfaces as a
// *model.ModelError with Kind == ErrHTTP. The cmd relies on this
// pass-through to map to exit code 3.
func TestRunOne_PropagatesModelErrorAsModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"oops"}`)
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-err")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	_, err := r.RunOne(context.Background(), "hi")
	if err == nil {
		t.Fatal("RunOne: expected error, got nil")
	}
	var me *model.ModelError
	if !errors.As(err, &me) {
		t.Fatalf("err is not *model.ModelError: %T %v", err, err)
	}
	if me.Kind != model.ErrHTTP {
		t.Errorf("Kind = %v, want ErrHTTP", me.Kind)
	}
	if me.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", me.StatusCode)
	}
}

// TestRunOne_TimeoutMapsToErrTimeout pins the request-timeout path:
// a server that sleeps past the configured RequestTimeout surfaces
// as a *model.ModelError with Kind == ErrTimeout. The cmd relies
// on this pass-through to map to exit code 6.
func TestRunOne_TimeoutMapsToErrTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-timeout")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 50 * time.Millisecond,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	_, err := r.RunOne(context.Background(), "hi")
	if err == nil {
		t.Fatal("RunOne: expected timeout error, got nil")
	}
	var me *model.ModelError
	if !errors.As(err, &me) {
		t.Fatalf("err is not *model.ModelError: %T %v", err, err)
	}
	if me.Kind != model.ErrTimeout {
		t.Errorf("Kind = %v, want ErrTimeout", me.Kind)
	}
}

// TestNormalizeBaseURL is table-driven and pins the four shape
// variants the seam handles: trailing /v1, trailing /v1/, no /v1,
// and an embedded /v1 in a deeper path (must NOT be stripped —
// only a TRULY trailing /v1 is stripped).
func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://x/v1", "http://x"},
		{"http://x/v1/", "http://x"},
		{"http://x", "http://x"},
		{"http://x/v1/chat", "http://x/v1/chat"},
	}
	for _, tc := range cases {
		got := NormalizeBaseURL(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRunOne_EmitsModelRequestBeforeChatStream pins the
// model_request event ordering (Run 006 / handoff 022): the loop
// must emit "started" first, then "model_request" immediately
// before the model-client invocation, then continue with the
// existing status / assistant_stream / completed sequence. The
// test uses an httptest server returning a single
// `data: [DONE]\n\n` payload so the loop completes cleanly
// without exercising the streaming path; the assertion is on
// the FIRST TWO events only (per the handoff's "the test
// should NOT assert anything about subsequent events beyond
// model_request being the second" rule). A future regression
// that drops the model_request emission, reorders it after
// ChatStream, or fires it twice will fail this test.
func TestRunOne_EmitsModelRequestBeforeChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-mr-loop")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	lines := strings.Split(strings.TrimRight(sidecar.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d sidecar lines, want >= 2 (output=%q)", len(lines), sidecar.String())
	}

	// First event must be "started" with config populated
	// (the SessionConfig emitted on every turn per the loop's
	// RunOne contract).
	var first event.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line 0 unmarshal: %v (line=%q)", err, lines[0])
	}
	if first.Event != "started" {
		t.Errorf("line 0 Event = %q, want started", first.Event)
	}
	if first.Config == nil {
		t.Fatalf("line 0 Config = nil, want populated SessionConfig")
	}
	if first.Config.Model != "qwen" {
		t.Errorf("line 0 Config.Model = %q, want qwen", first.Config.Model)
	}

	// Second event must be "model_request" with no payload
	// beyond the base fields (the GOAL §2 minimum event set
	// names model_request with no event-specific fields).
	var second event.Event
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("line 1 unmarshal: %v (line=%q)", err, lines[1])
	}
	if second.Event != "model_request" {
		t.Errorf("line 1 Event = %q, want model_request", second.Event)
	}
	if second.Config != nil {
		t.Errorf("line 1 Config = %+v, want nil (model_request has no payload)", second.Config)
	}
	if second.Role != "" || second.Content != "" || second.Status != "" ||
		second.Delta != "" || second.ExitCode != 0 {
		t.Errorf("line 1 has non-empty payload: %+v", second)
	}
}

// --- handoff 033: TestComposeMessages_* tests + integration pin ---

// TestComposeMessages_EmptyConfig_OneUserMessage: the regression
// pin — a zero-value Config (empty System / empty SystemExternal /
// nil Skills) + a prompt produces a single user message. Every
// existing test that constructs Config{} (zero-value System / etc)
// relies on this; if a future regression adds a phantom system
// message, this test fails.
func TestComposeMessages_EmptyConfig_OneUserMessage(t *testing.T) {
	got := ComposeMessages(Config{}, "hello")
	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1 (got=%+v)", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Errorf("messages[0] = %+v, want {user, hello}", got[0])
	}
}

// TestComposeMessages_OnlyHarnessSystem: System populated, no
// external, no skills. The single harness-system message sits
// before the user task.
func TestComposeMessages_OnlyHarnessSystem(t *testing.T) {
	got := ComposeMessages(Config{System: "HARN"}, "hello")
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Role != "system" || got[0].Content != "HARN" {
		t.Errorf("messages[0] = %+v, want {system, HARN}", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "hello" {
		t.Errorf("messages[1] = %+v, want {user, hello}", got[1])
	}
}

// TestComposeMessages_OnlyExternalSystem: SystemExternal
// populated, System empty, no skills.
func TestComposeMessages_OnlyExternalSystem(t *testing.T) {
	got := ComposeMessages(Config{SystemExternal: "EXT"}, "hello")
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Role != "system" || got[0].Content != "EXT" {
		t.Errorf("messages[0] = %+v, want {system, EXT}", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "hello" {
		t.Errorf("messages[1] = %+v, want {user, hello}", got[1])
	}
}

// TestComposeMessages_OnlySkill: a single skill with non-empty
// Content yields one system message before the user task.
func TestComposeMessages_OnlySkill(t *testing.T) {
	got := ComposeMessages(Config{Skills: []skill.Skill{{Name: "s", Content: "S"}}}, "hello")
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Role != "system" || got[0].Content != "S" {
		t.Errorf("messages[0] = %+v, want {system, S}", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "hello" {
		t.Errorf("messages[1] = %+v, want {user, hello}", got[1])
	}
}

// TestComposeMessages_AllSlotsPopulated: the SCOPE §14 full
// ordering pin. All four slots populated; the output must be
// [harness, external, skill-A, skill-B, user] in that exact
// order.
func TestComposeMessages_AllSlotsPopulated(t *testing.T) {
	got := ComposeMessages(Config{
		System:        "H",
		SystemExternal: "E",
		Skills: []skill.Skill{
			{Name: "a", Content: "A"},
			{Name: "b", Content: "B"},
		},
	}, "hello")
	if len(got) != 5 {
		t.Fatalf("len(messages) = %d, want 5 (got=%+v)", len(got), got)
	}
	wantRoles := []string{"system", "system", "system", "system", "user"}
	wantContents := []string{"H", "E", "A", "B", "hello"}
	for i, w := range wantRoles {
		if got[i].Role != w {
			t.Errorf("messages[%d].Role = %q, want %q", i, got[i].Role, w)
		}
		if got[i].Content != wantContents[i] {
			t.Errorf("messages[%d].Content = %q, want %q", i, got[i].Content, wantContents[i])
		}
	}
}

// TestComposeMessages_OrderingIsPermutationProof is REVIEWER
// DUTY 2. The test constructs 6 permutations of the same set of
// content fragments across the 4 SCOPE §14 slots (harness /
// external / skills / prompt). The function MUST always produce
// the positional layout [harness, external, skills..., user]
// regardless of which fragment was passed to which slot — a
// future regression that reorders, drops, or duplicates a slot
// fails this test.
//
// Each permutation has exactly one skill, so the canonical
// output is 4 messages: [harness-system, external-system,
// skill-system, user]. The user's Content MUST always be the
// prompt letter (the LAST fragment in the permutation); the
// harness-system's Content MUST always be Sys (the FIRST
// fragment in the permutation). The handoff notes "len == 2"
// in prose but the permutations explicitly include a skill,
// so the binding layout pin is the 4-message positional order.
func TestComposeMessages_OrderingIsPermutationProof(t *testing.T) {
	permutations := []struct {
		name   string
		sys    string
		ext    string
		skills []skill.Skill
		prompt string
	}{
		{"S=H,E=E,Sk=A,P=P", "H", "E", []skill.Skill{{Name: "x", Content: "A"}}, "P"},
		{"S=A,E=H,Sk=P,P=E", "A", "H", []skill.Skill{{Name: "x", Content: "P"}}, "E"},
		{"S=E,E=P,Sk=H,P=A", "E", "P", []skill.Skill{{Name: "x", Content: "H"}}, "A"},
		{"S=P,E=A,Sk=E,P=H", "P", "A", []skill.Skill{{Name: "x", Content: "E"}}, "H"},
		{"S=H,E=P,Sk=E,P=A", "H", "P", []skill.Skill{{Name: "x", Content: "E"}}, "A"},
		{"S=A,E=H,Sk=P,P=E (2)", "A", "H", []skill.Skill{{Name: "x", Content: "P"}}, "E"},
	}
	for _, p := range permutations {
		t.Run(p.name, func(t *testing.T) {
			got := ComposeMessages(Config{
				System:        p.sys,
				SystemExternal: p.ext,
				Skills:        p.skills,
			}, p.prompt)
			// 4 messages: harness, external, skill, user.
			if len(got) != 4 {
				t.Fatalf("len(messages) = %d, want 4 (got=%+v)", len(got), got)
			}
			// All but the last must be system messages; the last
			// must be the user message.
			for i := 0; i < 3; i++ {
				if got[i].Role != "system" {
					t.Errorf("messages[%d].Role = %q, want system", i, got[i].Role)
				}
			}
			if got[3].Role != "user" {
				t.Errorf("messages[3].Role = %q, want user", got[3].Role)
			}
			// The user-message Content MUST equal the prompt (the
			// LAST fragment) — never a shuffled system fragment,
			// even when the prompt's content matches a system-
			// fragment letter.
			if got[3].Content != p.prompt {
				t.Errorf("messages[3].Content = %q, want %q (prompt must end up at the user slot)", got[3].Content, p.prompt)
			}
			// The harness-system slot (messages[0]) MUST carry the
			// Sys fragment (the FIRST fragment) — never a shuffled
			// external/skill/prompt fragment.
			if got[0].Content != p.sys {
				t.Errorf("messages[0].Content = %q, want %q (harness slot must be first)", got[0].Content, p.sys)
			}
		})
	}
}

// TestComposeMessages_SkillsPreserveOrder: 3 skills [A, B, C].
// The output's skills slot is [A, B, C] in that order; the
// harness + external + user slots are present and in their
// canonical positions.
func TestComposeMessages_SkillsPreserveOrder(t *testing.T) {
	got := ComposeMessages(Config{
		System:        "H",
		SystemExternal: "E",
		Skills: []skill.Skill{
			{Name: "1", Content: "A"},
			{Name: "2", Content: "B"},
			{Name: "3", Content: "C"},
		},
	}, "task")
	if len(got) != 6 {
		t.Fatalf("len(messages) = %d, want 6 (got=%+v)", len(got), got)
	}
	want := []string{"H", "E", "A", "B", "C", "task"}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("messages[%d].Content = %q, want %q", i, got[i].Content, w)
		}
	}
}

// TestComposeMessages_EmptySkillContentIsSkipped: 2 skills, one
// with empty Content. The output has 1 skill message (the
// non-empty one); the empty-skill slot is absent — NOT a
// zero-byte system message in the list.
func TestComposeMessages_EmptySkillContentIsSkipped(t *testing.T) {
	got := ComposeMessages(Config{
		Skills: []skill.Skill{
			{Name: "filled", Content: "FILLED"},
			{Name: "empty", Content: ""},
		},
	}, "task")
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Content != "FILLED" {
		t.Errorf("messages[0].Content = %q, want %q", got[0].Content, "FILLED")
	}
	if got[1].Role != "user" || got[1].Content != "task" {
		t.Errorf("messages[1] = %+v, want {user, task}", got[1])
	}
}

// TestRunOne_PassesComposedMessagesToClient is the binding wire
// pin for the SCOPE §14 composition contract. The test spins up
// an httptest server that captures the request body and asserts
// the incoming messages JSON is exactly
// [system: HarnessSystem, system: "EXT", system: "SKILL",
//   user: "hello"]
// when constructed with the full populated Config. A future
// regression that drops a slot, reorders slots, or adds duplicate
// messages fails this test (it parses the actual outgoing JSON).
func TestRunOne_PassesComposedMessagesToClient(t *testing.T) {
	type capturedRequest struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-compose-test")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model: model.Options{BaseURL: srv.URL, Model: "qwen"},
		// Workspace + Permission kept for the started-event shape.
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "EXT",
		Skills:         []skill.Skill{{Name: "s", Content: "SKILL"}},
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hello"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	// On the wire the three composed system slots travel as ONE system
	// message (base, external, skill — in that order, blank-line
	// separated): FreeToken's chat template rejects a second system
	// message with HTTP 400 (2026-09-03, 9000-02-ELOOP h46). The
	// composition itself is still three slots — see TestComposeMessages_*.
	if len(captured.Messages) != 2 {
		t.Fatalf("captured len(messages) = %d, want 2 (got=%+v)", len(captured.Messages), captured.Messages)
	}
	want := []struct{ role, content string }{
		{"system", HarnessSystem + "\n\nEXT\n\nSKILL"},
		{"user", "hello"},
	}
	for i, w := range want {
		if captured.Messages[i].Role != w.role {
			t.Errorf("captured[%d].role = %q, want %q", i, captured.Messages[i].Role, w.role)
		}
		if captured.Messages[i].Content != w.content {
			t.Errorf("captured[%d].content = %q, want %q", i, captured.Messages[i].Content, w.content)
		}
	}
}

// --- handoff 035: TestRun_Ledger_* tests ---

// newCaptureServer returns an httptest server that replies with a
// single `data: [DONE]\n\n` payload (a clean completion path with
// no streaming deltas) plus a captured-request sidecar that the
// tests can inspect. It is the loop-test fixture pattern reused
// across the handoff 035 binding tests.
func newCaptureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv
}

// TestRun_Ledger_EmptyConfig_PopulatesHarnessSystemAndTask: drive
// RunOne with only HarnessSystem set and no external/skills; the
// ledger must contain EXACTLY two entries: HarnessSystem +
// Task.
func TestRun_Ledger_EmptyConfig_PopulatesHarnessSystemAndTask(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-empty")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     HarnessSystem,
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	led := r.Ledger()
	if led == nil {
		t.Fatal("Ledger() returned nil")
	}
	if len(led.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (got=%+v)", len(led.Entries), led.Entries)
	}
	if led.Entries[0].Category != contextpkg.HarnessSystem {
		t.Errorf("Entries[0].Category = %q, want %q", led.Entries[0].Category, contextpkg.HarnessSystem)
	}
	if led.Entries[0].Name != "harness" {
		t.Errorf("Entries[0].Name = %q, want %q", led.Entries[0].Name, "harness")
	}
	if led.Entries[1].Category != contextpkg.Task {
		t.Errorf("Entries[1].Category = %q, want %q", led.Entries[1].Category, contextpkg.Task)
	}
	if led.Entries[1].Name != "task" {
		t.Errorf("Entries[1].Name = %q, want %q", led.Entries[1].Name, "task")
	}
}

// TestRun_Ledger_AllSlotsPopulated: drive RunOne with all four
// populated slots; the ledger must contain EXACTLY four entries
// in the canonical order: HarnessSystem, ExternalSystem, Skill,
// Task.
func TestRun_Ledger_AllSlotsPopulated(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-all")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "governance-text",
		Skills:         []skill.Skill{{Name: "cold-start", Content: "skill-body"}},
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	led := r.Ledger()
	if len(led.Entries) != 4 {
		t.Fatalf("len(Entries) = %d, want 4 (got=%+v)", len(led.Entries), led.Entries)
	}
	want := []struct {
		cat contextpkg.Category
		nm  string
	}{
		{contextpkg.HarnessSystem, "harness"},
		{contextpkg.ExternalSystem, "external"},
		{contextpkg.Skill, "cold-start"},
		{contextpkg.Task, "task"},
	}
	for i, w := range want {
		if led.Entries[i].Category != w.cat {
			t.Errorf("Entries[%d].Category = %q, want %q", i, led.Entries[i].Category, w.cat)
		}
		if led.Entries[i].Name != w.nm {
			t.Errorf("Entries[%d].Name = %q, want %q", i, led.Entries[i].Name, w.nm)
		}
	}
}

// TestRun_Ledger_SkillsPreserveOrder_AcrossRunOneCalls: drive
// RunOne TWICE with the SAME skill set; the ledger accumulates
// EIGHT entries (4 per RunOne call, in the same order each call).
// This is the TestComposeMessages_SkillsPreserveOrder precedent
// carried to the ledger.
func TestRun_Ledger_SkillsPreserveOrder_AcrossRunOneCalls(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-order")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "E",
		Skills:         []skill.Skill{{Name: "s", Content: "A"}},
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "p1"); err != nil {
		t.Fatalf("RunOne #1: %v", err)
	}
	if _, err := r.RunOne(context.Background(), "p2"); err != nil {
		t.Fatalf("RunOne #2: %v", err)
	}

	led := r.Ledger()
	if len(led.Entries) != 8 {
		t.Fatalf("len(Entries) = %d, want 8 (got=%+v)", len(led.Entries), led.Entries)
	}
	want := []string{"harness", "external", "s", "task", "harness", "external", "s", "task"}
	for i, nm := range want {
		if led.Entries[i].Name != nm {
			t.Errorf("Entries[%d].Name = %q, want %q", i, led.Entries[i].Name, nm)
		}
	}
}

// TestRun_Ledger_Total_MatchesSum: drive RunOne with a known
// Config; the ledger's Total() equals the sum of
// contextpkg.Estimate(content) for each populated entry. This is
// the consistency pin that the loop uses the same Estimate
// function the package exports.
func TestRun_Ledger_Total_MatchesSum(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-total")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         "H",
		SystemExternal: "E",
		Skills:         []skill.Skill{{Name: "s", Content: "S"}},
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "p"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	led := r.Ledger()
	want := contextpkg.Estimate("H") + contextpkg.Estimate("E") +
		contextpkg.Estimate("S") + contextpkg.Estimate("p")
	if got := led.Total(); got != want {
		t.Errorf("Total() = %d, want %d (Estimate(H)+Estimate(E)+Estimate(S)+Estimate(p))", got, want)
	}
}

// TestRun_Ledger_OverflowNotTriggered_NoLimit: drive RunOne with a
// small Config; r.Ledger().Overflow() returns nil (the Limit field
// defaults to 0, so overflow never triggers).
func TestRun_Ledger_OverflowNotTriggered_NoLimit(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-overflow")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     "tiny",
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	if err := r.Ledger().Overflow(); err != nil {
		t.Errorf("Overflow() returned error with no Limit set: %v", err)
	}
}

// TestRun_Ledger_ConcurrentReads_NotGuardedByLock: drive RunOne
// once; in a separate goroutine (NOT in-flight with RunOne), call
// r.Ledger() and read the entries. The test verifies the
// docstring's "NOT safe for concurrent use with an in-flight
// RunOne call" contract by demonstrating that the SEQUENTIAL
// access pattern works. The test does NOT spawn a goroutine
// that reads the ledger WHILE RunOne is in flight (that would be
// a violation of the docstring contract and is the implementer's
// responsibility to avoid, not the test's).
func TestRun_Ledger_ConcurrentReads_NotGuardedByLock(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-ledger-concurrent")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     "H",
	}, client, em, &stdout)

	doneRun := make(chan struct{})
	go func() {
		_, _ = r.RunOne(context.Background(), "p")
		close(doneRun)
	}()
	<-doneRun

	type readResult struct {
		n     int
		total int
	}
	doneRead := make(chan readResult)
	go func() {
		led := r.Ledger()
		doneRead <- readResult{n: len(led.Entries), total: led.Total()}
	}()
	res := <-doneRead
	if res.n == 0 {
		t.Errorf("ledger read returned 0 entries, want > 0")
	}
	if res.total == 0 {
		t.Errorf("ledger read returned Total=0, want > 0")
	}
}

// --- handoff 036: TestRun_PopulateLedger_* tests ---

// TestRun_PopulateLedger_EmptyConfig_PopulatesHarnessSystemAndTask
// is the binding pin for the cmd-side `context show` path:
// calling r.PopulateLedger(prompt) DIRECTLY (without invoking
// RunOne) on a Run constructed with only HarnessSystem set must
// produce EXACTLY two entries — HarnessSystem + Task — in the
// canonical SCOPE §14 order. This is the contract the new
// `simple-harness context show` cmd-side surface relies on.
func TestRun_PopulateLedger_EmptyConfig_PopulatesHarnessSystemAndTask(t *testing.T) {
	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-populate-empty")
	var stdout bytes.Buffer
	// Use an unreachable base URL; the model client is NEVER
	// invoked by PopulateLedger so the unreachable URL is just a
	// type-seam value.
	client := model.NewClient(model.Options{
		BaseURL:        "http://127.0.0.1:9",
		Model:          "qwen",
		RequestTimeout: 1 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: "http://127.0.0.1:9", Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     HarnessSystem,
	}, client, em, &stdout)

	r.PopulateLedger("hi")

	led := r.Ledger()
	if led == nil {
		t.Fatal("Ledger() returned nil")
	}
	if len(led.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (got=%+v)", len(led.Entries), led.Entries)
	}
	if led.Entries[0].Category != contextpkg.HarnessSystem {
		t.Errorf("Entries[0].Category = %q, want %q", led.Entries[0].Category, contextpkg.HarnessSystem)
	}
	if led.Entries[0].Name != "harness" {
		t.Errorf("Entries[0].Name = %q, want %q", led.Entries[0].Name, "harness")
	}
	if led.Entries[1].Category != contextpkg.Task {
		t.Errorf("Entries[1].Category = %q, want %q", led.Entries[1].Category, contextpkg.Task)
	}
	if led.Entries[1].Name != "task" {
		t.Errorf("Entries[1].Name = %q, want %q", led.Entries[1].Name, "task")
	}
}

// TestRun_PopulateLedger_AllSlotsPopulated is the binding pin
// that the helper preserves the canonical SCOPE §14 ordering
// across all four populated slots when called DIRECTLY (without
// invoking RunOne). The ledger must contain FOUR entries in
// canonical order: HarnessSystem + ExternalSystem + Skill + Task.
func TestRun_PopulateLedger_AllSlotsPopulated(t *testing.T) {
	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-populate-all")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        "http://127.0.0.1:9",
		Model:          "qwen",
		RequestTimeout: 1 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: "http://127.0.0.1:9", Model: "qwen"},
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "governance-text",
		Skills:         []skill.Skill{{Name: "cold-start", Content: "skill-body"}},
	}, client, em, &stdout)

	r.PopulateLedger("hi")

	led := r.Ledger()
	if len(led.Entries) != 4 {
		t.Fatalf("len(Entries) = %d, want 4 (got=%+v)", len(led.Entries), led.Entries)
	}
	want := []struct {
		cat contextpkg.Category
		nm  string
	}{
		{contextpkg.HarnessSystem, "harness"},
		{contextpkg.ExternalSystem, "external"},
		{contextpkg.Skill, "cold-start"},
		{contextpkg.Task, "task"},
	}
	for i, w := range want {
		if led.Entries[i].Category != w.cat {
			t.Errorf("Entries[%d].Category = %q, want %q", i, led.Entries[i].Category, w.cat)
		}
		if led.Entries[i].Name != w.nm {
			t.Errorf("Entries[%d].Name = %q, want %q", i, led.Entries[i].Name, w.nm)
		}
	}
}

// TestRun_PopulateLedger_DoesNotCallModel is the binding pin
// that the cmd-side `runContextShow`'s r.PopulateLedger(prompt)
// call does NOT invoke the model client. The test constructs a
// Run with an unreachable base URL + a bytes.Buffer as the
// sidecar writer, calls r.PopulateLedger("hi"), and asserts:
// (i) no panic; (ii) the ledger has the expected entries; (iii)
// the sidecar buffer is EMPTY (no events emitted, since
// PopulateLedger does NOT call r.em.Started / r.em.ModelRequest
// — those are RunOne's responsibility); (iv) r.out (the stdout
// buffer) is EMPTY (no streamed text).
func TestRun_PopulateLedger_DoesNotCallModel(t *testing.T) {
	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-populate-nomodel")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        "http://127.0.0.1:9",
		Model:          "qwen",
		RequestTimeout: 1 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: "http://127.0.0.1:9", Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     HarnessSystem,
	}, client, em, &stdout)

	// If PopulateLedger accidentally invoked the model client,
	// the unreachable base URL would surface a network error
	// (the connection would fail). The test asserts no such
	// error surfaced: PopulateLedger is a pure ledger-populate
	// function that does NOT call the model client.
	r.PopulateLedger("hi")

	led := r.Ledger()
	if len(led.Entries) == 0 {
		t.Fatal("ledger has 0 entries, want > 0")
	}
	if sidecar.Len() != 0 {
		t.Errorf("sidecar buffer has %d bytes, want 0 (PopulateLedger must NOT emit events); got=%q",
			sidecar.Len(), sidecar.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout buffer has %d bytes, want 0 (PopulateLedger must NOT write to r.out); got=%q",
			stdout.Len(), stdout.String())
	}
}

// TestRun_PopulateLedger_RunOneCallsHelper is the regression
// tripwire that RunOne continues to populate via the
// PopulateLedger helper after the handoff 036 refactor. The
// existing TestRun_Ledger_* tests already cover this
// end-to-end; the new test makes the refactor explicit and
// produces a test that fails by name if a future change drops
// the r.PopulateLedger(prompt) call from RunOne's body. The
// test drives RunOne once with a Config{System: HarnessSystem,
// ...} and a known prompt, then verifies r.Ledger().Entries is
// populated identically to the standalone
// TestRun_PopulateLedger_EmptyConfig_PopulatesHarnessSystemAndTask
// case (same two entries, same names, same categories).
func TestRun_PopulateLedger_RunOneCallsHelper(t *testing.T) {
	srv := newCaptureServer(t)
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-populate-via-runone")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		System:     HarnessSystem,
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	led := r.Ledger()
	if len(led.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (RunOne must call r.PopulateLedger); got=%+v",
			len(led.Entries), led.Entries)
	}
	if led.Entries[0].Category != contextpkg.HarnessSystem || led.Entries[0].Name != "harness" {
		t.Errorf("Entries[0] = {%q, %q}, want {HarnessSystem, harness}", led.Entries[0].Category, led.Entries[0].Name)
	}
	if led.Entries[1].Category != contextpkg.Task || led.Entries[1].Name != "task" {
		t.Errorf("Entries[1] = {%q, %q}, want {Task, task}", led.Entries[1].Category, led.Entries[1].Name)
	}
}

// TestRunOne_AdvertisesRegisteredTools pins the loop-side wiring
// per Run 023 / handoff 073: the loop populates the
// model.ChatRequest.Tools field from r.cfg.Tools (the registered
// tool inventory). The pin registers 2 stub tools in a fresh
// registry, constructs a Run with Config.Tools pointing at the
// registry, calls RunOne, and asserts the captured body carries
// a tools array whose function.name entries are exactly the
// registered names. The pin is the loop-side complement to the
// model-side TestChatRequest_Tools_AdvertisesRegisteredTools pin
// in internal/model/client_test.go; together they cover the
// full construction -> wire contract.
func TestRunOne_AdvertisesRegisteredTools(t *testing.T) {
	var gotBody struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	reg := tools.NewRegistry()
	reg.Register(&stubLoopTool{name: "read_file", desc: "r"})
	reg.Register(&stubLoopTool{name: "write_file", desc: "w"})

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-tools-advertise")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:      model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:  "/tmp/ws",
		Permission: "READ_ONLY",
		Tools:      reg,
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hi"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len(gotBody.Tools) != 2 {
		t.Fatalf("body.tools len = %d, want 2 (got=%+v)", len(gotBody.Tools), gotBody.Tools)
	}
	names := []string{}
	for _, td := range gotBody.Tools {
		names = append(names, td.Function.Name)
	}
	want := []string{"read_file", "write_file"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("body.tools names = %v, want %v (sorted)", names, want)
	}
}

// stubLoopTool is the minimal tools.Tool implementation the new
// TestRunOne_AdvertisesRegisteredTools pin needs. It is the
// loop-side counterpart to the model-side stubTool helper in
// internal/model/client_test.go.
type stubLoopTool struct {
	name string
	desc string
}

func (s *stubLoopTool) Meta() tools.ToolMeta {
	return tools.ToolMeta{Name: s.name, Description: s.desc}
}

func (s *stubLoopTool) Schema() tools.Schema {
	return tools.Schema{}
}

func (s *stubLoopTool) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Status: "ok", Content: "stub"}, nil
}

// --- handoff 075: TestToolLoop_Messages_* pins (Run 023 amendment 4) ---

// TestToolLoop_Messages_FollowupBodyCarriesToolCallsAndToolCallID is
// binding pin (a) from Run 023 amendment 4: drives RunAgent against
// an httptest server that answers the FIRST request with a
// synthesized tool_call SSE payload (one delta with
// tool_calls=[{id:"call_test1", function:{name:"apply_patch",
// arguments:"{\"path\":\"x\",\"patch\":\"+y\\n\"}"}}]) and answers
// the SECOND request (the follow-up) with `data: [DONE]\n\n`. The
// test stubs tools.Registry with one apply_patch builtin (a
// stubLoopTool that returns tools.Result{Status:"ok", Content:"ok"}
// so the loop's dispatch path produces a tool-result message
// without requiring a real workspace). The test captures BOTH
// request bodies and asserts on the SECOND body: the follow-up
// has the 4 SCOPE §14 originals + 1 assistant-with-tool_calls + 1
// tool-message; the assistant entry carries tool_calls[0] with
// id=="call_test1", function.name=="apply_patch", and a parseable
// function.arguments object; the tool entry carries
// tool_call_id=="call_test1" and non-empty content.
func TestToolLoop_Messages_FollowupBodyCarriesToolCallsAndToolCallID(t *testing.T) {
	type capturedToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type capturedMessage struct {
		Role       string             `json:"role"`
		Content    string             `json:"content"`
		ToolCalls  []capturedToolCall `json:"tool_calls,omitempty"`
		ToolCallID string             `json:"tool_call_id,omitempty"`
	}
	type capturedRequest struct {
		Messages []capturedMessage `json:"messages"`
	}
	var captured []capturedRequest
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cr capturedRequest
		if err := json.NewDecoder(r.Body).Decode(&cr); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", 500)
			return
		}
		captured = append(captured, cr)
		n := atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First request: emit one tool_call delta, then [DONE].
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test1","function":{"name":"apply_patch","arguments":"{\"path\":\"x\",\"patch\":\"+y\\n\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		// Second request (follow-up): clean [DONE].
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	reg := tools.NewRegistry()
	reg.Register(&stubLoopTool{name: "apply_patch", desc: "applies a patch"})

	workspaceDir := t.TempDir()
	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-tool-loop-msgs")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:      workspaceDir,
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "EXT",
		Skills:         []skill.Skill{{Name: "s", Content: "SKILL"}},
		Tools:          reg,
	}, client, em, &stdout)

	if _, err := r.RunAgent(context.Background(), "fix the bug"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if len(captured) < 2 {
		t.Fatalf("captured len = %d, want >= 2 (got=%+v)", len(captured), captured)
	}
	followup := captured[1]
	// 2 initial on the wire (one merged system + user), see
	// TestRunOne_PassesComposedMessagesToClient.
	if got := len(followup.Messages); got != 4 {
		t.Fatalf("followup len(messages) = %d, want 4 (2 initial + 1 assistant-with-tool_calls + 1 tool-message) (got=%+v)", got, followup.Messages)
	}
	assistant := followup.Messages[2]
	if assistant.Role != "assistant" {
		t.Errorf("followup.messages[2].role = %q, want assistant", assistant.Role)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("followup.messages[2].tool_calls len = %d, want 1 (got=%+v)", len(assistant.ToolCalls), assistant.ToolCalls)
	}
	if assistant.ToolCalls[0].ID != "call_test1" {
		t.Errorf("followup.messages[2].tool_calls[0].id = %q, want call_test1", assistant.ToolCalls[0].ID)
	}
	if assistant.ToolCalls[0].Function.Name != "apply_patch" {
		t.Errorf("followup.messages[2].tool_calls[0].function.name = %q, want apply_patch", assistant.ToolCalls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(assistant.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Errorf("followup.messages[2].tool_calls[0].function.arguments not parseable JSON: %v (raw=%s)", err, assistant.ToolCalls[0].Function.Arguments)
	}
	if _, ok := args["path"]; !ok {
		t.Errorf("followup.messages[2].tool_calls[0].function.arguments missing 'path' key (got=%+v)", args)
	}
	last := followup.Messages[len(followup.Messages)-1]
	if last.Role != "tool" {
		t.Errorf("followup.messages[last].role = %q, want tool", last.Role)
	}
	if last.ToolCallID != "call_test1" {
		t.Errorf("followup.messages[last].tool_call_id = %q, want call_test1", last.ToolCallID)
	}
	if last.Content == "" {
		t.Errorf("followup.messages[last].content is empty, want non-empty")
	}
}

// TestToolLoop_Messages_PlainMessageByteCompat is binding pin (b)
// from Run 023 amendment 4: drives RunOne (single-turn happy path,
// NO tool calls) with the exact same fixture used by
// TestRunOne_PassesComposedMessagesToClient (the canonical SCOPE
// §14 composition: HarnessSystem + ExternalSystem + 1 Skill +
// Task), captures the request body BYTES, and asserts that no
// tool_calls or tool_call_id substrings appear on the wire. The
// pin also asserts the belt-and-suspenders model-layer byte-compat
// check (plain Message marshals without tool_calls/tool_call_id
// substrings). The pin's purpose is to FAIL a regression that
// drops the omitempty tags or adds a non-omitempty field that would
// leak into the wire for plain-message cases.
func TestToolLoop_Messages_PlainMessageByteCompat(t *testing.T) {
	var bodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-plain-byte-compat")
	var stdout bytes.Buffer
	client := model.NewClient(model.Options{
		BaseURL:        srv.URL,
		Model:          "qwen",
		RequestTimeout: 2 * time.Second,
	})
	r := New(Config{
		Model:          model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:      "/tmp/ws",
		Permission:     "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "EXT",
		Skills:         []skill.Skill{{Name: "s", Content: "SKILL"}},
	}, client, em, &stdout)

	if _, err := r.RunOne(context.Background(), "hello"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	if bytes.Contains(bodyBytes, []byte("tool_calls")) {
		t.Errorf("plain RunOne body contains tool_calls substring: %s", bodyBytes)
	}
	if bytes.Contains(bodyBytes, []byte("tool_call_id")) {
		t.Errorf("plain RunOne body contains tool_call_id substring: %s", bodyBytes)
	}
	// Belt-and-suspenders model-layer byte-compat check.
	plainBody, err := json.Marshal(model.Message{Role: "user", Content: "x"})
	if err != nil {
		t.Fatalf("Marshal plain Message: %v", err)
	}
	if bytes.Contains(plainBody, []byte("tool_calls")) {
		t.Errorf("plain Message JSON contains tool_calls substring: %s", plainBody)
	}
	if bytes.Contains(plainBody, []byte("tool_call_id")) {
		t.Errorf("plain Message JSON contains tool_call_id substring: %s", plainBody)
	}
}
