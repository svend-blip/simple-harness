package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// Shell is the shell builtin tool. It executes a shell command in
// its own process group and captures stdout, stderr, the exit code,
// and the elapsed duration. See SCOPE §§11, 27 for the contract;
// see the handoff 020 result file for the seam choices
// (SysProcAttr.Setpgid for process-group ownership, the
// ShellResult wire shape, the empty termination_reason in this
// handoff vs the timeout/cancelled/escalated values that land on
// 021).
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

// Meta implements tools.Tool.
func (Shell) Meta() tools.ToolMeta {
	return tools.ToolMeta{
		Name: "shell",
		Description: "Execute a shell command in its own process " +
			"group. Mutation tool — gated by the policy stage " +
			"(READ_ONLY denies; WORKSPACE_WRITE allows; " +
			"FULL_ACCESS allows). Captures stdout, stderr, exit " +
			"code, and duration. Timeout, cancellation, and " +
			"output-size cap land in handoff 021.",
	}
}

// Schema implements tools.Tool. The AdditionalProperties=false
// default rejects unknown fields. `command` is required — the
// tool's contract is "run this command", and a missing argument
// is a schema violation, not an implicit default. `cwd` is
// optional; when absent, the child inherits the harness's
// current working directory.
func (Shell) Schema() tools.Schema {
	return tools.Schema{
		Required: []string{"command"},
		Properties: map[string]tools.PropertyType{
			"command": tools.TypeString,
			"cwd":     tools.TypeString,
		},
	}
}

// ShellResult is the success content shape. Result.Content on
// success carries this struct; JSON tags match the wire format
// and downstream consumers parse the fields by name.
//
// ExitCode is the process's exit status (the conventional
// 0-on-success / non-zero-on-failure). The shell tool returns
// Result{Status:"ok"} even on a non-zero exit — the exit code
// is data, not an error. This matches the existing WriteFile
// and ApplyPatch conventions where the tool's structured
// content carries success-or-failure details.
//
// Stdout and Stderr are the captured bytes verbatim. Handoff
// 021 adds the byte-cap and the explicit truncation marker; in
// handoff 020 the captures are uncapped.
//
// Duration is the elapsed wall time formatted via
// time.Duration.String() (e.g. "42.1ms", "1.5s"). Downstream
// consumers parse it via time.ParseDuration.
//
// TerminationReason is empty in handoff 020. Handoff 021
// populates it with "timeout", "cancelled", or "escalated"
// for the corresponding teardown paths. The field is present
// in the JSON from day one so the wire shape is stable across
// handoffs.
type ShellResult struct {
	ExitCode          int    `json:"exit_code"`
	Stdout            string `json:"stdout"`
	Stderr            string `json:"stderr"`
	Duration          string `json:"duration"`
	TerminationReason string `json:"termination_reason,omitempty"`
}

// Execute implements tools.Tool. Algorithm:
//
//  1. Extract command (required string) and optional cwd.
//     Missing or non-string command returns a structured
//     schema_violation error (the pipeline's schema validator
//     normally catches this first; the defensive guard keeps
//     direct-Execute callers honest).
//  2. Construct exec.Command("sh", "-c", command) with
//     Dir=cwd (or empty for "inherit parent's cwd") and
//     SysProcAttr{Setpgid: true} so the child becomes the
//     leader of a new process group. This is the SCOPE §27
//     seam: the child PID == child PGID, which is what
//     handoff 021's syscall.Kill(-pgid, SIGTERM) targets.
//  3. Allocate bytes.Buffer for stdout and stderr; wire them
//     to the command's Stdout / Stderr fields BEFORE Start.
//  4. Record start := time.Now(); call cmd.Run(); compute
//     duration := time.Since(start).
//  5. Return Result{Status:"ok", Content: ShellResult{...}}
//     with ExitCode=cmd.ProcessState.ExitCode() (cmd.Run
//     populates this even on a non-zero exit; on a
//     start/wait failure, ExitCode is -1). Stdout and Stderr
//     are the buffers' bytes verbatim. Duration is the
//     formatted elapsed time. TerminationReason is "" in
//     handoff 020.
//  6. On a cmd.Run failure that is NOT a non-zero exit (e.g.
//     `sh` not found, which is extremely unlikely on Linux
//     but defensive), return
//     Result{Status:"error", Error:&ToolError{Kind:"execution_failed",...}}.
//
// ctx is accepted but ignored in handoff 020. Handoff 021
// wires ctx.Done() to the cancel/SIGTERM escalation path.
func (Shell) Execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	// ctx is reserved for the handoff-021 cancellation /
	// SIGTERM / SIGKILL escalation path. The handoff-020
	// happy path has no cancel/timeout behavior.
	_ = ctx

	command, ok := call.Arguments["command"].(string)
	if !ok || command == "" {
		return tools.Result{Status: "error", Error: &tools.ToolError{
			Kind:    "schema_violation",
			Message: "shell: missing or non-string command argument",
			Call:    call,
		}}, nil
	}
	cwd, _ := call.Arguments["cwd"].(string)
	// (Missing cwd is fine — exec.Cmd treats empty Dir as
	// "inherit parent's cwd".)

	cmd := exec.Command("sh", "-c", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	// On a clean run (including a non-zero exit), err is
	// *exec.ExitError but cmd.ProcessState is populated;
	// Surface *exec.ExitError as a normal "ok" with
	// ExitCode carrying the failure. Other errors (e.g.
	// `sh` not found) are surfaced as execution_failed.
	var exitCode int
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else {
		exitCode = -1
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		// Defensive: only fires if `sh` itself failed to
		// start. Unlikely on Linux but possible in
		// chroot/sandbox environments.
		return tools.Result{}, fmt.Errorf("shell: run %q: %w", command, err)
	}

	return tools.Result{Status: "ok", Content: ShellResult{
		ExitCode:          exitCode,
		Stdout:            stdout.String(),
		Stderr:            stderr.String(),
		Duration:          elapsed.String(),
		TerminationReason: "",
	}}, nil
}

// Compile-time assertion that Shell implements tools.Tool.
var _ tools.Tool = Shell{}
