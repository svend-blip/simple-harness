package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/svend-blip/simple-harness/internal/loop"
)

// captureSkill is the test helper for handoff 032's TestRun_Skill_*
// suite. It mirrors captureSessions (sessions_test.go): saves +
// restores os.Stdout / os.Stderr, redirects them to pipes, runs
// run(args), drains the pipes into buffers, and returns the run()
// exit code + the captured stdout + the captured stderr.
//
// The helper is PRIVATE to skill_test.go (lowercase, file-scoped,
// not exported). The "no shared helper exported" constraint from
// handoff 031's verdict carries forward: main_test.go and
// sessions_test.go each keep their own capture helpers; this file
// has its own. This helper MUST NOT be referenced from main_test.go
// and MUST NOT be exported to other test files.
func captureSkill(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()

	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW

	code = run(args)

	_ = outW.Close()
	_ = errW.Close()
	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return code, outBuf.String(), errBuf.String()
}

// writeSkillFixture writes a SKILL.md file at
// <root>/.simple-harness/skills/<name>/SKILL.md with the given
// content. Returns the directory used (root). Used by the
// workspace + home search-root setup paths.
func writeSkillFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".simple-harness", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, "SKILL.md"), err)
	}
}

// writeOverrideSkillFixture writes a SKILL.md file at
// <root>/<name>/SKILL.md (NO .simple-harness prefix — the
// --skills-dir override is a literal directory). Returns the root.
func writeOverrideSkillFixture(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(dir, "SKILL.md"), err)
	}
}

// TestRun_Skill_UnknownExits2 is the TG1 verbatim path: a
// `--skill` name that resolves to no file under the active search
// roots exits 2 with stderr containing "unknown skill". The test
// uses an unreachable endpoint (port 9) so the run WOULD proceed
// to the model client if the --skill validation passed — the
// skill-checkpoint is the "fail fast" gate per GOAL §2.
//
// Mirrors the GOAL §6 TG1 command shape:
//
//	./bin/simple-harness run --base-url http://127.0.0.1:9 --model tg \
//	    --workspace /tmp --permission read_only \
//	    --prompt-file ... --skill no-such-skill-tg9
//
// but driven through the inner run() entry point so the binary
// does not need to be on disk for the test.
func TestRun_Skill_UnknownExits2(t *testing.T) {
	ws := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, _, stderr := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", ws,
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--skill", "no-such-skill-tg9",
	})
	if code != 2 {
		t.Fatalf("run --skill no-such-skill-tg9 returned %d, want 2 (config error) (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "unknown skill") {
		t.Fatalf("run --skill no-such-skill-tg9 stderr missing %q; got %q", "unknown skill", stderr)
	}
}

// TestRun_Skill_NoSkill_FlagIsOptional is the regression pin: the
// --skill flag is optional. A run WITHOUT --skill must proceed
// past the skill-checkpoint and reach the existing prompt-file
// validation / model-client path. This is the "V1 --skill is
// opt-in" contract; a future regression that makes --skill
// required would break every existing run-mode invocation.
//
// The test uses an unreachable endpoint (port 9) so the run
// reaches the model client, fails on connect, and exits 3
// (SCOPE §28 model/API failure). Exit 3 is the proof that the
// skill-checkpoint passed and the run progressed to the model
// call — the assertion is NOT "exit 3", it's "the run was not
// blocked at the skill-checkpoint".
func TestRun_Skill_NoSkill_FlagIsOptional(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, _, stderr := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		// no --skill flag
	})
	if code == 2 {
		t.Fatalf("run without --skill returned 2 (config error); the --skill flag must be optional (stderr=%q)", stderr)
	}
}

// TestRun_Skill_SkillsDirOverride covers two cases:
//
// (a) when --skills-dir <tmp> points at a temp dir holding
//     cold-start/SKILL.md, --skill cold-start resolves via the
//     override (Source="override") — the run proceeds past the
//     skill-checkpoint to the unreachable-endpoint (exit 3).
//
// (b) when --skills-dir points at an empty / missing dir, an
//     unknown --skill still produces exit 2 with the
//     "unknown skill" stderr message. The override REPLACES
//     both default roots — when the override is empty, even a
//     well-known skill name (cold-start) resolves nowhere.
func TestRun_Skill_SkillsDirOverride(t *testing.T) {
	// Case (a): --skills-dir override resolves cold-start.
	overrideDir := t.TempDir()
	writeOverrideSkillFixture(t, overrideDir, "cold-start", "OVERRIDE-RESOLVED cold-start content\n")
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code, _, stderr := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--skills-dir", overrideDir,
		"--skill", "cold-start",
	})
	if code == 2 {
		t.Fatalf("run --skills-dir (override) --skill cold-start returned 2 (config error); the override should resolve (stderr=%q)", stderr)
	}

	// Case (b): --skills-dir points at an empty / missing dir;
	// the canonical cold-start name is unknown there.
	emptyDir := t.TempDir()
	promptFile2 := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile2, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	code2, _, stderr2 := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile2,
		"--skills-dir", emptyDir,
		"--skill", "cold-start",
	})
	if code2 != 2 {
		t.Fatalf("run --skills-dir (empty) --skill cold-start returned %d, want 2 (config error) (stderr=%q)", code2, stderr2)
	}
	if !strings.Contains(stderr2, "unknown skill") {
		t.Fatalf("run --skills-dir (empty) --skill cold-start stderr missing %q; got %q", "unknown skill", stderr2)
	}
}

// TestRun_Skill_SourceWinsOnCollision: write the same skill name
// under both <ws>/.simple-harness/skills/<name>/SKILL.md and
// <home>/.simple-harness/skills/<name>/SKILL.md with
// distinguishable content. The run with --skill <name> and
// HOME pointed at the home dir, WORKSPACE pointed at the ws dir,
// must resolve the workspace's content (SCOPE §15 binding
// contract).
//
// For handoff 032 the model-context composition is NOT yet wired
// (handoff 033's work), so this test verifies the run PROCEEDS
// past the skill-checkpoint (exit code is not 2; it is the
// unreachable-endpoint exit 3). The binding content-pin — that
// the workspace's bytes land in the model context and the
// global's bytes do not — is handoff 033's contract to satisfy
// via the composition wiring.
func TestRun_Skill_SourceWinsOnCollision(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	writeSkillFixture(t, ws, "cold-start", "WS-COLLISION-WINS-PIN-cc11\n")
	writeSkillFixture(t, home, "cold-start", "HOME-COLLISION-LOSES-PIN-dd22\n")

	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	// HOME=<home>; WORKSPACE=<ws>; --skill cold-start with no
	// --skills-dir (so default roots are active). The run must
	// proceed past the skill-checkpoint (not exit 2); the
	// content-pin (workspace wins) is enforced by the package's
	// TestLoad_WorkspaceWinsOnCollision in internal/skill/skill_test.go.
	code, _, stderr := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", ws,
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--skill", "cold-start",
	})
	if code == 2 {
		t.Fatalf("run --skill (workspace+home collision) returned 2 (config error); the resolution should pass (stderr=%q)", stderr)
	}
}

// TestSkill_NoStartupNamesInRuntime is REVIEWER DUTY #1
// (SCOPE §16): the names "README", "GOAL.md", "DIRECTION.md"
// must NOT appear hardcoded in the harness runtime source. The
// names live ONLY in skill files (the cold-start reference skill
// in particular). The test walks the runtime source tree and
// asserts ZERO hits for these literals in the runtime surface.
//
// Scope of the scan:
//
//	cmd/simple-harness/*.go  EXCLUDING skill.go + skill_test.go
//	                          (the new skill-package integration;
//	                          not runtime — runtime = the rest of
//	                          the cmd surface)
//	internal/*/*.go          EXCLUDING internal/skill/*
//	                          (the skill loader is the loader, not
//	                          the runtime — it may reference SCOPE
//	                          §16 by name in its docstrings)
//
// OUT of scope by design:
//
//	share/skills/cold-start/SKILL.md
//	    — the skill body, mentions the names BY DEFINITION
//	internal/skill/skill.go + internal/skill/skill_test.go
//	    — the loader's docstrings reference SCOPE §16; the loader
//	      does NOT consume the names
//	docs/, README.md, go.mod, scripts/test.sh, .gitignore
//	    — FROZEN this Run per the scope fence
func TestSkill_NoStartupNamesInRuntime(t *testing.T) {
	names := []string{"GOAL.md", "README", "DIRECTION.md"}

	// Resolve the project root by walking up from the test's CWD
	// (the cmd/simple-harness package directory). The test runs
	// with the package directory as PWD; the project root is two
	// levels up.
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectRoot := filepath.Clean(filepath.Join(pwd, "..", ".."))

	// cmdFiles: every .go file in cmd/simple-harness/ EXCEPT the
	// skill-integration files (the new package's tests are out of
	// runtime scope).
	cmdDir := filepath.Join(projectRoot, "cmd", "simple-harness")
	cmdEntries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", cmdDir, err)
	}
	var cmdFiles []string
	for _, e := range cmdEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		// Skip the skill-integration files: skill_test.go is the
		// new test file (this file itself); skill.go would be a
		// future cmd-side skill glue file if we add one. Today
		// neither exists at cmd-level for the runner; the guard
		// is defensive against a future handoff that adds them.
		if name == "skill.go" || name == "skill_test.go" {
			continue
		}
		cmdFiles = append(cmdFiles, filepath.Join(cmdDir, name))
	}

	// internalFiles: every .go file under internal/*/ EXCEPT the
	// internal/skill/* directory (the loader is the LOAD PATH,
	// not the runtime).
	internalDir := filepath.Join(projectRoot, "internal")
	internalEntries, err := os.ReadDir(internalDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", internalDir, err)
	}
	var internalFiles []string
	for _, e := range internalEntries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "skill" {
			// Loader — out of scope (the loader is not runtime).
			continue
		}
		pkgDir := filepath.Join(internalDir, e.Name())
		pkgEntries, err := os.ReadDir(pkgDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", pkgDir, err)
		}
		for _, pe := range pkgEntries {
			if pe.IsDir() || !strings.HasSuffix(pe.Name(), ".go") {
				continue
			}
			internalFiles = append(internalFiles, filepath.Join(pkgDir, pe.Name()))
		}
	}

	allFiles := append(cmdFiles, internalFiles...)
	if len(allFiles) == 0 {
		t.Fatalf("scan discovered no runtime source files (cwd=%s, root=%s)", pwd, projectRoot)
	}

	for _, path := range allFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Line-by-line scan so the failure message can name the
		// offending file:line (SCOPE §16 "the test fails with a
		// clear message naming the offending file:line when a hit
		// is found").
		for lineNo, line := range strings.Split(string(data), "\n") {
			for _, name := range names {
				if strings.Contains(line, name) {
					t.Errorf("SCOPE §16 violation: runtime source contains hardcoded %q at %s:%d\n  line: %s",
						name, path, lineNo+1, line)
				}
			}
		}
	}
}

// --- handoff 033: composition contract tests + reviewer duty #3 ---

// capturedChatRequest mirrors the OpenAI-compat chat-completions
// request body shape (the loop's ChatRequest.Messages). Only the
// fields the new tests assert on are decoded (Role, Content).
type capturedChatRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// runWithCapturingServer is the private helper for the new
// httptest-based content-injection tests. It spins up an
// httptest server whose handler JSON-decodes the request body
// into the capturedChatRequest pointed at by `out` and returns
// `data: [DONE]\n\n` so the loop completes cleanly, then runs
// run(args) with --base-url pointed at the server. Returns
// the run() exit code and stderr. The captured body is asserted
// by the caller.
//
// The helper is PRIVATE to skill_test.go — the no-shared-
// helper-exported constraint from handoff 031/032 carries
// forward.
func runWithCapturingServer(t *testing.T, args []string, out *capturedChatRequest) (code int, stderr string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(out); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode fail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// Splice srv.URL into --base-url. The handoff's test list
	// always passes --base-url explicitly in args; we replace
	// the placeholder value (or any value) with the test server
	// URL so the run-mode flag parser sees the new base.
	newArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--base-url" && i+1 < len(args) {
			newArgs = append(newArgs, "--base-url", srv.URL)
			i++
			continue
		}
		newArgs = append(newArgs, args[i])
	}

	origStdout := os.Stdout
	origStderr := os.Stderr
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	})

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = outW
	os.Stderr = errW

	code = run(newArgs)

	_ = outW.Close()
	_ = errW.Close()
	_, _ = io.Copy(io.Discard, outR)
	var errBuf bytes.Buffer
	_, _ = io.Copy(&errBuf, errR)
	return code, errBuf.String()
}

// TestSkill_NoPluginCreep is REVIEWER DUTY #3 (SCOPE §15 +
// GOAL §5 #3): the skill loader's Go source must NOT import any
// execution / dynamic-loading package. The test parses
// internal/skill/skill.go as a Go AST, walks the ImportSpec
// nodes, and asserts the import path's base does NOT match any
// of the dangerous prefixes. A future regression that introduces
// "os/exec", "plugin", "debug/elf", etc. into the loader fails
// the test by name.
//
// The test reports each violation with a clear "SCOPE §15
// violation: skill loader imports %q" message so the failure
// output names the offending import path explicitly.
func TestSkill_NoPluginCreep(t *testing.T) {
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectRoot := filepath.Clean(filepath.Join(pwd, "..", ".."))
	skillPath := filepath.Join(projectRoot, "internal", "skill", "skill.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, skillPath, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", skillPath, err)
	}

	dangerous := []string{
		"os/exec",
		"plugin",
		"debug/elf",
		"debug/gosym",
		"debug/dwarf",
	}

	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, d := range dangerous {
			if path == d {
				t.Errorf("SCOPE §15 violation: skill loader imports %q (file %s)", path, skillPath)
			}
		}
	}
}

// TestRun_Skill_ContentInjectedIntoModelContext is the binding
// wire pin for the SCOPE §14 composition contract. The test:
//
//   - writes a cold-start skill under --skills-dir <override>
//     with a known marker string
//   - spins up an httptest server whose handler captures the
//     incoming chat-completions request body and returns
//     data: [DONE]\n\n
//   - invokes run with --skill cold-start + --skills-dir
//     <override>
//   - asserts the captured request body's messages JSON contains
//     the marker string (the skill slot), contains a system
//     message with loop.HarnessSystem (the harness slot), and
//     contains the user prompt string (the user slot)
func TestRun_Skill_ContentInjectedIntoModelContext(t *testing.T) {
	const marker = "LOADED-SKILL-MARKER-cc11"
	const prompt = "PROMPT-FOR-SKILL-INJECTION-TEST-cc11"

	overrideDir := t.TempDir()
	writeOverrideSkillFixture(t, overrideDir, "cold-start", marker+"\n")
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	var captured capturedChatRequest
	code, stderr := runWithCapturingServer(t, []string{
		"run",
		"--base-url", "http://placeholder",
		"--model", "qwen",
		"--workspace", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--skills-dir", overrideDir,
		"--skill", "cold-start",
	}, &captured)
	if code != 0 {
		t.Fatalf("run returned %d (expected 0 against [DONE]); stderr=%q", code, stderr)
	}

	// Defaults: the harness-system text is loop.HarnessSystem;
	// the marker is the skill content; the prompt is the user
	// task. The capture should yield exactly 3 messages:
	// system (harness), system (skill), user (prompt).
	if len(captured.Messages) != 3 {
		t.Fatalf("captured %d messages, want 3 (messages=%+v)", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[0].Role != "system" || !strings.Contains(captured.Messages[0].Content, loop.HarnessSystem) {
		t.Errorf("messages[0] = %+v, want {system, contains loop.HarnessSystem}", captured.Messages[0])
	}
	if captured.Messages[1].Role != "system" || !strings.Contains(captured.Messages[1].Content, marker) {
		t.Errorf("messages[1] = %+v, want {system, contains %q}", captured.Messages[1], marker)
	}
	if captured.Messages[2].Role != "user" || captured.Messages[2].Content != prompt {
		t.Errorf("messages[2] = %+v, want {user, %q}", captured.Messages[2], prompt)
	}
}

// TestRun_Skill_SourceWinsOnCollision_ContentInContext pins
// SCOPE §15's workspace-wins-on-collision rule AT THE
// COMPOSITION LEVEL (not just the loader level). The test
// writes DISTINGUISHABLE skill content under both the
// workspace root and the HOME root, then asserts the workspace
// content lands in the model context and the global content
// is shadowed.
func TestRun_Skill_SourceWinsOnCollision_ContentInContext(t *testing.T) {
	const wsMarker = "WS-WINS-PIN-cc11"
	const homeMarker = "HOME-LOSES-PIN-dd22"

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)

	writeSkillFixture(t, ws, "cold-start", wsMarker+"\n")
	writeSkillFixture(t, home, "cold-start", homeMarker+"\n")

	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	var captured capturedChatRequest
	code, stderr := runWithCapturingServer(t, []string{
		"run",
		"--base-url", "http://placeholder",
		"--model", "qwen",
		"--workspace", ws,
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--skill", "cold-start",
	}, &captured)
	if code != 0 {
		t.Fatalf("run returned %d (expected 0 against [DONE]); stderr=%q", code, stderr)
	}

	// Walk the captured messages; wsMarker must appear (in a
	// system message), homeMarker must NOT appear anywhere.
	foundWS := false
	for _, m := range captured.Messages {
		if m.Role == "system" && strings.Contains(m.Content, wsMarker) {
			foundWS = true
		}
		if strings.Contains(m.Content, homeMarker) {
			t.Errorf("workspace shadow failed: HOME content %q appeared in a model-context message", homeMarker)
		}
	}
	if !foundWS {
		t.Errorf("workspace content %q not found in any system message (messages=%+v)", wsMarker, captured.Messages)
	}
}

// TestRun_System_BothFlags_Errors pins the mutual-exclusion of
// --system and --system-file. The test sets both, asserts exit 2,
// and asserts stderr contains "mutually exclusive".
func TestRun_System_BothFlags_Errors(t *testing.T) {
	tmp := t.TempDir()
	sysFile := filepath.Join(tmp, "system.txt")
	if err := os.WriteFile(sysFile, []byte("FILE-SYSTEM-CONTENT-cc11"), 0o644); err != nil {
		t.Fatalf("write sys file: %v", err)
	}
	promptFile := filepath.Join(tmp, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	code, _, stderr := captureSkill(t, []string{
		"run",
		"--base-url", "http://127.0.0.1:9",
		"--model", "tg",
		"--workspace", tmp,
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--system", "INLINE-CONTENT-cc11",
		"--system-file", sysFile,
	})
	if code != 2 {
		t.Fatalf("run --system + --system-file returned %d, want 2 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("run --system + --system-file stderr missing %q; got %q", "mutually exclusive", stderr)
	}
}

// TestRun_System_TextInjectedIntoContext pins the inline --system
// <text> flag at the wire level. The test sets --system to a
// marker string and asserts the captured request body's messages
// JSON contains the marker (in a system message).
func TestRun_System_TextInjectedIntoContext(t *testing.T) {
	const marker = "INLINE-SYSTEM-MARKER-cc11"

	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	var captured capturedChatRequest
	code, stderr := runWithCapturingServer(t, []string{
		"run",
		"--base-url", "http://placeholder",
		"--model", "qwen",
		"--workspace", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--system", marker,
	}, &captured)
	if code != 0 {
		t.Fatalf("run --system returned %d (expected 0 against [DONE]); stderr=%q", code, stderr)
	}

	found := false
	for _, m := range captured.Messages {
		if m.Role == "system" && strings.Contains(m.Content, marker) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("inline --system content %q not found in any system message (messages=%+v)", marker, captured.Messages)
	}
}

// TestRun_System_FileInjectedIntoContext pins the file-based
// --system-file <path> flag at the wire level. The test writes a
// tmpfile with a marker, invokes --system-file <tmpfile>, and
// asserts the marker appears in a system message in the
// captured request body.
func TestRun_System_FileInjectedIntoContext(t *testing.T) {
	const marker = "FILE-SYSTEM-MARKER-cc11"

	tmp := t.TempDir()
	sysFile := filepath.Join(tmp, "system.txt")
	if err := os.WriteFile(sysFile, []byte(marker), 0o644); err != nil {
		t.Fatalf("write sys file: %v", err)
	}
	promptFile := filepath.Join(tmp, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	var captured capturedChatRequest
	code, stderr := runWithCapturingServer(t, []string{
		"run",
		"--base-url", "http://placeholder",
		"--model", "qwen",
		"--workspace", tmp,
		"--state-dir", t.TempDir(),
		"--permission", "read_only",
		"--prompt-file", promptFile,
		"--system-file", sysFile,
	}, &captured)
	if code != 0 {
		t.Fatalf("run --system-file returned %d (expected 0 against [DONE]); stderr=%q", code, stderr)
	}

	found := false
	for _, m := range captured.Messages {
		if m.Role == "system" && strings.Contains(m.Content, marker) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--system-file content %q not found in any system message (messages=%+v)", marker, captured.Messages)
	}
}
