// Package procgroup starts a subprocess in its own process group and
// signals that whole group, on every platform simple-harness builds for.
// The shell tool and the MCP stdio transport need exactly this pair — "own the subprocess
// tree" at Start and "terminate the tree, not simple-harness" at Stop — and
// each one had it inlined with syscall.Setpgid/Getpgid/Kill, which do
// not exist on Windows, so simple-harness was Linux-only by construction.
package procgroup
