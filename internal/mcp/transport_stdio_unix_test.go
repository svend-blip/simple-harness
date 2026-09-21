//go:build !windows

package mcp

// The two tests that look at the child through POSIX eyes - its process
// group, Wait4, signal 0. They kept the whole package's tests from compiling
// on Windows (syscall.Getpgid is not there), which nobody saw until the suite
// first ran there (CI, 2026-09-21).

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

// TestMCP_TransportStdio_ProcessGroupOwnership: the SCOPE §27 +
// Run 005 process-group pin. Three assertions:
//
//	(a) the child is spawned with Setpgid:true
//	    (syscall.Getpgid(child.Pid) == child.Pid);
//	(b) Close reaps the child (no zombie — syscall.Wait4 with
//	    WNOHANG returns -1/ECHILD after Close returns);
//	(c) the long-running child is actually killed by Close
//	    (kill -0 returns ESRCH after Close returns).
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
