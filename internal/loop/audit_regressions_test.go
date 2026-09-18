package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/ctxlife"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/mcp"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/path"
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

// TestRunAgent_DoesNotRedoReductionsEveryTurn — Fit was given the raw
// history each turn, so once over budget every turn re-pruned and
// re-compacted from scratch: one extra inference per turn, and stats
// that double-counted. The loop now fits the previously fitted view
// plus the new messages.
func TestRunAgent_DoesNotRedoReductionsEveryTurn(t *testing.T) {
	var compactions int32
	srv, _ := toolCallingServerWithCompactionCount(t, 12, &compactions)
	reg := tools.NewRegistry()
	reg.Register(&echoTool{size: 900})
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 10 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:         model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:     t.TempDir(),
		Permission:    "READ_ONLY",
		System:        HarnessSystem,
		Tools:         reg,
		MaxTurns:      20,
		ContextPolicy: ContextPolicy{ModelLimit: 8192, KeepRecentTurns: 4},
	}, client, event.NewEmitter(&sidecar, "memo"), &stdout)
	if _, err := r.RunAgent(context.Background(), "work"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	st := r.ContextManager().Stats
	if st.Reductions == 0 {
		t.Fatal("nothing was reduced; the fixture proves nothing")
	}
	if n := atomic.LoadInt32(&compactions); n > 2 {
		t.Errorf("%d compaction requests over 12 tool turns; the fitted view should carry forward", n)
	}
	if st.ToolResultsPruned > 12 {
		t.Errorf("ToolResultsPruned = %d with only 12 tool results ever produced", st.ToolResultsPruned)
	}
}

// toolCallingServerWithCompactionCount is toolCallingServer, also
// counting the requests that carry the compaction instruction.
func toolCallingServerWithCompactionCount(t *testing.T, turns int, compactions *int32) (*httptest.Server, func() []int) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []model.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "You are compacting") {
			atomic.AddInt32(compactions, 1)
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"compact summary"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		turn := atomic.AddInt32(&n, 1)
		if int(turn) <= turns {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-%d","function":{"name":"echo","arguments":"{\"n\":\"%d\"}"}}]}}]}`+"\n\n", turn, turn)
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"finished"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, nil
}

// TestPopulateLedger_ReplacesThePreviousComposition — the ledger
// accumulated across prompts, so in an interactive session /context
// listed the harness system N times after N prompts and --limit
// tripped on the sum of every prompt's composition.
func TestPopulateLedger_ReplacesThePreviousComposition(t *testing.T) {
	srv, _ := twoTurnServer(t, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
	r, _ := newAgentRun(t, srv, Config{System: HarnessSystem})
	r.PopulateLedger("first")
	r.PopulateLedger("second")
	led := r.Ledger()
	var tasks, harness int
	for _, e := range led.Entries {
		switch e.Name {
		case "task":
			tasks++
			if e.Content != "second" {
				t.Errorf("task entry = %q, want the current prompt", e.Content)
			}
		case "harness":
			harness++
		}
	}
	if tasks != 1 || harness != 1 {
		t.Errorf("entries after two compositions: %d task, %d harness; want one of each", tasks, harness)
	}
}

// TestPopulateLedger_AccountsTheToolSurface — tool schemas are part
// of every request, but the ledger never carried them, so `context
// show` reported 0 tokens of tool schemas and the doctor's schema
// finding could never fire.
func TestPopulateLedger_AccountsTheToolSurface(t *testing.T) {
	srv, _ := twoTurnServer(t, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
	r, _ := newAgentRun(t, srv, Config{System: HarnessSystem})
	r.PopulateLedger("p")
	var schemaTokens int
	for _, e := range r.Ledger().Entries {
		if e.Category == "tool schemas" {
			schemaTokens += e.TokenEstimate
		}
	}
	if schemaTokens == 0 {
		t.Error("no tool-schema entries in the ledger although a tool is registered")
	}
	if r.ContextManager() != nil && r.ContextManager().ToolSchemaTokens == 0 {
		t.Error("the lifecycle manager was not told what the tool surface costs")
	}
}

// bigResultTransport is an MCP server whose tool returns a large
// result on every call and records what it was asked.
type bigResultTransport struct {
	mu    sync.Mutex
	calls []map[string]interface{}
}

func (b *bigResultTransport) List(context.Context) ([]mcp.ListedTool, error) {
	return []mcp.ListedTool{{Name: "scope_state", Description: "authoritative project state",
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"n": map[string]interface{}{"type": "string"}}}}}, nil
}
func (b *bigResultTransport) Call(_ context.Context, _ string, args map[string]interface{}) (map[string]interface{}, error) {
	b.mu.Lock()
	b.calls = append(b.calls, args)
	b.mu.Unlock()
	return map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text",
		"text": "STATE-" + fmt.Sprint(args["n"]) + " " + strings.Repeat("abc ", 900)}}}, nil
}
func (b *bigResultTransport) Close() error { return nil }

type allowPolicy struct{}

func (allowPolicy) Decide(context.Context, tools.Call, tools.Workspace) tools.Decision {
	return tools.Decision{Allowed: true}
}

// TestMCPToolsKeepWorkingAfterPruningAndCompaction — addendum §24.10
// and §24.11: an external MCP tool (scope-mcp stands in for any
// stateful server) keeps being called with the model's arguments
// after tool results have been pruned and conversation compacted,
// and the durable record still holds every full result — the
// placeholders exist only in the active context.
func TestMCPToolsKeepWorkingAfterPruningAndCompaction(t *testing.T) {
	const turns = 12
	var compactions int32
	srv, _ := toolCallingServerWithCompactionCount(t, turns, &compactions)
	// The server names the tool "echo"; the registry must offer it
	// under that name, so the MCP server lists it as "echo" too.
	tr := &bigResultTransport{}
	reg := tools.NewRegistry()
	ws, err := path.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := mcp.NewManager(reg, func(context.Context, tools.Call, tools.Schema, tools.Workspace, tools.Policy) *tools.DecisionError {
		return nil
	}, allowPolicy{}, ws)
	if err := mgr.AddServer(context.Background(), mcp.Server{Name: "scope-mcp"}, renamed{tr}); err != nil {
		t.Fatal(err)
	}
	var durable []model.Message
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 10 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:         model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:     ws.Root(),
		Permission:    "READ_ONLY",
		System:        HarnessSystem,
		Tools:         reg,
		MaxTurns:      turns + 2,
		ContextPolicy: ContextPolicy{ModelLimit: 8192, KeepRecentTurns: 4},
		OnMessage:     func(m model.Message) { durable = append(durable, m) },
	}, client, event.NewEmitter(&sidecar, "mcp-reduce"), &stdout)
	if _, err := r.RunAgent(context.Background(), "work"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	st := r.ContextManager().Stats
	if st.ToolResultsPruned == 0 {
		t.Fatal("nothing was pruned; the fixture proves nothing")
	}
	tr.mu.Lock()
	n := len(tr.calls)
	last := tr.calls[len(tr.calls)-1]
	tr.mu.Unlock()
	if n != turns {
		t.Errorf("MCP tool was called %d times, want %d (every turn, before and after reduction)", n, turns)
	}
	if fmt.Sprint(last["n"]) != fmt.Sprint(turns) {
		t.Errorf("last MCP call args = %v, want the model's argument for turn %d", last, turns)
	}
	// Every full result is in the durable record; no placeholder is.
	full := 0
	for _, m := range durable {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(m.Content, "[Earlier tool result pruned") {
			t.Errorf("a placeholder reached the durable record: %.80s", m.Content)
		}
		if strings.Contains(m.Content, "STATE-") {
			full++
		}
	}
	if full != turns {
		t.Errorf("durable record holds %d full MCP results, want %d", full, turns)
	}
}

// renamed lists the wrapped transport's tool under the name the test
// server calls.
type renamed struct{ inner *bigResultTransport }

func (r renamed) List(ctx context.Context) ([]mcp.ListedTool, error) {
	l, err := r.inner.List(ctx)
	for i := range l {
		l[i].Name = "echo"
	}
	return l, err
}
func (r renamed) Call(ctx context.Context, name string, args map[string]interface{}) (map[string]interface{}, error) {
	return r.inner.Call(ctx, name, args)
}
func (r renamed) Close() error { return nil }

// TestARunWhoseToolResultsExceedTheBudgetStillCompletes — §24.12 with
// results bigger than the budget: every request stays within budget
// and the run completes, instead of failing at the first oversized
// read (the §25 benchmark failed at call 2 on exactly this).
func TestARunWhoseToolResultsExceedTheBudgetStillCompletes(t *testing.T) {
	const limit = 8192
	srv, sizes := toolCallingServer(t, 6)
	t.Cleanup(srv.Close)
	reg := tools.NewRegistry()
	reg.Register(&echoTool{size: 7000})
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen", RequestTimeout: 10 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model: model.Options{BaseURL: srv.URL, Model: "qwen"}, Workspace: t.TempDir(),
		Permission: "READ_ONLY", System: HarnessSystem, Tools: reg, MaxTurns: 8,
		ContextPolicy: ContextPolicy{ModelLimit: limit},
	}, client, event.NewEmitter(&sidecar, "oversized"), &stdout)
	if _, err := r.RunAgent(context.Background(), "read big things"); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	budget := ctxlife.DefaultBudget(limit).Active()
	for i, n := range sizes() {
		if n > budget {
			t.Errorf("request %d sent %d tokens against %d", i+1, n, budget)
		}
	}
	if r.ContextManager().Stats.ToolResultsTruncated == 0 {
		t.Error("no result was truncated; the fixture proves nothing")
	}
}

// wireSchemaTool is a tool that carries a full JSON Schema next to the
// validator's type-only one, as an MCP adapter does.
type wireSchemaTool struct{ *stubLoopTool }

func (wireSchemaTool) WireSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"goals":{"type":"array","items":{"type":"object"}}}}`)
}

// TestToolDefinitionsCarryTheFullSchemaWhenAToolHasOne — the request
// showed the model the validator's type-only rendering even when the
// tool knew its full schema.
func TestToolDefinitionsCarryTheFullSchemaWhenAToolHasOne(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(wireSchemaTool{&stubLoopTool{name: "set_goals"}})
	defs, _ := toolsToChatRequestTools(reg)
	if len(defs) != 1 {
		t.Fatalf("defs = %d", len(defs))
	}
	if !strings.Contains(string(defs[0].Function.Parameters), `"items"`) {
		t.Errorf("parameters = %s", defs[0].Function.Parameters)
	}
}
