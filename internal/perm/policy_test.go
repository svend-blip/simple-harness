package perm

import (
	"context"
	"testing"

	"github.com/svend-blip/simple-harness/internal/path"
	"github.com/svend-blip/simple-harness/internal/tools"
)

// tempWorkspaceForPolicy creates a temporary directory and returns a
// path.Workspace rooted at it. Mirrors the perm_test.go helper
// without sharing state across test files.
func tempWorkspaceForPolicy(t *testing.T) path.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := path.New(dir)
	if err != nil {
		t.Fatalf("path.New(%q): %v", dir, err)
	}
	return ws
}

// TestPolicy_ParseMode_RoundTrip: ParseMode accepts each of the three
// SCOPE §12 CLI values and rejects unknown values. Mode.String() inverts
// the mapping.
func TestPolicy_ParseMode_RoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"read_only", READ_ONLY},
		{"workspace_write", WORKSPACE_WRITE},
		{"full_access", FULL_ACCESS},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.in)
		if err != nil {
			t.Fatalf("ParseMode(%q) returned error %v, want nil", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseMode(%q) = %d, want %d", tc.in, got, tc.want)
		}
		if s := tc.want.String(); s != tc.in {
			t.Fatalf("Mode(%d).String() = %q, want %q", tc.want, s, tc.in)
		}
	}

	// Unknown values return errors and yield the zero Mode (READ_ONLY).
	if _, err := ParseMode("bogus"); err == nil {
		t.Fatalf("ParseMode(\"bogus\") returned nil error, want error")
	}
	if _, err := ParseMode(""); err == nil {
		t.Fatalf("ParseMode(\"\") returned nil error, want error")
	}
}

// TestPolicy_READ_ONLY_RejectsMutation: for each tool in
// mutationTools, Policy{READ_ONLY}.Decide returns a Deny with
// Reason="read-only-mode-rejects-mutation".
func TestPolicy_READ_ONLY_RejectsMutation(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	p := NewPolicy(READ_ONLY)
	for tool := range mutationTools {
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "in-workspace.txt"}}
		d := p.Decide(nilTestCtx(), call, ws)
		if d.Allowed {
			t.Fatalf("Policy{READ_ONLY}.Decide(%s, in-ws) = Allowed, want Denied", tool)
		}
		if d.Reason != "read-only-mode-rejects-mutation" {
			t.Fatalf("Policy{READ_ONLY}.Decide(%s) Reason = %q, want %q",
				tool, d.Reason, "read-only-mode-rejects-mutation")
		}
	}
}

// TestPolicy_READ_ONLY_AllowsReadOnly: for each of the four Run-003
// read-only tools, Policy{READ_ONLY}.Decide returns Allowed:true.
func TestPolicy_READ_ONLY_AllowsReadOnly(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	p := NewPolicy(READ_ONLY)
	for _, tool := range []string{"read_file", "list_directory", "search_files", "grep"} {
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "."}}
		d := p.Decide(nilTestCtx(), call, ws)
		if !d.Allowed {
			t.Fatalf("Policy{READ_ONLY}.Decide(%s) = Denied, want Allowed (Reason=%q)",
				tool, d.Reason)
		}
	}
}

// TestPolicy_WORKSPACE_WRITE_AllowsInWorkspace: for each mutation tool,
// with an in-workspace path, Policy{WORKSPACE_WRITE}.Decide returns
// Allowed:true.
func TestPolicy_WORKSPACE_WRITE_AllowsInWorkspace(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	p := NewPolicy(WORKSPACE_WRITE)
	for tool := range mutationTools {
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "in-workspace.txt"}}
		d := p.Decide(nilTestCtx(), call, ws)
		if !d.Allowed {
			t.Fatalf("Policy{WORKSPACE_WRITE}.Decide(%s, in-ws) = Denied (Reason=%q), want Allowed",
				tool, d.Reason)
		}
	}
}

// TestPolicy_WORKSPACE_WRITE_RejectsEscape: for each mutation tool,
// with args whose path is "../escape" or "/etc/passwd",
// Policy{WORKSPACE_WRITE}.Decide returns Deny with
// Reason="workspace-write-rejects-escape".
func TestPolicy_WORKSPACE_WRITE_RejectsEscape(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	p := NewPolicy(WORKSPACE_WRITE)
	for tool := range mutationTools {
		for _, badPath := range []string{"../escape", "/etc/passwd"} {
			call := tools.Call{Name: tool, Arguments: map[string]any{"path": badPath}}
			d := p.Decide(nilTestCtx(), call, ws)
			if d.Allowed {
				t.Fatalf("Policy{WORKSPACE_WRITE}.Decide(%s, path=%q) = Allowed, want Denied",
					tool, badPath)
			}
			if d.Reason != "workspace-write-rejects-escape" {
				t.Fatalf("Policy{WORKSPACE_WRITE}.Decide(%s, path=%q) Reason = %q, want %q",
					tool, badPath, d.Reason, "workspace-write-rejects-escape")
			}
		}
	}
}

// TestPolicy_FULL_ACCESS_AllowsEverything: for every tool (read-only
// and mutation), Policy{FULL_ACCESS}.Decide returns Allowed:true,
// including for a mutation tool whose path escapes the workspace.
func TestPolicy_FULL_ACCESS_AllowsEverything(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	p := NewPolicy(FULL_ACCESS)
	for _, tool := range []string{"read_file", "list_directory", "search_files", "grep"} {
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "."}}
		d := p.Decide(nilTestCtx(), call, ws)
		if !d.Allowed {
			t.Fatalf("Policy{FULL_ACCESS}.Decide(%s, in-ws) = Denied, want Allowed", tool)
		}
	}
	for tool := range mutationTools {
		// In-workspace path — allowed.
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "in-ws.txt"}}
		d := p.Decide(nilTestCtx(), call, ws)
		if !d.Allowed {
			t.Fatalf("Policy{FULL_ACCESS}.Decide(%s, in-ws) = Denied, want Allowed", tool)
		}
		// Even an escaping path — FULL_ACCESS allows it (no
		// silent escalation: the operator must explicitly
		// select --permission full_access).
		call = tools.Call{Name: tool, Arguments: map[string]any{"path": "../escape"}}
		d = p.Decide(nilTestCtx(), call, ws)
		if !d.Allowed {
			t.Fatalf("Policy{FULL_ACCESS}.Decide(%s, escape) = Denied, want Allowed (FULL_ACCESS is the explicit opt-in)",
				tool)
		}
	}
}

// TestPolicy_ZeroValueIsReadOnly: the zero value of Policy is
// equivalent to NewPolicy(READ_ONLY). Pins the SCOPE §12 "never silent
// escalation" rule — the harness never silently escalates by virtue
// of an uninitialized policy.
func TestPolicy_ZeroValueIsReadOnly(t *testing.T) {
	ws := tempWorkspaceForPolicy(t)
	var p Policy // zero value, Mode == 0 == READ_ONLY
	for tool := range mutationTools {
		call := tools.Call{Name: tool, Arguments: map[string]any{"path": "in-ws.txt"}}
		d := p.Decide(nilTestCtx(), call, ws)
		if d.Allowed {
			t.Fatalf("Policy{} (zero).Decide(%s, in-ws) = Allowed, want Denied (zero value is READ_ONLY)", tool)
		}
		if d.Reason != "read-only-mode-rejects-mutation" {
			t.Fatalf("Policy{}.Decide(%s) Reason = %q, want %q",
				tool, d.Reason, "read-only-mode-rejects-mutation")
		}
	}
}

// TestIsMutationTool: the mutation tool detector matches the
// mutationTools list exactly.
func TestIsMutationTool(t *testing.T) {
	for tool := range mutationTools {
		if !IsMutationTool(tool) {
			t.Fatalf("IsMutationTool(%q) = false, want true", tool)
		}
	}
	for _, tool := range []string{"read_file", "list_directory", "search_files", "grep", "no_such_tool"} {
		if IsMutationTool(tool) {
			t.Fatalf("IsMutationTool(%q) = true, want false", tool)
		}
	}
}

// nilTestCtx returns a context.Background(). It's a tiny helper to
// keep Decide call sites compact in the matrix tests above (the
// Decision logic does not consult the context).
func nilTestCtx() context.Context { return context.Background() }
