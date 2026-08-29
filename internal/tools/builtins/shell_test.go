package builtins

import (
	"context"
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
		t.Fatalf("TerminationReason = %q, want empty in handoff 020", sr.TerminationReason)
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
