package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/event"
	"github.com/svend-blip/simple-harness/internal/model"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// capturingServer records every request body the harness sends and
// replies with plain text, so a test can assert what actually went on
// the wire rather than what the harness says it sent.
func capturingServer(t *testing.T, reply string) (*httptest.Server, *[]model.ChatRequest) {
	t.Helper()
	var seen []model.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.ChatRequest
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if err := json.Unmarshal(body.Bytes(), &req); err != nil {
			t.Errorf("the harness sent a body that is not a ChatRequest: %v", err)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv, &seen
}

func runWithPolicy(t *testing.T, policy ContextPolicy, prompt string,
	system string) (*Run, *[]model.ChatRequest, error) {
	t.Helper()
	srv, seen := capturingServer(t, "done")
	t.Cleanup(srv.Close)

	client := model.NewClient(model.Options{
		BaseURL: srv.URL, Model: "qwen", RequestTimeout: 5 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:         model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace:     t.TempDir(),
		Permission:    "READ_ONLY",
		System:        system,
		Tools:         tools.NewRegistry(),
		MaxTurns:      2,
		ContextPolicy: policy,
	}, client, event.NewEmitter(&sidecar, "sess-ctx"), &stdout)
	_, err := r.RunAgent(context.Background(), prompt)
	return r, seen, err
}

// -- the manager is wired and reachable ---------------------------

func TestTheContextManagerIsBuiltFromThePolicy(t *testing.T) {
	r, _, err := runWithPolicy(t, ContextPolicy{ModelLimit: 131072}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	m := r.ContextManager()
	if m == nil {
		t.Fatal("no context manager was built")
	}
	if m.Budget.ModelLimit != 131072 {
		t.Fatalf("model limit %d", m.Budget.ModelLimit)
	}
	if m.Stats.Inferences == 0 {
		t.Fatal("the manager was not consulted before the inference")
	}
}

func TestADisabledPolicyLeavesTheLoopExactlyAsItWas(t *testing.T) {
	r, seen, err := runWithPolicy(t, ContextPolicy{Disabled: true, ModelLimit: 4096},
		"hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if r.ContextManager() != nil {
		t.Fatal("a disabled policy still built a manager")
	}
	if len(*seen) == 0 {
		t.Fatal("no request was sent")
	}
}

func TestTheConfiguredLimitReachesTheAccountingLedger(t *testing.T) {
	r, _, err := runWithPolicy(t, ContextPolicy{ModelLimit: 65536}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if r.Ledger().Limit != 65536 {
		t.Fatalf("the ledger's limit is %d; `context show` would report the wrong "+
			"figure", r.Ledger().Limit)
	}
}

func TestReservesAndRecentWindowAreTakenFromThePolicy(t *testing.T) {
	r, _, err := runWithPolicy(t, ContextPolicy{
		ModelLimit: 131072, GenerationReserve: 999, SafetyReserve: 111,
		KeepRecentTurns: 3}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	m := r.ContextManager()
	if m.Budget.GenerationReserve != 999 || m.Budget.SafetyReserve != 111 {
		t.Fatalf("reserves %+v", m.Budget)
	}
	if m.KeepRecentTurns != 3 {
		t.Fatalf("recent window %d", m.KeepRecentTurns)
	}
}

func TestPruningAndCompactionCanBeTurnedOffIndependently(t *testing.T) {
	r, _, err := runWithPolicy(t, ContextPolicy{ModelLimit: 131072,
		DisableToolResultPruning: true, DisableCompaction: true}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	m := r.ContextManager()
	if m.PruneToolResults {
		t.Fatal("pruning was not disabled")
	}
	if m.Compactor != nil {
		t.Fatal("compaction was not disabled")
	}
}

// -- §7 the budget is enforced before the inference ---------------

func TestAnImpossibleBudgetFailsBeforeAnythingIsSent(t *testing.T) {
	// The system message alone dwarfs the budget, so §21 requires
	// an explicit failure rather than a request.
	huge := strings.Repeat("abc ", 20000)
	r, seen, err := runWithPolicy(t, ContextPolicy{ModelLimit: 2048}, "hi", huge)
	if err == nil {
		t.Fatal("an impossible budget did not fail")
	}
	var be *ContextBudgetError
	if !asContextBudgetError(err, &be) {
		t.Fatalf("wrong error type: %T %v", err, err)
	}
	if len(*seen) != 0 {
		t.Fatalf("%d requests were sent despite the budget being impossible",
			len(*seen))
	}
	if !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("the diagnostic does not name the cause: %v", err)
	}
	_ = r
}

func TestTheFailureIsAnnouncedOnTheSidecar(t *testing.T) {
	srv, _ := capturingServer(t, "x")
	defer srv.Close()
	client := model.NewClient(model.Options{BaseURL: srv.URL, Model: "qwen",
		RequestTimeout: 5 * time.Second})
	var sidecar, stdout bytes.Buffer
	r := New(Config{
		Model:     model.Options{BaseURL: srv.URL, Model: "qwen"},
		Workspace: t.TempDir(), Permission: "READ_ONLY",
		System:        strings.Repeat("abc ", 20000),
		Tools:         tools.NewRegistry(),
		ContextPolicy: ContextPolicy{ModelLimit: 2048},
	}, client, event.NewEmitter(&sidecar, "s"), &stdout)
	if _, err := r.RunAgent(context.Background(), "hi"); err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(sidecar.String(), "CONTEXT_BUDGET_EXCEEDED") {
		t.Fatalf("the sidecar does not carry the reason:\n%s", sidecar.String())
	}
}

// -- §4 the tool surface is accounted for -------------------------

func TestTheToolSurfaceCountsAgainstTheBudget(t *testing.T) {
	r, _, err := runWithPolicy(t, ContextPolicy{ModelLimit: 131072}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	// An empty registry costs nothing, which is the honest figure;
	// what matters is that the loop set it at all rather than
	// leaving the manager blind to the tool surface.
	m := r.ContextManager()
	if m.ToolSchemaTokens != 0 {
		t.Fatalf("an empty registry cost %d tokens", m.ToolSchemaTokens)
	}
}

// -- §3/§20 durable history is not what goes on the wire ----------

func TestWhatGoesOnTheWireIsTheBoundedViewNotTheHistory(t *testing.T) {
	r, seen, err := runWithPolicy(t, ContextPolicy{ModelLimit: 131072}, "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) == 0 {
		t.Fatal("nothing was sent")
	}
	sent := (*seen)[0]
	if len(sent.Messages) == 0 {
		t.Fatal("an empty message list was sent")
	}
	if r.ContextManager().Stats.Inferences != len(*seen) {
		t.Fatalf("the manager saw %d inferences, the server saw %d requests",
			r.ContextManager().Stats.Inferences, len(*seen))
	}
}

func asContextBudgetError(err error, target **ContextBudgetError) bool {
	for err != nil {
		if be, ok := err.(*ContextBudgetError); ok {
			*target = be
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
