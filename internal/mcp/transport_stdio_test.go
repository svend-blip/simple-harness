package mcp

import (
	"context"
	"testing"
)

// Stubs are in-process shell pipelines that speak newline-delimited
// JSON-RPC 2.0 over stdin/stdout. The transport spawns `sh -c <stub>`;
// the stub reads one JSON-RPC request per line from stdin, extracts
// the id via sed, and writes a canned response with the matching id
// to stdout. The pipe is one-shot per child invocation — a stub that
// needs multiple roundtrips (listing + call) loops on `read line`,
// a stub that does a single roundtrip (mid-call cancel) does one
// `read line` then sleeps.
//
// The stubs live in *_test.go and are NOT exported; production code
// paths must NOT reference them. They are the only MCP endpoints
// the tests in this file exercise (per GOAL §2 bound decision 7 —
// no live service in scripts/test.sh).
const (
	// listingStub responds to a single tools/list request with a
	// canned 2-tool listing. The stub loops on `read line` so the
	// transport can issue more than one request (in practice the
	// listing test does only one call; the loop is for robustness
	// if a future test reuses the same fixture).
	listingStub = `while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"tool_alpha\",\"description\":\"alpha tool\",\"inputSchema\":{\"type\":\"object\"}},{\"name\":\"tool_beta\",\"description\":\"beta tool\",\"inputSchema\":{\"type\":\"object\"}}]}}"
done`

	// callStub responds to a single tools/call request with a
	// canned {"content":[...]} response that echoes the parsed
	// tool name (the transport passes the name verbatim; the
	// stub extracts it via sed and echoes "echo:<name>" so the
	// test can verify the wire shape end-to-end).
	callStub = `while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  name=$(echo "$line" | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')
  echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"echo:$name\"}]}}"
done`

	// slowStub reads one request, then sleeps for a long time
	// before writing a response. The test uses this stub to
	// exercise mid-call ctx cancel: the transport's roundtrip
	// blocks in readLine waiting for the response; the test
	// cancels the ctx; the transport signals the child by
	// closing stdin; the child's `read` already returned, so
	// the sh continues sleeping until Close escalates to
	// SIGTERM (the SCOPE §27 process-group kill).
	slowStub = `read line
sleep 30
id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{}}`

	// longRunningStub is a child that blocks on `read line`
	// forever. The test uses it to verify (a) Setpgid:true is
	// honored (PID == PGID), and (b) Close reaps the child
	// (no zombie, no orphan). The stub does not respond to any
	// request — the test does not call List/Call on this
	// transport.
	longRunningStub = `while true; do read line; done`
)

// TestMCP_TransportStdio_StubListing: the stdio transport's List
// roundtrips a JSON-RPC tools/list request through a child-process
// pipe and decodes the result.tools array. The fixture is a shell
// loop that reads JSON-RPC requests from stdin and writes canned
// responses to stdout; the transport's framing handles the id
// matching and the JSON-RPC shape parsing.
func TestMCP_TransportStdio_StubListing(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", listingStub})
	if err != nil {
		t.Fatalf("NewStdioTransport error = %v, want nil", err)
	}
	defer tr.Close()

	got, err := tr.List(context.Background())
	if err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2 (got=%+v)", len(got), got)
	}
	if got[0].Name != "tool_alpha" || got[0].Description != "alpha tool" {
		t.Fatalf("got[0] = %+v, want Name=tool_alpha Description=alpha tool", got[0])
	}
	if got[1].Name != "tool_beta" || got[1].Description != "beta tool" {
		t.Fatalf("got[1] = %+v, want Name=tool_beta Description=beta tool", got[1])
	}
	if _, ok := got[0].InputSchema["type"]; !ok {
		t.Fatalf("got[0].InputSchema = %+v, want object with type field", got[0].InputSchema)
	}
}

// TestMCP_TransportStdio_StubCall: the stdio transport's Call
// roundtrips a JSON-RPC tools/call request through a child-process
// pipe. The fixture extracts the tool name from the request and
// echoes it back in the response so the test can verify the
// transport passed the name verbatim.
func TestMCP_TransportStdio_StubCall(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", callStub})
	if err != nil {
		t.Fatalf("NewStdioTransport error = %v, want nil", err)
	}
	defer tr.Close()

	got, err := tr.Call(context.Background(), "tool_alpha", map[string]interface{}{"x": float64(1)})
	if err != nil {
		t.Fatalf("Call error = %v, want nil", err)
	}
	content, ok := got["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("Call result content = %+v, want []interface{} of length 1", got["content"])
	}
	first, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Call result content[0] = %T, want map[string]interface{}", content[0])
	}
	if first["text"] != "echo:tool_alpha" {
		t.Fatalf("Call result content[0].text = %v, want %q (transport must pass the name verbatim)",
			first["text"], "echo:tool_alpha")
	}
}
