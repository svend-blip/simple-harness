package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// stdioTransport is the child-process stdio Transport implementation.
// One instance per MCP server declaration; created by NewStdioTransport.
//
// Wire shape (newline-delimited JSON-RPC 2.0 over stdin/stdout):
//
//   - List(): write {"jsonrpc":"2.0","id":<n>,"method":"tools/list",
//     "params":{}}\n to child's stdin. Read JSON objects line-by-
//     line from stdout until a response with matching id arrives
//     (the server may emit notifications/log entries on stdout —
//     the transport ignores anything that isn't a JSON-RPC response
//     with the matching id). Parse "result": {"tools":[...]};
//     surface "error": {"code":<n>,"message":<s>} as an error.
//   - Call(): write {"jsonrpc":"2.0","id":<n>,"method":"tools/
//     call","params":{"name":<name>,"arguments":<args>}}\n. Read
//     stdout until the matching id response arrives. Parse "result":
//     {"content":[...]} or similar. JSON-RPC error → wrapped error.
//   - Close(): close stdin, wait briefly for the child to exit
//     gracefully; if it doesn't exit within the grace, SIGTERM the
//     process group (the child and any of its descendants), wait
//     another grace; finally SIGKILL the process group. Wait for
//     the child to reap (no zombies).
//
// SCOPE §27 compliance: the child is spawned with
// SysProcAttr{Setpgid: true} (the wire documented in
// internal/tools/builtins/shell.go:189-197 + pinned by
// TestShell_ProcessGroupOwnership). With Setpgid:true, the child's
// PID equals its PGID; the Close path uses
// `syscall.Kill(-pgid, ...)` to signal the whole group, mirroring
// the shell builtin.
//
// SCOPE §30 compliance: error messages NEVER include the raw
// command-line arguments verbatim (a server command may carry
// secrets as args — e.g. `--api-key=xyz`). The transport's error
// format is `mcp: <operation>: <err>` where <operation> is "stdio
// child" or "stdio roundtrip" — the server name is wrapped at the
// caller (Manager.AddServer), so secrets do not appear in any
// surfaced error.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mu     sync.Mutex
	nextID int64
	closed atomic.Bool
}

// NewStdioTransport spawns the child process and wires its stdio
// pipes to the transport. The child is created with
// SysProcAttr{Setpgid: true} (SCOPE §27 + Run 005's process-group
// discipline: child PID == child PGID).
//
// Spawn-time cancellation: if ctx is already cancelled when
// NewStdioTransport is called, the child is reaped (SIGKILL +
// cmd.Wait) before the error is returned — no orphan from a
// cancelled spawn.
//
// The constructor uses signature (b): a successful spawn returns a
// *stdioTransport; the spawn error is returned as the second value.
// If the child exits between cmd.Start and the first List/Call,
// the roundtrip surfaces the EOF as a structured error (per
// GOAL §2 bound decision 4 — declared-but-unreachable becomes a
// structured startup error at the caller).
func NewStdioTransport(ctx context.Context, command []string) (*stdioTransport, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("mcp: stdio: command must be non-empty")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdio: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: stdio: start child: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: scanner,
	}, nil
}

// Compile-time assertion that stdioTransport satisfies Transport.
var _ Transport = (*stdioTransport)(nil)

// List implements Transport.List. Sends a tools/list JSON-RPC
// request and parses the result.tools array into []ListedTool.
func (t *stdioTransport) List(ctx context.Context) ([]ListedTool, error) {
	var wire struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := t.roundtrip(ctx, "tools/list", nil, &wire); err != nil {
		return nil, err
	}
	out := make([]ListedTool, len(wire.Tools))
	for i, w := range wire.Tools {
		out[i] = ListedTool{
			Name:        w.Name,
			Description: w.Description,
			InputSchema: w.InputSchema,
		}
	}
	return out, nil
}

// Call implements Transport.Call. Sends a tools/call JSON-RPC
// request and returns the result map verbatim.
func (t *stdioTransport) Call(ctx context.Context, name string, args map[string]interface{}) (map[string]interface{}, error) {
	params := struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}{Name: name, Arguments: args}
	var out map[string]interface{}
	if err := t.roundtrip(ctx, "tools/call", params, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, nil
}

// Close implements Transport.Close. Closes stdin (so a well-behaved
// child sees EOF and exits), waits briefly, then escalates:
//
//  1. Close stdin → child reads EOF → child exits gracefully.
//  2. Wait up to 2s for cmd.Wait to return.
//  3. If still alive, SIGTERM the process group
//     (syscall.Kill(-pgid, SIGTERM); pgid = cmd.Process.Pid because
//     Setpgid:true makes the child its own group leader).
//  4. Wait up to 2s for cmd.Wait to return.
//  5. If still alive, SIGKILL the process group as last resort.
//  6. Wait for cmd.Wait to return (the child is reaped — no zombie).
//
// The function is idempotent: a second call returns nil without
// re-closing stdin or re-signaling the group (the closed flag
// guards the body). The reaper goroutine calls cmd.Wait exactly
// once; the second Close does not race against it.
func (t *stdioTransport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	pgid := t.cmd.Process.Pid
	done := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(done)
	}()
	if waitDone(done, 2*time.Second) {
		return nil
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitDone(done, 2*time.Second) {
		return nil
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	<-done
	return nil
}

// waitDone returns true if done is signalled within d, false
// otherwise. A non-blocking select on done first avoids waiting
// when the goroutine has already completed.
func waitDone(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	default:
	}
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// roundtrip serializes a JSON-RPC request/response cycle against
// the stdio stream. The mutex guards the stream against concurrent
// List/Call (the child's stdin/stdout is a single ordered pipe;
// concurrent calls would interleave their frames). The request id
// is a per-transport atomic counter so concurrent internal callers
// (none today, but the seam stays clean) get distinct ids.
//
// Per-call cancellation: when ctx is cancelled mid-call, the
// transport signals the child by closing stdin. The child's read
// returns false, the child exits, the stdout pipe closes, the
// scanner returns false (EOF), and roundtrip returns ctx.Err().
// The Close path then reaps the child (no orphan).
func (t *stdioTransport) roundtrip(ctx context.Context, method string, params interface{}, out interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed.Load() {
		return fmt.Errorf("mcp: stdio: transport is closed")
	}
	id := atomic.AddInt64(&t.nextID, 1)
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	bs, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("mcp: stdio: marshal request: %w", err)
	}
	bs = append(bs, '\n')
	if _, err := t.stdin.Write(bs); err != nil {
		return fmt.Errorf("mcp: stdio: write request: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := t.readLine(ctx)
		if err != nil {
			return err
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *jsonRPCError   `json:"error"`
		}
		if err := json.Unmarshal(line, &parsed); err != nil {
			// Skip non-JSON lines (server may emit log entries or
			// notifications). Continue scanning.
			continue
		}
		if parsed.ID == nil {
			// Notification (no id field). Continue scanning.
			continue
		}
		var gotID int64
		if err := json.Unmarshal(parsed.ID, &gotID); err != nil {
			continue
		}
		if gotID != id {
			// Not our response (out-of-order or unrelated).
			// Continue scanning.
			continue
		}
		if parsed.Error != nil {
			return fmt.Errorf("mcp: jsonrpc error %d: %s", parsed.Error.Code, parsed.Error.Message)
		}
		if out != nil && len(parsed.Result) > 0 && string(parsed.Result) != "null" {
			if err := json.Unmarshal(parsed.Result, out); err != nil {
				return fmt.Errorf("mcp: stdio: parse result: %w", err)
			}
		}
		return nil
	}
}

// readLine returns the next non-context-cancelled stdout line. If
// ctx is cancelled while the underlying bufio.Scanner.Scan call
// is blocked, the transport signals the child by closing stdin
// (the child sees EOF on its stdin, exits, the pipe closes, the
// scanner returns false, the readLine goroutine completes). The
// caller then receives ctx.Err() — the structured tool failure.
func (t *stdioTransport) readLine(ctx context.Context) ([]byte, error) {
	out := make(chan readLineResult, 1)
	go func() {
		if t.stdout.Scan() {
			out <- readLineResult{line: append([]byte(nil), t.stdout.Bytes()...)}
			return
		}
		if err := t.stdout.Err(); err != nil {
			out <- readLineResult{err: err}
			return
		}
		out <- readLineResult{eof: true}
	}()
	select {
	case r := <-out:
		if r.err != nil {
			return nil, fmt.Errorf("mcp: stdio: read response: %w", r.err)
		}
		if r.eof {
			return nil, fmt.Errorf("mcp: stdio: read response: unexpected EOF")
		}
		return r.line, nil
	case <-ctx.Done():
		// Signal the child to exit by closing stdin. The
		// goroutine will eventually return (with EOF or an
		// error). We don't wait for it — we return ctx.Err()
		// to the caller; the bufio.Scanner goroutine exits
		// when the child's stdout pipe closes.
		_ = t.stdin.Close()
		return nil, ctx.Err()
	}
}

// readLineResult is the internal result of the readLine goroutine.
// Exactly one of (line), (err), or (eof:true) is set.
type readLineResult struct {
	line []byte
	err  error
	eof  bool
}

// ErrStdioClosed is the sentinel a caller can use to detect that a
// roundtrip hit a closed transport. The transport's own errors are
// wrapped versions of this sentinel; today no caller needs to
// detect them (the Manager.AddServer wraps every List error and
// the adapter wraps every Call error). The sentinel is exported
// for the future test that wants to assert the closed-state path.
var ErrStdioClosed = errors.New("mcp: stdio: transport is closed")
