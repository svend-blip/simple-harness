package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot is the absolute path of the repository, resolved from this
// source file's location rather than from the working directory, so
// tests that read fixtures (example-project/, scripts/) keep working
// after TestMain moves the process out of the package directory.
var repoRoot string

// testBinary is a runtime binary built from the source under test,
// once per package run. The subprocess tests used to exec the
// committed bin/simple-harness-runtime, so they measured whatever
// had last been built and committed, not the code being tested.
var testBinary string

// TestMain makes the package hermetic. The config loader reads the
// user config from HOME and searches for a project config upward from
// the working directory; under `go test` that directory is the
// package source dir, whose ancestor on a developer machine is the
// developer's real ~/.simple-harness. With a real MCP server declared
// there, every test that dispatched `run` connected to it and
// registered its tools into the process-global registry — which is
// why TestMCPLight_GetGovernanceIndex passed alone and failed in the
// full run (its stub tool collided with the real server's and landed
// under the prefixed name). Both discovery roots now point at fresh
// temporary directories; tests that need a specific HOME or cwd still
// set their own.
func TestMain(m *testing.M) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	home, err := os.MkdirTemp("", "simple-harness-test-home-")
	if err != nil {
		panic(err)
	}
	cwd, err := os.MkdirTemp("", "simple-harness-test-cwd-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	if err := os.Chdir(cwd); err != nil {
		panic(err)
	}
	testBinary = filepath.Join(cwd, "simple-harness-runtime")
	build := exec.Command("go", "build", "-o", testBinary, "./cmd/simple-harness")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build test binary: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.RemoveAll(cwd)
	os.Exit(code)
}
