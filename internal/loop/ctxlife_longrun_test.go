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
	"testing"
	"time"

	contextpkg "github.com/svend-blip/simple-harness/internal/context"
	"github.com/svend-blip/simple-harness/internal/ctxlife"
	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// echoTool returns a large result, so a long run accumulates context
// the way a real one does: most of what grows is tool output.
type echoTool struct{ size int }

func (e *echoTool) Meta() tools.ToolMeta {
	return tools.ToolMeta{Name: "echo", Description: "returns a large block of text"}
}
func (e *echoTool) Schema() tools.Schema {
	return tools.Schema{Properties: map[string]tools.PropertyType{"n": "string"}}
}
func (e *echoTool) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{Status: "ok", Content: strings.Repeat("abc ", e.size)}, nil
}

// toolCallingServer answers with a tool call for the first `turns`
// requests and with plain text after that, recording the prompt size
// of every request it received.
func toolCallingServer(t *testing.T, turns int) (*httptest.Server, func() []int) {
	t.Helper()
	var mu sync.Mutex
	var sizes []int
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		var req model.ChatRequest
		if err := json.Unmarshal(body.Bytes(), &req); err != nil {
			t.Errorf("unparseable request: %v", err)
		}
		total := 0
		for _, msg := range req.Messages {
			total += ctxlife.MessageTokens(msg)
		}
		mu.Lock()
		sizes = append(sizes, total)
		n++
		turn := n
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if turn <= turns {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,`+
				`"id":"call-%d","type":"function","function":{"name":"echo",`+
				`"arguments":"{\"n\":\"%d\"}"}}]}}]}`+"\n\n", turn, turn)
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"finished"}}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv, func() []int {
		mu.Lock()
		defer mu.Unlock()
		out := make([]int, len(sizes))
		copy(out, sizes)
		return out
	}
}

func longRun(t *testing.T, policy ContextPolicy, turns int) (*Run, []int, error) {
	t.Helper()
	srv, sizes := toolCallingServer(t, turns)
	t.Cleanup(srv.Close)

	reg := tools.NewRegistry()
	reg.Register(&echoTool{size: 900})
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen",
		RequestTimeout: 10 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:         model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:     t.TempDir(),
		Permission:    "READ_ONLY",
		System:        HarnessSystem,
		Tools:         reg,
		MaxTurns:      turns + 2,
		ContextPolicy: policy,
	}, client, event.NewEmitter(&sidecar, "long"), &stdout)
	_, err := r.RunAgent(context.Background(), "do a long piece of work")
	return r, sizes(), err
}

// §24.12 / §26.3 / §26.12: a substantially longer run completes, and
// the context it sends does not grow without bound.
func TestALongRunStaysInsideItsBudget(t *testing.T) {
	const limit = 24576
	r, sizes, err := longRun(t, ContextPolicy{ModelLimit: limit, KeepRecentTurns: 6}, 40)
	if err != nil {
		t.Fatalf("a 40-turn run failed: %v", err)
	}
	if len(sizes) < 40 {
		t.Fatalf("only %d requests were made", len(sizes))
	}

	budget := ctxlife.DefaultBudget(limit).Active()
	for i, n := range sizes {
		if n > budget {
			t.Fatalf("request %d sent %d tokens against a %d budget", i+1, n, budget)
		}
	}

	m := r.ContextManager()
	if m.Stats.ToolResultsPruned == 0 {
		t.Fatal("a 40-turn run with 900-token results pruned nothing; the fixture " +
			"is not long enough to prove anything")
	}
	if m.Stats.PeakActiveTokens > budget {
		t.Fatalf("peak active context %d exceeded the budget %d",
			m.Stats.PeakActiveTokens, budget)
	}
}

// The comparison the addendum is for: without the lifecycle the same
// run grows past the same limit.
func TestTheSameRunWithoutTheLifecycleGrowsPastTheLimit(t *testing.T) {
	const limit = 24576
	_, sizes, err := longRun(t, ContextPolicy{Disabled: true}, 40)
	if err != nil {
		t.Fatalf("the unbounded run failed for another reason: %v", err)
	}
	budget := ctxlife.DefaultBudget(limit).Active()
	var over int
	for _, n := range sizes {
		if n > budget {
			over++
		}
	}
	if over == 0 {
		t.Fatalf("the unbounded run never exceeded %d tokens, so the bounded "+
			"comparison proves nothing; lengthen the fixture", budget)
	}
	if sizes[len(sizes)-1] <= sizes[0] {
		t.Fatal("the unbounded run did not grow, so this is not measuring growth")
	}
}

// §3 / §20 / §26.4: reduction does not destroy what the harness has
// accumulated — only what it sends.
func TestTheDurableLedgerIsUntouchedByReduction(t *testing.T) {
	r, _, err := longRun(t, ContextPolicy{ModelLimit: 24576, KeepRecentTurns: 6}, 30)
	if err != nil {
		t.Fatal(err)
	}
	if r.ContextManager().Stats.ToolResultsPruned == 0 {
		t.Fatal("nothing was pruned, so this proves nothing")
	}
	total := r.Ledger().Total()
	if total == 0 {
		t.Fatal("the accounting ledger is empty after a long run")
	}
	var sawHarness bool
	for _, e := range r.Ledger().Entries {
		if e.Category == contextpkg.HarnessSystem {
			sawHarness = true
		}
	}
	if !sawHarness {
		t.Fatal("reduction removed an entry from the durable accounting ledger")
	}
}

// §8 / §26.6: the instructions survive a run long enough to need
// every reduction the policy has.
func TestPinnedInstructionsSurviveALongRun(t *testing.T) {
	srv, _ := toolCallingServer(t, 40)
	defer srv.Close()
	reg := tools.NewRegistry()
	reg.Register(&echoTool{size: 900})

	var seenSystem []string
	inspect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		var req model.ChatRequest
		_ = json.Unmarshal(body.Bytes(), &req)
		var found string
		for _, msg := range req.Messages {
			if msg.Role == "system" && strings.Contains(msg.Content, "GOVERNANCE MARKER") {
				found = msg.Content
			}
		}
		seenSystem = append(seenSystem, found)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(seenSystem) <= 30 {
			fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,`+
				`"id":"c-%d","type":"function","function":{"name":"echo",`+
				`"arguments":"{\"n\":\"1\"}"}}]}}]}`+"\n\n", len(seenSystem))
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"}}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer inspect.Close()

	client := model.NewClient(model.Options{BaseURL: inspect.URL, Model: "qwen",
		RequestTimeout: 10 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:     model.Options{BaseURL: inspect.URL, Model: "qwen"},
		Workspace: t.TempDir(), Permission: "READ_ONLY",
		System:         HarnessSystem,
		SystemExternal: "GOVERNANCE MARKER: this must survive every reduction",
		Tools:          reg, MaxTurns: 40,
		ContextPolicy: ContextPolicy{ModelLimit: 24576, KeepRecentTurns: 4},
	}, client, event.NewEmitter(&sidecar, "pin"), &stdout)
	if _, err := r.RunAgent(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if r.ContextManager().Stats.Reductions == 0 {
		t.Fatal("no reduction happened, so survival proves nothing")
	}
	for i, got := range seenSystem {
		if got == "" {
			t.Fatalf("request %d of %d went out without the governance marker",
				i+1, len(seenSystem))
		}
	}
}
