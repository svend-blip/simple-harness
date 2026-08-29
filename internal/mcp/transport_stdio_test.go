package mcp

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
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

// TestMCP_TransportStdio_ProcessGroupOwnership: the SCOPE §27 +
// Run 005 process-group pin. Three assertions:
//
//   (a) the child is spawned with Setpgid:true
//       (syscall.Getpgid(child.Pid) == child.Pid);
//   (b) Close reaps the child (no zombie — syscall.Wait4 with
//       WNOHANG returns -1/ECHILD after Close returns);
//   (c) the long-running child is actually killed by Close
//       (kill -0 returns ESRCH after Close returns).
//
// The test does NOT call List/Call on the transport — the
// longRunningStub does not respond to requests; the test only
// exercises the spawn + cleanup paths.
func TestMCP_TransportStdio_ProcessGroupOwnership(t *testing.T) {
	tr, err := NewStdioTransport(context.Background(), []string{"sh", "-c", longRunningStub})
	if err != nil {
		t.Fatalf("NewStdioTransport error = %v, want nil", err)
	}
	pid := tr.cmd.Process.Pid

	// Give the child a moment to start its read loop.
	time.Sleep(50 * time.Millisecond)

	// (a) Setpgid:true: child PID == child PGID.
	pgid, pgErr := syscall.Getpgid(pid)
	if pgErr != nil {
		t.Fatalf("Getpgid(%d) error = %v", pid, pgErr)
	}
	if pgid != pid {
		t.Fatalf("pgid = %d, pid = %d (Setpgid:true makes PID == PGID; mismatch indicates the SCOPE §27 wire is broken)",
			pgid, pid)
	}

	// The child is alive before Close.
	if kErr := syscall.Kill(pid, 0); kErr != nil {
		t.Fatalf("Kill(%d, 0) before Close = %v, want nil (child should be alive)", pid, kErr)
	}

	// Close reaps the child (closes stdin → sh's read returns
	// false → while loop exits → sh exits → cmd.Wait returns).
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error = %v, want nil", err)
	}

	// (b) No zombie: Wait4 with WNOHANG returns -1/ECHILD.
	var status syscall.WaitStatus
	wpid, wErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if wpid != -1 || wErr != syscall.ECHILD {
		t.Fatalf("Wait4(%d, WNOHANG) after Close = (%d, %v), want (-1, ECHILD) — child reaped",
			pid, wpid, wErr)
	}

	// (c) Child is dead: kill -0 returns ESRCH.
	if kErr := syscall.Kill(pid, 0); kErr != syscall.ESRCH {
		t.Fatalf("Kill(%d, 0) after Close = %v, want ESRCH — child should be dead", pid, kErr)
	}
}

// TestMCP_TransportStdio_MidCallDisconnect: mid-call ctx cancel.
// The test spawns a slow stub (read line; sleep 30; respond),
// starts a List call, cancels the ctx mid-call (before the sh
// wakes from sleep), and verifies:
//
//   - the transport's List returns context.Canceled cleanly
//     (no panic, no orphan);
//   - the child is reaped on Close (no zombie, no orphan).
//
// The transport's mid-call handler closes stdin when ctx fires
// (per the documented "signal the child" path); the sh is
// sleeping and does not notice, but the Close escalation
// (SIGTERM at 2s, SIGKILL at 4s) cleans it up.
func TestMCP_TransportStdio_MidCallDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	tr, err := NewStdioTransport(ctx, []string{"sh", "-c", slowStub})
	if err != nil {
		cancel()
		t.Fatalf("NewStdioTransport error = %v, want nil", err)
	}
	pid := tr.cmd.Process.Pid

	// Cancel the ctx after a short delay (the slowStub reads
	// the request, then sleeps 30s; the cancel happens during
	// the sleep, before the sh writes its response).
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	// List should fail with context.Canceled.
	_, listErr := tr.List(ctx)
	if listErr == nil {
		// Force Close so the test can exit cleanly.
		_ = tr.Close()
		t.Fatalf("List error = nil, want error (ctx cancelled mid-call)")
	}
	if !errors.Is(listErr, context.Canceled) {
		_ = tr.Close()
		t.Fatalf("List error = %v, want context.Canceled", listErr)
	}

	// Close reaps the child. The sh is sleeping; Close waits
	// up to 2s for graceful exit, then SIGTERMs the process
	// group, then SIGKILLs as last resort.
	if err := tr.Close(); err != nil {
		t.Fatalf("Close error = %v, want nil", err)
	}

	// Verify the child is reaped.
	var status syscall.WaitStatus
	wpid, wErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if wpid != -1 || wErr != syscall.ECHILD {
		t.Fatalf("Wait4(%d, WNOHANG) after Close = (%d, %v), want (-1, ECHILD) — child reaped",
			pid, wpid, wErr)
	}
}
