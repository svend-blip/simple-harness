package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Shell is the shell builtin tool. It executes a shell command in
// its own process group and captures stdout, stderr, the exit code,
// the elapsed duration, and the termination reason (timeout /
// cancelled / escalated / ""). See SCOPE §§11, 27 for the contract;
// see the handoff 020 result file for the foundation seam choices
// (SysProcAttr.Setpgid for process-group ownership, the
// ShellResult wire shape) and the handoff 021 result file for the
// advanced-behavior additions (timeout, cancellation, SIGKILL
// escalation, output-size cap with explicit marker, orphan-survival
// proof).
//
// The tool assumes the dispatch pipeline has already validated the
// call (schema → path → policy). It does NOT re-check the
// permission mode itself — that's the policy stage's job (reviewer
// duty #3). When called directly from a test that bypasses
// Dispatch, the tool runs the command unconditionally; the
// READ_ONLY integration test in builtins_test.go exercises the
// policy-seam denial path via the Registry.Dispatch path, NOT
// direct Execute.
type Shell struct{}

// DefaultTimeout is the deadline applied to a shell call whose caller
// omitted timeout_ms (or passed <= 0). Zero means no default — the
// historical behaviour. The cmd layer sets it from config.ShellTimeout
// after Load; the tool itself never reads the environment, so the test
// surface stays a plain package variable.
//
// Why a default: the per-call timeout_ms existed since handoff 021,
// but a model never sets it. Measured 2026-09-02 on a dispatched chain
// role: `helper & ...` from this tool left the helper holding the
// stdout pipe, and the call — with no deadline — waited 3 h 24 min
// until an operator killed the helper by hand. The default turns that
// into a "timeout" result the model can act on, and the existing
// process-group SIGTERM/SIGKILL path reaps the helper.
var DefaultTimeout time.Duration

// Meta implements tools.Tool.
func (Shell) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name: "shell",
		Description: "Execute a shell command in its own process " +
			"group. Mutation tool — gated by the policy stage " +
			"(READ_ONLY denies; WORKSPACE_WRITE allows; " +
			"FULL_ACCESS allows). Captures stdout, stderr, exit " +
			"code, duration, and termination reason. Supports " +
			"optional timeout_ms (kills the process group with " +
			"SIGTERM after the deadline; escalates to SIGKILL " +
			"after a 2s grace if the child ignores SIGTERM) and " +
			"max_output_bytes (per-stream cap with explicit " +
			"in-stream truncation marker; the cap is on capture, " +
			"not on the child process).",
	}
}

// Schema implements tools.Tool. `command` is required. `cwd` is
// optional (defaults to inheriting the parent's cwd when absent).
// `timeout_ms` is optional (int; 0 or absent means no timeout —
// the run completes in its own time). `max_output_bytes` is
// optional (int; 0 or absent means no cap — all bytes are
// captured).
func (Shell) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"command"},
		Properties: map[string]tools.PropertyType{
			"command":          tools.TypeString,
			"cwd":              tools.TypeString,
			"timeout_ms":       tools.TypeInt,
			"max_output_bytes": tools.TypeInt,
		},
	}
}

// ShellResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format
// and downstream consumers parse the fields by name.
//
// ExitCode is the process's exit status (0-on-success /
// non-zero-on-failure). The shell tool returns Result{Status:"ok"}
// even on a non-zero exit — the exit code is data, not an error.
// This matches the existing WriteFile and ApplyPatch conventions
// where the tool's structured content carries success-or-failure
// details.
//
// Stdout and Stderr are the captured bytes. When
// max_output_bytes is set and the cap is hit on a stream, the
// captured string is truncated to at most `cap` bytes followed by
// the canonical truncation marker (see truncateMarker below); the
// child is NOT killed — the cap is on capture, not on the process.
//
// Duration is the elapsed wall time formatted via
// time.Duration.String() (e.g. "42.1ms", "1.5s"). Downstream
// consumers parse it via time.ParseDuration.
//
// TerminationReason is one of: "" (normal exit, including a
// non-zero exit code with no signal), "timeout" (timeout_ms fired
// and SIGTERM was honored), "cancelled" (ctx was cancelled and
// SIGTERM was honored), "escalated" (SIGTERM was ignored by the
// child, grace expired, SIGKILL was delivered). The field is
// present in the JSON from day one (handoff 020) so the wire
// shape is stable across handoffs.
type ShellResult struct {
	ExitCode          int    `json:"exit_code"`
	Stdout            string `json:"stdout"`
	Stderr            string `json:"stderr"`
	Duration          string `json:"duration"`
	TerminationReason string `json:"termination_reason,omitempty"`
}

// terminateGrace is the bounded delay between SIGTERM and SIGKILL.
// Per SCOPE §27 "controlled escalation where required" — two
// seconds gives well-behaved children (default signal disposition)
// time to exit cleanly while bounding the wall-clock wait for
// children that ignore SIGTERM (e.g. `trap ” TERM; sleep 60`).
const terminateGrace = 2 * time.Second

// truncateMarkerFor builds the marker with the cap value
// substituted in. The "N bytes" substring is replaced with the
// actual cap (decimal). The marker text is the binding contract —
// the orphan/cap tests assert its content verbatim. Note the
// leading "\n" so the marker always starts on a fresh line
// regardless of what the preceding bytes end with.
func truncateMarkerFor(cap int) string {
	return "\n[shell output truncated: cap " + strconv.Itoa(cap) +
		" bytes reached; remaining output not captured]"
}

// cappedWriter wraps an io.Writer (typically *bytes.Buffer) with
// a per-stream byte cap. Writes that fit within the cap are
// passed through to the underlying writer; the first Write that
// would exceed the cap causes (a) the underlying writer to
// receive the cap-th byte verbatim, (b) the marker to be
// appended, and (c) the writer to enter "truncated" mode where
// subsequent Writes are no-ops (return len(p), nil without
// forwarding). The truncated flag is checked without a lock —
// the writer is single-goroutine (the child's stdout/stderr pipe
// is read by exactly one goroutine inside the os/exec runtime).
type cappedWriter struct {
	underlying io.Writer
	cap        int
	truncated  bool
}

func newCappedWriter(underlying io.Writer, cap int) *cappedWriter {
	return &cappedWriter{underlying: underlying, cap: cap}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.cap <= 0 || w.truncated {
		// No cap, or already truncated — pass through (no-op if
		// already truncated, but we still claim the full length
		// so os/exec doesn't see a short write).
		if w.truncated {
			return len(p), nil
		}
		return w.underlying.Write(p)
	}
	// We have a cap and have not yet hit it. Determine how many
	// bytes we can accept.
	n := len(p)
	if n > w.cap {
		n = w.cap
	}
	nn, err := w.underlying.Write(p[:n])
	if err != nil {
		return nn, err
	}
	if len(p) > w.cap {
		// Cap exceeded on this Write — append marker once,
		// enter truncated mode.
		w.truncated = true
		if _, mErr := w.underlying.Write([]byte(truncateMarkerFor(w.cap))); mErr != nil {
			return nn, mErr
		}
	}
	return len(p), nil // claim the full length so os/exec doesn't error
}

// Execute implements tools.Tool. Algorithm (handoff 021):
//
//  1. Extract command (required), optional cwd, optional
//     timeout_ms (int milliseconds; <=0 or absent means no
//     timeout), optional max_output_bytes (int; <=0 or absent
//     means no cap). Missing/non-string command returns a
//     structured schema_violation error.
//  2. Construct exec.Command("sh", "-c", command) with
//     Dir=cwd and SysProcAttr{Setpgid: true}. The Setpgid wire
//     is the SCOPE §27 seam: child PID == child PGID.
//  3. Allocate bytes.Buffer for stdout and stderr; wrap each in
//     a cappedWriter when max_output_bytes > 0.
//  4. cmd.Start() (not Run() — we need to wait in a goroutine so
//     the cancel/SIGTERM/SIGKILL escalation can fire).
//  5. Resolve pgid from cmd.Process.Pid (Getpgid returns the
//     child's PGID; with Setpgid:true that is the child's own
//     PID, confirmed by TestShell_ProcessGroupOwnership).
//  6. Set up cancellation sources (ctx.Done() and timeout_ms),
//     both delivering SIGTERM to the process group (Kill(-pgid,
//     SIGTERM)) the first time they fire. A sync.Once guards
//     against double-signal.
//  7. Wait via a goroutine: cmd.Wait() into a channel; the main
//     path selects on (Wait, ctx.Done, timeout). If a SIGTERM
//     has fired, a separate goroutine sleeps for terminateGrace
//     and then SIGKILLs the group if cmd.Wait has not completed.
//  8. After Wait returns (with or without SIGKILL escalation),
//     populate ShellResult: ExitCode from ProcessState (or -1
//     if State is nil — defensive), Stdout/Stderr from the
//     buffers, Duration from time.Since(start), and
//     TerminationReason from the populated reason pointer
//     (atomic.LoadPointer); the empty reason string means a
//     normal exit (including a non-zero exit code with no
//     signal).
//  9. On a cmd.Start failure (e.g. sh not found), return
//     Result{}, fmt.Errorf(...). On a cmd.Wait failure that is
//     NOT an ExitError and NOT a signal kill, return
//     Result{}, fmt.Errorf(...) — same as handoff 020.
//
// The Setpgid:true wire from handoff 020 stays unchanged. The
// ShellResult JSON shape stays unchanged (no new fields added).
func (Shell) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	command, ok := call.Arguments["command"].(string)
	if !ok || command == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "shell: missing or non-string command argument",
			Call:    call,
		}}, nil
	}
	cwd, _ := call.Arguments["cwd"].(string)
	var timeoutMs int
	if v, ok := call.Arguments["timeout_ms"]; ok && v != nil {
		// Accept int OR float64 (the JSON decoder produces
		// float64 for numbers; defensive coercion).
		switch n := v.(type) {
		case int:
			timeoutMs = n
		case float64:
			timeoutMs = int(n)
		}
	}
	if timeoutMs <= 0 && DefaultTimeout > 0 {
		timeoutMs = int(DefaultTimeout / time.Millisecond)
	}
	var maxOutputBytes int
	if v, ok := call.Arguments["max_output_bytes"]; ok && v != nil {
		switch n := v.(type) {
		case int:
			maxOutputBytes = n
		case float64:
			maxOutputBytes = int(n)
		}
	}

	cmd := exec.Command("sh", "-c", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = newCappedWriter(&stdoutBuf, maxOutputBytes)
	cmd.Stderr = newCappedWriter(&stderrBuf, maxOutputBytes)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return tools.Result{}, fmt.Errorf("shell: start %q: %w", command, err)
	}

	pgid, pgidErr := syscall.Getpgid(cmd.Process.Pid)
	if pgidErr != nil {
		// Setpgid:true was set; this should not fail unless
		// the child already exited (e.g. sh -c 'exit 0' ran
		// and exited between Start and Getpgid). Defensive
		// fallback: kill the child directly.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return tools.Result{}, fmt.Errorf("shell: Getpgid(%d): %w", cmd.Process.Pid, pgidErr)
	}

	// Cancellation sources. First to fire wins; the sync.Once
	// ensures we send SIGTERM exactly once even if both ctx and
	// timeout fire near-simultaneously. The reason pointer is
	// atomically loaded at the end.
	var reason atomic.Pointer[string]
	setReason := func(r string) {
		s := r
		reason.CompareAndSwap(nil, &s)
	}
	var signalOnce sync.Once
	signalTerm := func() {
		signalOnce.Do(func() {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		})
	}

	ctxDone := ctx.Done()
	if ctxDone != nil {
		go func() {
			<-ctxDone
			setReason("cancelled")
			signalTerm()
		}()
	}
	var timer *time.Timer
	if timeoutMs > 0 {
		timer = time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
			setReason("timeout")
			signalTerm()
		})
	}

	// Grace-escalation goroutine: once a SIGTERM has been sent,
	// wait terminateGrace; if the child is still alive, send
	// SIGKILL. Runs in parallel with cmd.Wait below.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var escalated bool
	if ctxDone != nil || timer != nil {
		// We have a signal source. Watch for the grace window.
		go func() {
			// Wait until a reason has been set (i.e. SIGTERM
			// sent). Poll because there's no clean channel to
			// "wait for first signal". Defensive ceiling: if
			// neither ctx nor timeout ever fires, exit after 30s
			// having done nothing.
			deadline := time.Now().Add(30 * time.Second)
			for reason.Load() == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if reason.Load() == nil {
				// No signal fired during the run; nothing to
				// escalate. Exit silently.
				return
			}
			time.Sleep(terminateGrace)
			// If the child has exited, cmd.Wait has returned
			// and waitDone is buffered; cmd.Process is reused
			// internally so we can't probe it directly. Send
			// SIGKILL — if the child is dead, the syscall
			// errors with ESRCH, which we ignore. The
			// TerminationReason becomes "escalated" only if
			// SIGKILL actually killed the child (i.e.
			// waitDone has NOT yet received a value when we
			// get here).
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
				// SIGKILL was sent (not ESRCH) — child was
				// still alive after the grace. This is the
				// escalation path.
				escalated = true
			}
		}()
	}

	waitErr := <-waitDone
	if timer != nil {
		timer.Stop()
	}
	elapsed := time.Since(start)

	var exitCode int
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else {
		exitCode = -1
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return tools.Result{}, fmt.Errorf("shell: run %q: %w", command, waitErr)
	}

	term := ""
	if r := reason.Load(); r != nil {
		term = *r
		if escalated {
			term = "escalated"
		}
	}

	return tools.Result{Status: "ok", Content: ShellResult{
		ExitCode:          exitCode,
		Stdout:            stdoutBuf.String(),
		Stderr:            stderrBuf.String(),
		Duration:          elapsed.String(),
		TerminationReason: term,
	}}, nil
}

// Compile-time assertion that Shell implements tools.Tool.
var _ tools.Tool = Shell{}
