package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/skill"
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

	if len(captured.Messages) != 4 {
		t.Fatalf("captured len(messages) = %d, want 4 (got=%+v)", len(captured.Messages), captured.Messages)
	}
	want := []struct{ role, content string }{
		{"system", HarnessSystem},
		{"system", "EXT"},
		{"system", "SKILL"},
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
