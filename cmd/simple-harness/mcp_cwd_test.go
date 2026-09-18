package main

import (
	"path/filepath"
	"testing"
)

// TestMCPServerDir — where a stdio MCP server is started: the
// workspace unless the declaration says otherwise, and a relative
// "cwd" is relative to the workspace, not to wherever the harness
// was launched from.
func TestMCPServerDir(t *testing.T) {
	ws := filepath.Join(string(filepath.Separator), "w", "proj")
	abs := filepath.Join(string(filepath.Separator), "srv", "state")
	for _, tc := range []struct{ name, cwd, want string }{
		{"absent is the workspace", "", ws},
		{"relative is under the workspace", "state", filepath.Join(ws, "state")},
		{"absolute stands", abs, abs},
	} {
		if got := mcpServerDir(tc.cwd, ws); got != tc.want {
			t.Errorf("%s: mcpServerDir(%q) = %q, want %q", tc.name, tc.cwd, got, tc.want)
		}
	}
}
