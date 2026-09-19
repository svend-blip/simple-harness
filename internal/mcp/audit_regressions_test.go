package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// handshakeStub is a stdio MCP server built like the reference SDK:
// it refuses every request until it has seen `initialize` and the
// `notifications/initialized` notification.
const handshakeStub = `init=0
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{},\"serverInfo\":{\"name\":\"stub\",\"version\":\"0\"}}}";;
    *'"method":"notifications/initialized"'*)
      init=1;;
    *)
      if [ "$init" = 1 ]; then
        echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"t\",\"description\":\"d\",\"inputSchema\":{\"type\":\"object\"}}]}}"
      else
        echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"error\":{\"code\":-32602,\"message\":\"Received request before initialization was complete\"}}"
      fi;;
  esac
done`

// TestStdioTransportInitializesBeforeListing — the stdio transport
// went straight to tools/list. Every server built on the reference
// SDK answers that with -32602 "Received request before
// initialization was complete", so no such server was usable.
func TestStdioTransportInitializesBeforeListing(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", handshakeStub})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	listing, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing) != 1 || listing[0].Name != "t" {
		t.Errorf("listing = %+v", listing)
	}
}

// longLineStub answers tools/call with a text field of 1.2 MiB.
const longLineStub = `while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}";;
    *'"method":"notifications/initialized"'*) ;;
    *) big=$(head -c 1200000 /dev/zero | tr '\0' a)
       echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"$big\"}]}}";;
  esac
done`

// TestStdioTransportReadsResponsesLongerThanAMebibyte — a 1 MiB line
// cap made any larger response "token too long", after which the
// scanner stayed failed for the rest of the session.
func TestStdioTransportReadsResponsesLongerThanAMebibyte(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", longLineStub})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	out, err := tr.Call(context.Background(), "t", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	content, _ := out["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content = %v", out)
	}
	text, _ := content[0].(map[string]interface{})["text"].(string)
	if len(text) != 1200000 {
		t.Errorf("text length = %d, want 1200000", len(text))
	}
}

// TestHTTPTransportSpeaksTheSessionProtocolWithCredentials — the
// configured api_key and headers were parsed, redacted and never
// sent; the `notifications/initialized` notification the spec
// requires after `initialize` was never sent either.
func TestHTTPTransportSpeaksTheSessionProtocolWithCredentials(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		methods = append(methods, req.Method)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("X-Team") != "genealogy" {
			http.Error(w, "missing credentials", http.StatusUnauthorized)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				http.Error(w, "no session", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, req.ID)
		}
	}))
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL, WithBearerToken("secret-key"), WithHeaders(map[string]string{"X-Team": "genealogy"}))
	defer tr.Close()
	if _, err := tr.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "initialize,notifications/initialized,tools/list" {
		t.Errorf("methods seen = %v", methods)
	}
}

// TestHTTPTransportPicksTheResponseOutOfAMultiEventSSEBody — every
// `data:` line of the body was concatenated and parsed as one
// document; a server that sends a notification event before the
// response produced "invalid character '{' after top-level value".
func TestHTTPTransportPicksTheResponseOutOfAMultiEventSSEBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"working\"}}\n\n")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"x\",\"description\":\"\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", req.ID)
	}))
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	listing, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing) != 1 || listing[0].Name != "x" {
		t.Errorf("listing = %+v", listing)
	}
}

// errorResultTransport answers every call with an isError result.
type errorResultTransport struct{}

func (errorResultTransport) List(context.Context) ([]ListedTool, error) {
	return []ListedTool{{Name: "boom", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}}, nil
}
func (errorResultTransport) Call(context.Context, string, map[string]interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": "the tool failed: no such row"}},
		"isError": true,
	}, nil
}
func (errorResultTransport) Close() error { return nil }

func allowAll(_ context.Context, _ tools.Call, _ tools.Schema, _ tools.Workspace, _ tools.Policy) *tools.DecisionError {
	return nil
}

type permissive struct{}

func (permissive) Decide(context.Context, tools.Call, tools.Workspace) tools.Decision {
	return tools.Decision{Allowed: true}
}

// TestAdapterReportsIsErrorAsAToolFailure — a result carrying
// isError:true was returned as status ok; the contract says a tool
// failure reaches the model as tool_result_status "error".
func TestAdapterReportsIsErrorAsAToolFailure(t *testing.T) {
	reg := tools.NewRegistry()
	ws, _ := path.New(t.TempDir())
	m := NewManager(reg, allowAll, permissive{}, ws)
	if err := m.AddServer(context.Background(), Server{Name: "s"}, errorResultTransport{}); err != nil {
		t.Fatal(err)
	}
	res := reg.Dispatch(context.Background(), tools.Call{Name: "boom", Arguments: map[string]any{}}, ws, permissive{}, allowAll)
	if res.Status != "error" || res.Error == nil || res.Error.Kind != "execution_failed" {
		t.Fatalf("result = %+v, want an execution_failed error", res)
	}
	if !strings.Contains(res.Error.Message, "no such row") {
		t.Errorf("message %q should carry the server's text", res.Error.Message)
	}
}

// TestSchemaFromMapKeepsOptionalTypedPropertiesCallable — a property
// typed as anyOf [T, null] (FastMCP's Optional[T]) was dropped while
// its name stayed in `required`, and additionalProperties defaulted
// to false against the JSON Schema default of true: the model was
// told to supply `limit` and was rejected for supplying it.
func TestSchemaFromMapKeepsOptionalTypedPropertiesCallable(t *testing.T) {
	in := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit": map[string]interface{}{"anyOf": []interface{}{
				map[string]interface{}{"type": "integer"}, map[string]interface{}{"type": "null"}}},
			"name":  map[string]interface{}{"type": []interface{}{"string", "null"}},
			"blob":  map[string]interface{}{"$ref": "#/defs/Blob"},
			"count": map[string]interface{}{"type": "integer"},
		},
		"required": []interface{}{"limit", "blob"},
	}
	schema, err := schemaFromMap(in)
	if err != nil {
		t.Fatal(err)
	}
	if schema.Properties["limit"] != tools.TypeInt || schema.Properties["name"] != tools.TypeString {
		t.Errorf("nullable types not mapped: %+v", schema.Properties)
	}
	if !schema.AdditionalProperties {
		t.Error("additionalProperties absent must mean open (JSON Schema default)")
	}
	call := tools.Call{Name: "t", Arguments: map[string]any{"limit": 3.0, "blob": map[string]any{"a": 1}, "count": 2.0}}
	if err := tools.Validate(call, schema); err != nil {
		t.Errorf("a call supplying every required property was rejected: %v", err)
	}
	explicit := map[string]interface{}{"type": "object", "additionalProperties": false,
		"properties": map[string]interface{}{"a": map[string]interface{}{"type": "string"}}}
	s2, _ := schemaFromMap(explicit)
	if s2.AdditionalProperties {
		t.Error("an explicit additionalProperties:false must be honoured")
	}
}

// TestStdioCancelledCallClosesTheTransport — cancelling a call closed
// the child's stdin but left the transport open, so every later call
// failed "write |1: file already closed" and the child was not reaped
// until Close.
func TestStdioCancelledCallClosesTheTransport(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", `while IFS= read -r line; do
  case "$line" in *'"method":"initialize"'*) echo '{"jsonrpc":"2.0","id":1,"result":{}}';; *) sleep 30;; esac
done`})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := tr.Call(ctx, "t", nil); err == nil {
		t.Fatal("a cancelled call returned nil")
	}
	if !tr.closed.Load() {
		t.Error("the transport is still open after a cancelled call")
	}
	if _, err := tr.Call(context.Background(), "t", nil); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("a call after cancellation = %v, want a closed-transport error", err)
	}
}

// strictParamsStub is a stdio MCP server that, like the reference
// TypeScript SDK, validates each message against the JSON-RPC schema
// before dispatch: `"params":null` is not an object, so the message is
// dropped without a response.
const strictParamsStub = `while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"params":null'*) ;;
    *'"method":"initialize"'*) echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}";;
    *'"method":"notifications/initialized"'*) ;;
    *) echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"t\",\"description\":\"d\",\"inputSchema\":{\"type\":\"object\"}}]}}";;
  esac
done`

// TestStdioTransportOmitsAbsentParams — tools/list went out as
// `"params":null`. The reference TypeScript SDK drops such a message
// silently, so listing against scope-mcp hung until the startup
// deadline ("listing failed: context deadline exceeded").
func TestStdioTransportOmitsAbsentParams(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", strictParamsStub})
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	listing, err := tr.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing) != 1 || listing[0].Name != "t" {
		t.Errorf("listing = %+v", listing)
	}
}

// TestHTTPTransportOmitsAbsentParams — the same wire defect on the
// http transport: a request without parameters carries no params
// member at all.
func TestHTTPTransportOmitsAbsentParams(t *testing.T) {
	var mu sync.Mutex
	var listBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "tools/list" {
			mu.Lock()
			listBody = string(body)
			mu.Unlock()
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, req.ID)
	}))
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	if _, err := tr.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if listBody == "" {
		t.Fatal("no tools/list request reached the server")
	}
	if strings.Contains(listBody, `"params"`) {
		t.Errorf("tools/list carries a params member: %s", listBody)
	}
}

// TestWireSchemaKeepsWhatTheModelNeeds — the model was shown only
// {"goals":{"type":"array"}} for scope-mcp's set_goals: the item
// shape, the status enum and every parameter description were lost in
// the conversion to the validator's type-only schema, so the model had
// to guess the structure of a call it could not see.
func TestWireSchemaKeepsWhatTheModelNeeds(t *testing.T) {
	in := map[string]interface{}{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]interface{}{
			"goals": map[string]interface{}{
				"type":        "array",
				"description": "Full goal list, in intended order.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":     map[string]interface{}{"type": "string"},
						"status": map[string]interface{}{"type": "string", "enum": []interface{}{"pending", "active"}},
						"rank":   map[string]interface{}{"type": "int"},
						"done":   map[string]interface{}{"type": []interface{}{"bool", "null"}},
					},
					"required": []interface{}{"id"},
				},
			},
		},
		"required": []interface{}{"goals"},
	}
	raw := wireSchemaFromMap(in)
	if raw == nil {
		t.Fatal("wireSchemaFromMap returned nil for a complete schema")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["$schema"]; ok {
		t.Error("$schema must not reach the model endpoint")
	}
	goals := got["properties"].(map[string]interface{})["goals"].(map[string]interface{})
	if goals["description"] != "Full goal list, in intended order." {
		t.Errorf("description lost: %v", goals["description"])
	}
	item := goals["items"].(map[string]interface{})
	props := item["properties"].(map[string]interface{})
	if enum, _ := props["status"].(map[string]interface{})["enum"].([]interface{}); len(enum) != 2 {
		t.Errorf("enum lost: %v", props["status"])
	}
	// Strict endpoints answer 400 to the "int"/"bool" shorthand.
	if props["rank"].(map[string]interface{})["type"] != "integer" {
		t.Errorf("int not normalised: %v", props["rank"])
	}
	if alts := props["done"].(map[string]interface{})["type"].([]interface{}); alts[0] != "boolean" {
		t.Errorf("bool not normalised inside a type array: %v", alts)
	}
	// The caller's map is not mutated.
	if in["$schema"] == nil {
		t.Error("input schema was mutated")
	}

	// A schema without the object envelope gets one; nil stays nil.
	raw = wireSchemaFromMap(map[string]interface{}{})
	_ = json.Unmarshal(raw, &got)
	if got["type"] != "object" || got["properties"] == nil {
		t.Errorf("empty schema not completed: %s", raw)
	}
	if wireSchemaFromMap(nil) != nil {
		t.Error("nil schema must stay nil so the caller falls back")
	}
}

// pwdStub answers tools/list with its own working directory as the
// tool description, so the test can see where the child was started.
const pwdStub = `while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}";;
    *'"method":"notifications/initialized"'*) ;;
    *) echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"t\",\"description\":\"$(pwd -P)\",\"inputSchema\":{\"type\":\"object\"}}]}}";;
  esac
done`

// TestStdioTransportStartsTheChildInTheGivenDirectory — a stdio server
// inherited the harness's cwd. A server that keeps state under its cwd
// (scope-mcp: .scope-mcp/state.db) wrote it wherever the harness
// happened to be started, not in the workspace.
func TestStdioTransportStartsTheChildInTheGivenDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", pwdStub}, WithDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	listing, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing) != 1 || listing[0].Description != dir {
		t.Errorf("child cwd = %+v, want %s", listing, dir)
	}
}

// restartableMCP is a streamable-http MCP server that can be "restarted":
// it then forgets its session and answers 404 "Session not found" to the
// old id, as the live mcp-light server does (measured 2026-09-19) and as
// the specification requires.
type restartableMCP struct {
	mu          sync.Mutex
	generation  int
	initialize  int
	initialized int
	calls       []string // session id each tools/call arrived with
	down        bool     // answer 503 to everything
	forgetful   bool     // never recognise a session, even a fresh one
}

func (s *restartableMCP) restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
}

func (s *restartableMCP) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.down {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		current := fmt.Sprintf("session-%d", s.generation)
		switch req.Method {
		case "initialize":
			s.initialize++
			w.Header().Set("Mcp-Session-Id", current)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
			return
		case "notifications/initialized":
			s.initialized++
			w.WriteHeader(http.StatusAccepted)
			return
		}
		sid := r.Header.Get("Mcp-Session-Id")
		if sid != current || s.forgetful {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Session not found"}}`)
			return
		}
		if req.Method == "tools/call" {
			s.calls = append(s.calls, sid)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}],"tools":[]}}`, req.ID)
	})
}

// TestHTTPTransportReinitializesAfterTheServerForgetsTheSession — an MCP
// server restarted mid-run answered every later request 404 "Session not
// found", and the transport, which initialised exactly once, passed each
// one on as a tool failure: the session's MCP use was over. The
// specification's answer to that 404 is a new initialize.
func TestHTTPTransportReinitializesAfterTheServerForgetsTheSession(t *testing.T) {
	stub := &restartableMCP{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	ctx := context.Background()

	if _, err := tr.Call(ctx, "t", nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	stub.restart()
	if _, err := tr.Call(ctx, "t", nil); err != nil {
		t.Fatalf("call after the server restarted: %v", err)
	}
	if _, err := tr.Call(ctx, "t", nil); err != nil {
		t.Fatalf("call after recovery: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.initialize != 2 || stub.initialized != 2 {
		t.Errorf("initialize %d, initialized notifications %d; want 2 and 2", stub.initialize, stub.initialized)
	}
	want := []string{"session-0", "session-1", "session-1"}
	if fmt.Sprint(stub.calls) != fmt.Sprint(want) {
		t.Errorf("tools/call arrived with sessions %v, want %v", stub.calls, want)
	}
}

// TestHTTPTransportGivesUpOnAServerThatNeverKeepsASession — one new
// session per request, not a loop: a server that answers 404 to a session
// it has just issued gets the error reported.
func TestHTTPTransportGivesUpOnAServerThatNeverKeepsASession(t *testing.T) {
	stub := &restartableMCP{forgetful: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	_, err := tr.Call(context.Background(), "t", nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the 404 reported", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.initialize != 2 {
		t.Errorf("initialize sent %d times, want 2 (the first, and one retry)", stub.initialize)
	}
}

// TestHTTPTransportInitializesAgainAfterAFailedInitialize — the first
// initialize was remembered for the life of the transport, its failure
// included: a server that was still starting when the first call came
// stayed unreachable after it was up.
func TestHTTPTransportInitializesAgainAfterAFailedInitialize(t *testing.T) {
	stub := &restartableMCP{down: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	if _, err := tr.Call(context.Background(), "t", nil); err == nil {
		t.Fatal("call against a server that is down succeeded")
	}
	stub.mu.Lock()
	stub.down = false
	stub.mu.Unlock()
	if _, err := tr.Call(context.Background(), "t", nil); err != nil {
		t.Fatalf("call once the server is up: %v", err)
	}
}

// TestHTTPTransportConcurrentCallsShareOneNewSession — calls in flight
// when the session is lost must not each start a session of their own.
func TestHTTPTransportConcurrentCallsShareOneNewSession(t *testing.T) {
	stub := &restartableMCP{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	tr := NewHTTPTransport(srv.URL)
	defer tr.Close()
	if _, err := tr.Call(context.Background(), "t", nil); err != nil {
		t.Fatal(err)
	}
	stub.restart()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tr.Call(context.Background(), "t", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.initialize != 2 {
		t.Errorf("initialize sent %d times for one restart, want 2 in all", stub.initialize)
	}
}
