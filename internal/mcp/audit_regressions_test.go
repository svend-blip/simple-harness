package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
