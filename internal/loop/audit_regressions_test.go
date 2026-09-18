package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/tools"
)

type wireToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireReq struct {
	Messages []wireMsg              `json:"messages"`
	Tools    []model.ToolDefinition `json:"tools"`
}

// twoTurnServer answers the first request with `first` and every later
// request with a plain final text, recording each request body.
func twoTurnServer(t *testing.T, first string) (*httptest.Server, *[]wireReq) {
	t.Helper()
	var captured []wireReq
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req wireReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		captured = append(captured, req)
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&n, 1) == 1 {
			fmt.Fprint(w, first)
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newAgentRun(t *testing.T, srv *httptest.Server, cfg Config) (*Run, *bytes.Buffer) {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(&stubLoopTool{name: "read_file", desc: "r"})
	cfg.Model = model.Options{BaseURL: srv.URL, Model: "qwen"}
	cfg.Workspace = t.TempDir()
	cfg.Permission = "READ_ONLY"
	cfg.Tools = reg
	var sidecar bytes.Buffer
	em := event.NewEmitter(&sidecar, "sess-audit")
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 2 * time.Second})
	return New(cfg, client, em, &bytes.Buffer{}), &sidecar
}

// TestRunAgent_AssistantTextAlongsideToolCallsIsKeptInHistory — a
// model that narrates before calling a tool ("Reading the file. ")
// lost that text: the assistant message appended to the history
// carried the tool_calls and an empty content, so on the next turn the
// model saw a history in which it had said nothing.
func TestRunAgent_AssistantTextAlongsideToolCallsIsKeptInHistory(t *testing.T) {
	srv, captured := twoTurnServer(t,
		`data: {"choices":[{"delta":{"content":"Reading the file. ","tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]}}]}`+"\n\n")
	r, _ := newAgentRun(t, srv, Config{System: HarnessSystem})
	if _, err := r.RunAgent(context.Background(), "go"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if len(*captured) != 2 {
		t.Fatalf("requests = %d, want 2", len(*captured))
	}
	msgs := (*captured)[1].Messages
	var assistant *wireMsg
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			assistant = &msgs[i]
		}
	}
	if assistant == nil || len(assistant.ToolCalls) != 1 {
		t.Fatalf("no assistant tool_calls message in follow-up: %+v", msgs)
	}
	if assistant.Content != "Reading the file. " {
		t.Errorf("assistant content = %q, want the narrated text", assistant.Content)
	}
}

// TestRunAgent_SynthesizesMissingToolCallIDs — a runtime that omits
// tool-call ids produced an assistant tool_calls entry with id "" and
// a tool message with tool_call_id "", which OpenAI-compatible
// endpoints reject on the follow-up. The harness assigns an id when
// the model did not, and uses it on both sides of the pair.
func TestRunAgent_SynthesizesMissingToolCallIDs(t *testing.T) {
	srv, captured := twoTurnServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{}"}}]}}]}`+"\n\n")
	r, sidecar := newAgentRun(t, srv, Config{})
	if _, err := r.RunAgent(context.Background(), "go"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	msgs := (*captured)[1].Messages
	var id, toolCallID string
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 {
			id = m.ToolCalls[0].ID
		}
		if m.Role == "tool" {
			toolCallID = m.ToolCallID
		}
	}
	if id == "" || toolCallID != id {
		t.Errorf("tool_calls id %q / tool_call_id %q: want one non-empty synthesized id on both", id, toolCallID)
	}
	if !bytes.Contains(sidecar.Bytes(), []byte(`"call_id":"`+id+`"`)) {
		t.Errorf("tool_call event does not carry the synthesized id %q", id)
	}
}

// TestRunAgent_ReportsEveryAppendedMessage — the durable session
// record must be the execution history. The loop reports each message
// it appends after the initial composition, in order: the assistant
// tool_calls message, each tool result, and the final assistant text.
func TestRunAgent_ReportsEveryAppendedMessage(t *testing.T) {
	srv, _ := twoTurnServer(t,
		`data: {"choices":[{"delta":{"content":"look","tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]}}]}`+"\n\n")
	var seen []model.Message
	r, _ := newAgentRun(t, srv, Config{OnMessage: func(m model.Message) { seen = append(seen, m) }})
	text, err := r.RunAgent(context.Background(), "go")
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if text != "lookdone" {
		t.Errorf("returned text = %q, want the concatenated stream", text)
	}
	if len(seen) != 3 {
		t.Fatalf("reported %d messages, want 3: %+v", len(seen), seen)
	}
	if seen[0].Role != "assistant" || len(seen[0].ToolCalls) != 1 || seen[0].Content != "look" {
		t.Errorf("seen[0] = %+v, want assistant with one tool call and its text", seen[0])
	}
	if seen[1].Role != "tool" || seen[1].ToolCallID != "call_1" {
		t.Errorf("seen[1] = %+v, want the tool result for call_1", seen[1])
	}
	if seen[2].Role != "assistant" || seen[2].Content != "done" || len(seen[2].ToolCalls) != 0 {
		t.Errorf("seen[2] = %+v, want the final assistant text alone", seen[2])
	}
}

// TestRunOne_DoesNotAdvertiseToolsItCannotDispatch — RunOne is the
// single-turn call: it never dispatches a tool call. It advertised the
// registry anyway, so a model that answered with a tool call had its
// answer silently ignored and the turn reported COMPLETED with empty
// output.
func TestRunOne_DoesNotAdvertiseToolsItCannotDispatch(t *testing.T) {
	srv, captured := twoTurnServer(t, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
	r, _ := newAgentRun(t, srv, Config{})
	if _, err := r.RunOne(context.Background(), "go"); err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len((*captured)[0].Tools) != 0 {
		t.Errorf("RunOne advertised %d tools; it cannot dispatch any", len((*captured)[0].Tools))
	}
}
