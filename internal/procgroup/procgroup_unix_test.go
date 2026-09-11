//go:build !windows

package procgroup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSignalTerminatesTheGroupAndToleratesGone: a child started with
// Attr() dies on Signal(SIGTERM), and signalling a group that is gone is
// not an error.
func TestSignalTerminatesTheGroupAndToleratesGone(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = Attr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := Signal(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("child did not exit after SIGTERM to its group")
	}
	if err := Signal(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("signalling a gone group must be nil, got %v", err)
	}
}

// TestSignalReachesSurvivorsAfterTheLeaderExited pins the case that
// made the first implementation hang: the leader (a shell) backgrounds
// a child and exits; Signal must still reach the child through the
// group id, which equals the dead leader's pid.
func TestSignalReachesSurvivorsAfterTheLeaderExited(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $! > "+pidfile+"; exit 0")
	cmd.SysProcAttr = Attr()
	if err := cmd.Run(); err != nil { // leader has exited and is reaped
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if child == 0 {
		t.Fatalf("no child pid recorded")
	}
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("survivor not alive before Signal: %v", err)
	}
	if err := Signal(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("Signal via dead leader: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(child, 0); err != nil {
			return // gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("survivor %d still alive after Signal to the dead leader's group", child)
}
