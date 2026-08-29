package builtins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// TestShell_HappyPath: a simple echo command exits 0 and the
// captured stdout matches exactly.
func TestShell_HappyPath(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "echo hello-world",
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", sr.ExitCode)
	}
	if sr.Stdout != "hello-world\n" {
		t.Fatalf("Stdout = %q, want %q", sr.Stdout, "hello-world\n")
	}
	if sr.Stderr != "" {
		t.Fatalf("Stderr = %q, want empty", sr.Stderr)
	}
	if sr.Duration == "" {
		t.Fatalf("Duration is empty")
	}
	if sr.TerminationReason != "" {
		t.Fatalf("TerminationReason = %q, want empty for a normal exit", sr.TerminationReason)
	}
}

// TestShell_NonZeroExit: a command that exits non-zero returns
// Status:"ok" (per the WriteFile / ApplyPatch convention) with
// the exit code carried in ShellResult.ExitCode.
func TestShell_NonZeroExit(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "sh -c 'exit 7'",
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (non-zero exit is data, not an error)", res.Status, "ok")
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", sr.ExitCode)
	}
	if sr.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty", sr.Stdout)
	}
	if sr.Stderr != "" {
		t.Fatalf("Stderr = %q, want empty", sr.Stderr)
	}
}

// TestShell_StderrCapture: a command that writes to stderr is
// captured into ShellResult.Stderr verbatim.
func TestShell_StderrCapture(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "sh -c 'echo to-stderr >&2'",
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q", res.Status, "ok")
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.Stdout != "" {
		t.Fatalf("Stdout = %q, want empty", sr.Stdout)
	}
	if sr.Stderr != "to-stderr\n" {
		t.Fatalf("Stderr = %q, want %q", sr.Stderr, "to-stderr\n")
	}
}

// TestShell_DurationReported: a sleep command's elapsed time is
// reflected in ShellResult.Duration (formatted via
// time.Duration.String()) and is parseable via
// time.ParseDuration back to a value >= the sleep threshold
// (allowing for scheduler slop).
func TestShell_DurationReported(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "sh -c 'sleep 0.05'",
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q", res.Status, "ok")
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.Duration == "" {
		t.Fatalf("Duration is empty")
	}
	parsed, err := time.ParseDuration(sr.Duration)
	if err != nil {
		t.Fatalf("ParseDuration(%q): %v", sr.Duration, err)
	}
	if parsed < 40*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 40ms (slept 50ms)", parsed)
	}
}

// TestShell_ProcessGroupOwnership (LOAD-BEARING — pins the
// SCOPE §27 invariant): the spawned child's PID equals its
// process-group ID (this is what SysProcAttr.Setpgid:true
// guarantees and what handoff 021's syscall.Kill(-pgid,
// SIGTERM) targets). Without this test, a regression to
// Setpgid:false would pass handoff 020's basic tests and
// silently break handoff 021's orphan-survival proof.
//
// The test uses `ps -o pgid= -p $$` to read the child's PGID
// via /proc (the kernel reports the PGID of any running PID
// regardless of the inspecting process's permissions). The
// child writes its PID and PGID to stdout BEFORE exiting;
// cmd.Run() blocks until the child exits and the bytes are
// captured into ShellResult.Stdout. The test parses the
// "PID PGID" pair and asserts PID == PGID. This is the
// observable signature of Setpgid:true.
func TestShell_ProcessGroupOwnership(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": `echo "$$ $(ps -o pgid= -p $$)"`,
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q", res.Status, "ok")
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	fields := strings.Fields(sr.Stdout)
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields (PID PGID), got %d: %q", len(fields), sr.Stdout)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse child PID %q: %v", fields[0], err)
	}
	pgid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse child PGID %q: %v", fields[1], err)
	}
	if pgid != pid {
		t.Fatalf("child PGID = %d, want %d (Setpgid:true makes PID == PGID; this is the SCOPE §27 invariant)",
			pgid, pid)
	}
}

// TestShell_TimeoutKillsWholeGroup: a long-running command with a
// short timeout_ms is killed by SIGTERM to the process group
// (reviewer duty #2, partial — SIGTERM honored path). The
// command spawns `sleep 60` as a backgrounded child inside the
// same shell invocation, so the sleep is in the harness's
// process group (Setpgid:true) and gets killed when the SIGTERM
// fires. The command writes the spawned sleep's PID to a marker
// file (via `echo $! > /tmp/...`) before the SIGTERM arrives.
//
// After Execute returns with TerminationReason="timeout", the
// test reads the marker file to recover the spawned sleep's PID
// and asserts the PID is no longer alive via
// exec.Command("kill", "-0", pid).Run() returning non-zero
// (ESRCH).
//
// The wall-clock bound is ~3 seconds (timeout_ms=500 + a
// 2-second terminateGrace ceiling — the test asserts the kill
// happens within 3 seconds, well before sleep 60 would expire
// naturally).
func TestShell_TimeoutKillsWholeGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "sleep-marker.pid")
	// The command spawns a sleep and writes its PID to the
	// marker file. The outer `wait` keeps the outer shell
	// alive until the sleep finishes (which won't happen
	// because the harness SIGTERMs the whole group).
	command := fmt.Sprintf(`sh -c 'sleep 60 & echo $! > %s; wait'`, marker)

	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command":    command,
		"timeout_ms": 500,
	}}
	start := time.Now()
	res, err := sh.Execute(context.Background(), call)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "timeout" {
		t.Fatalf("TerminationReason = %q, want %q (timeout_ms=500 must SIGTERM the group)",
			sr.TerminationReason, "timeout")
	}
	// The kill should land within timeout_ms + a small grace
	// for the cmd.Wait propagation; not the full 2-second
	// terminateGrace because default SIGTERM disposition
	// (terminate) does not require the grace.
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, want < 3s (timeout_ms=500 must kill in well under 2s)",
			elapsed)
	}

	// The marker file should have been written before the
	// SIGTERM arrived (the `echo $! > ...` runs before the
	// `wait`).
	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse marker PID %q: %v", pidBytes, err)
	}
	// The spawned sleep is dead: `kill -0 PID` returns ESRCH.
	killProbe := exec.Command("kill", "-0", strconv.Itoa(pid))
	if killOut, killErr := killProbe.CombinedOutput(); killErr == nil {
		t.Fatalf("orphan: kill -0 %d succeeded (output=%q); sleep child survived teardown",
			pid, killOut)
	}
}

// TestShell_TimeoutEscalatesToSIGKILL: a child that traps SIGTERM
// (`trap '' TERM; sleep 60`) ignores the harness's first signal.
// The grace goroutine then escalates to SIGKILL after
// terminateGrace (2s). After Execute returns,
// TerminationReason == "escalated" and the test wall-clock is
// bounded at ~3 seconds (terminateGrace + SIGKILL propagation +
// cmd.Wait), NOT 60 seconds (the natural sleep duration).
//
// This is the second half of GOAL §5 reviewer duty #2 —
// "Timeout evidence distinguishes SIGTERM honored from SIGKILL
// escalation".
func TestShell_TimeoutEscalatesToSIGKILL(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		// trap '' TERM ignores SIGTERM. The outer sleep runs
		// for 60s; only SIGKILL can stop it.
		"command":    `sh -c 'trap "" TERM; sleep 60'`,
		"timeout_ms": 500,
	}}
	start := time.Now()
	res, err := sh.Execute(context.Background(), call)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "escalated" {
		t.Fatalf("TerminationReason = %q, want %q (trap '' TERM ignored SIGTERM; grace escalated to SIGKILL)",
			sr.TerminationReason, "escalated")
	}
	// Bound: terminateGrace (2s) + a small buffer for SIGKILL
	// propagation and cmd.Wait. Anything close to 60s means
	// the grace did NOT fire.
	if elapsed > 4*time.Second {
		t.Fatalf("elapsed = %v, want < 4s (grace=2s + SIGKILL propagation; close to 60s means SIGKILL never fired)",
			elapsed)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("elapsed = %v, want >= 2s (grace must fire before SIGKILL)", elapsed)
	}
}

// TestShell_Cancellation: a parent context that is cancelled
// mid-flight causes the shell tool to SIGTERM the process group
// (the second cancellation source, in addition to timeout_ms).
// TerminationReason == "cancelled".
func TestShell_Cancellation(t *testing.T) {
	sh := Shell{}
	ctx, cancel := context.WithCancel(context.Background())
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command": "sleep 60",
	}}
	// Cancel after a short delay so the child is mid-flight
	// when cancel fires.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := sh.Execute(ctx, call)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "cancelled" {
		t.Fatalf("TerminationReason = %q, want %q (ctx cancel must SIGTERM the group)",
			sr.TerminationReason, "cancelled")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, want < 3s (cancel landed mid-flight; sleep 60 must NOT run to completion)",
			elapsed)
	}
}

// TestShell_OutputSizeCap: a command that emits >cap bytes with
// max_output_bytes=100 produces a captured stdout of at most 100
// bytes + the truncation marker. The child is NOT killed — the
// cap is on capture, not on the process. TerminationReason is
// empty (normal exit).
func TestShell_OutputSizeCap(t *testing.T) {
	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command":          `sh -c 'yes A | head -c 5000'`, // 5000 bytes of "A"s
		"max_output_bytes": 100,
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "" {
		t.Fatalf("TerminationReason = %q, want empty (cap is on capture, not on the process; the child completed normally)",
			sr.TerminationReason)
	}
	// The captured stdout is bounded by cap + marker length.
	// We do NOT assert strict equality because the marker
	// length depends on the cap value (decimal digits).
	if len(sr.Stdout) > 100+len(truncateMarkerFor(100)) {
		t.Fatalf("len(Stdout) = %d, want <= %d (cap=100 + marker)",
			len(sr.Stdout), 100+len(truncateMarkerFor(100)))
	}
	if len(sr.Stdout) < 100 {
		t.Fatalf("len(Stdout) = %d, want >= 100 (cap=100; first 100 bytes should be captured before truncation)",
			len(sr.Stdout))
	}
}

// TestShell_TruncationMarker: the canonical truncation marker
// is appended to the captured stdout when the cap fires. The
// marker content is the binding contract — see cappedWriter.
func TestShell_TruncationMarker(t *testing.T) {
	sh := Shell{}
	capVal := 50
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		// printf 'A%.0s' $(seq 1 5000) emits 5000 'A' bytes
		// with no newlines — the captured prefix should be
		// byte-for-byte the first capVal 'A' bytes.
		"command":          `printf 'A%.0s' $(seq 1 5000)`,
		"max_output_bytes": capVal,
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	marker := truncateMarkerFor(capVal)
	if !strings.HasSuffix(sr.Stdout, marker) {
		t.Fatalf("Stdout does not end with the canonical truncation marker.\n  got suffix: %q\n  want suffix: %q\n  full Stdout: %q",
			sr.Stdout[len(sr.Stdout)-min(80, len(sr.Stdout)):], marker, sr.Stdout)
	}
	// The captured prefix (before the marker) should be
	// parseable as repeated "A" bytes — proves the captured
	// bytes are the FIRST cap bytes of the child's stdout,
	// not arbitrary truncation.
	prefix := strings.TrimSuffix(sr.Stdout, marker)
	if len(prefix) != capVal {
		t.Fatalf("prefix length = %d, want %d (cap=%d; first %d bytes should be captured verbatim)",
			len(prefix), capVal, capVal, capVal)
	}
	for i, b := range []byte(prefix) {
		if b != 'A' {
			t.Fatalf("prefix[%d] = %q, want 'A' (the captured prefix should be the first %d bytes of printf output)",
				i, b, capVal)
		}
	}
}

// TestShell_NoOrphanSurvives: the SCOPE §27 reviewer-duty-#1
// full proof that the process table has no harness-owned
// children after teardown. The command spawns a `sleep 60`
// backgrounded child, writes its PID to a marker file, and
// waits. With timeout_ms=200, the harness SIGTERMs the whole
// process group; the spawned sleep is killed along with the
// outer shell. After Execute returns, the test:
//
//  1. Reads the marker file to recover the spawned sleep's
//     PID.
//  2. Calls `kill -0 <pid>` and asserts it returns non-zero
//     (ESRCH — no such process). This is the direct
//     proof: the PID is no longer alive.
//  3. Calls `pgrep -f "<marker>"` and asserts it returns no
//     rows. This is the table-inspection proof: no surviving
//     process has the unique marker in its command line.
//
// Together (1) and (3) pin the SCOPE §27 orphan-survival
// contract: a terminated harness leaves no orphaned
// harness-owned processes.
func TestShell_NoOrphanSurvives(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "orphan-marker.pid")
	// The marker string is generated at runtime so it does
	// NOT appear in the test runner's own argv (a literal
	// string would survive in `bash -c 'go test ...'` and
	// make pgrep find the test runner, not an orphan).
	// "shell-orphan-survival-" + a unique suffix is the
	// stable identifier that survives process renaming
	// (via `exec -a`).
	markerString := fmt.Sprintf("shell-orphan-survival-%d", time.Now().UnixNano())
	// Use bash so the `exec -a` builtin can rename the
	// backgrounded sleep's argv[0] to the marker string —
	// `sh` (dash) does not support `exec -a`. The backgrounded
	// sleep inherits bash's process group (Setpgid:true at the
	// harness boundary makes bash's PGID == bash's PID), so
	// SIGTERM-to-PGID kills both.
	command := fmt.Sprintf(`bash -c 'exec -a "%s" sleep 60 & echo $! > %s; wait'`, markerString, marker)

	sh := Shell{}
	call := tools.Call{Name: "shell", Arguments: map[string]any{
		"command":    command,
		"timeout_ms": 200,
	}}
	res, err := sh.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want %q (error=%+v)", res.Status, "ok", res.Error)
	}
	sr, ok := res.Content.(ShellResult)
	if !ok {
		t.Fatalf("Content type = %T, want ShellResult", res.Content)
	}
	if sr.TerminationReason != "timeout" {
		t.Fatalf("TerminationReason = %q, want %q (timeout_ms=200 must SIGTERM the group)",
			sr.TerminationReason, "timeout")
	}

	// (1) Direct PID proof: the spawned sleep is dead.
	pidBytes, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("read marker file: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse marker PID %q: %v", pidBytes, parseErr)
	}
	killProbe := exec.Command("kill", "-0", strconv.Itoa(pid))
	if killOut, killErr := killProbe.CombinedOutput(); killErr == nil {
		t.Fatalf("(1) direct proof failed: kill -0 %d succeeded (output=%q); sleep child survived teardown",
			pid, killOut)
	}

	// (2) Process-table proof: no surviving process has the
	// unique marker string in its command line. We exclude
	// pgrep itself (which has the marker in its argv because
	// we pass it as an argument).
	pgrep := exec.Command("pgrep", "-f", markerString)
	pgrepOut, pgrepErr := pgrep.CombinedOutput()
	if pgrepErr == nil && len(pgrepOut) > 0 {
		t.Fatalf("(2) process-table proof failed: pgrep -f %q found %q; orphan survived teardown",
			markerString, pgrepOut)
	}
}
