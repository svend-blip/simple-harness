package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svend-blip/simple-harness/internal/session"
)

// captureSessions is the test helper for handoff 031's TestSessions_*
// suite. It saves+restores os.Stdout / os.Stderr, redirects them to
// pipes, runs run(args), drains the pipes into buffers, and returns
// the run() exit code + the captured stdout + the captured stderr.
//
// The helper is private to sessions_test.go (file-scoped helper,
// not exported). main_test.go stays FROZEN — this helper is the
// local equivalent of the runCapture pattern main_test.go uses
// inline at TestToolsSubcommand_EmptyRegistry / TestPermissionFlag_*.
func captureSessions(t *testing.T, args []string) (code int, stdout, stderr string) {
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

// writeSessionFixture writes a session.Session s into
// <stateDir>/<id>/session.json as pretty-printed JSON. It is the
// helper the TestSessions_List_* / TestSessions_Show_* tests use
// to build deterministic fixtures without depending on the
// internal/session.Writer (which is the binding-contract producer,
// not the consumer — handoff 031's inspector reads via stdlib +
// json.Unmarshal per GOAL §5 #3 "no semantic-memory creep").
func writeSessionFixture(t *testing.T, stateDir, id string, s session.Session) {
	t.Helper()
	dir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestSessions_Help: `run([]string{"sessions"})` (no verb) returns
// non-zero and prints sessionsUsage to stderr. Smoke test for the
// missing-verb path (the TG2 pre-step).
func TestSessions_Help(t *testing.T) {
	code, _, stderr := captureSessions(t, []string{"sessions"})
	if code == 0 {
		t.Fatalf("run(sessions) returned 0, want non-zero (missing verb)")
	}
	if !strings.Contains(stderr, "Usage: simple-harness sessions") {
		t.Fatalf("run(sessions) stderr missing %q; got %q",
			"Usage: simple-harness sessions", stderr)
	}
}

// TestSessions_UnknownVerb: `run([]string{"sessions", "bogus"})`
// returns 1 and prints "unknown sessions verb" + sessionsUsage to
// stderr.
func TestSessions_UnknownVerb(t *testing.T) {
	code, _, stderr := captureSessions(t, []string{"sessions", "bogus"})
	if code != 1 {
		t.Fatalf("run(sessions bogus) returned %d, want 1", code)
	}
	if !strings.Contains(stderr, "unknown sessions verb") {
		t.Fatalf("run(sessions bogus) stderr missing %q; got %q",
			"unknown sessions verb", stderr)
	}
	if !strings.Contains(stderr, "Usage: simple-harness sessions") {
		t.Fatalf("run(sessions bogus) stderr missing sessionsUsage; got %q", stderr)
	}
}

// TestSessions_List_Empty: empty state-dir (use t.TempDir()).
// run(sessions list --state-dir <tmp>) returns 0 and prints nothing.
func TestSessions_List_Empty(t *testing.T) {
	tmp := t.TempDir()
	code, stdout, _ := captureSessions(t, []string{"sessions", "list", "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions list --state-dir empty) returned %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("run(sessions list --state-dir empty) stdout = %q, want empty", stdout)
	}
}

// TestSessions_List_Multiple: write three session.json files into a
// temp state-dir; run(sessions list --state-dir <tmp>) returns 0 and
// prints three ids sorted by started_at DESCENDING. Use distinct
// started_at to make the sort assertion deterministic.
func TestSessions_List_Multiple(t *testing.T) {
	tmp := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		id        string
		startedAt time.Time
	}{
		{"01930000-0000-7000-8000-000000000001", base},
		{"01930000-0000-7000-8000-000000000002", base.Add(1 * time.Hour)},
		{"01930000-0000-7000-8000-000000000003", base.Add(2 * time.Hour)},
	}
	for _, c := range cases {
		writeSessionFixture(t, tmp, c.id, session.Session{
			SessionID: c.id,
			StartedAt: c.startedAt,
			EndedAt:   c.startedAt.Add(time.Second),
			Status:    session.StatusCompleted,
			ExitCode:  0,
			Config: session.Config{
				BaseURL:    "http://example.invalid",
				Model:      "tg",
				Workspace:  "/tmp",
				Permission: "READ_ONLY",
			},
			EventsPath: "events.jsonl",
		})
	}

	code, stdout, _ := captureSessions(t, []string{"sessions", "list", "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions list --state-dir multi) returned %d, want 0", code)
	}

	wantLines := []string{
		"01930000-0000-7000-8000-000000000003", // most recent
		"01930000-0000-7000-8000-000000000002",
		"01930000-0000-7000-8000-000000000001", // oldest
	}
	gotLines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("run(sessions list) output line count = %d, want %d (stdout=%q)",
			len(gotLines), len(wantLines), stdout)
	}
	for i, want := range wantLines {
		if gotLines[i] != want {
			t.Fatalf("run(sessions list) line %d = %q, want %q (full stdout=%q)",
				i, gotLines[i], want, stdout)
		}
	}
}

// TestSessions_List_SkipsCorrupt: write two valid session.json
// files and one corrupt session.json (random garbage) into the
// temp state-dir. run(sessions list --state-dir <tmp>) returns 0
// and prints only the two valid ids; the warning for the corrupt
// one is on stderr. Asserts the inspector is best-effort, not
// fail-fast.
func TestSessions_List_SkipsCorrupt(t *testing.T) {
	tmp := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	validID := "01930000-0000-7000-8000-0000000000aa"
	writeSessionFixture(t, tmp, validID, session.Session{
		SessionID: validID,
		StartedAt: base,
		EndedAt:   base.Add(time.Second),
		Status:    session.StatusCompleted,
		ExitCode:  0,
		Config: session.Config{
			BaseURL:    "http://example.invalid",
			Model:      "tg",
			Workspace:  "/tmp",
			Permission: "READ_ONLY",
		},
		EventsPath: "events.jsonl",
	})

	corruptID := "01930000-0000-7000-8000-0000000000bb"
	if err := os.MkdirAll(filepath.Join(tmp, corruptID), 0o755); err != nil {
		t.Fatalf("mkdir corrupt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, corruptID, "session.json"),
		[]byte("this is not json {{{"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	code, stdout, stderr := captureSessions(t, []string{"sessions", "list", "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions list --state-dir corrupt) returned %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != validID {
		t.Fatalf("run(sessions list --state-dir corrupt) stdout = %q, want %q",
			stdout, validID)
	}
	if !strings.Contains(stderr, "warning: skipping") {
		t.Fatalf("run(sessions list --state-dir corrupt) stderr missing warning; got %q", stderr)
	}
}

// TestSessions_List_NonexistentStateDir: pass a path that does not
// exist. run(sessions list --state-dir <tmp>/does-not-exist)
// returns 0 and prints nothing (matches the "empty state-dir" UX;
// this is the canonical `ls <missing>` behavior — listing a
// missing directory is not an error, it is empty).
func TestSessions_List_NonexistentStateDir(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")
	code, stdout, _ := captureSessions(t, []string{"sessions", "list", "--state-dir", missing})
	if code != 0 {
		t.Fatalf("run(sessions list --state-dir missing) returned %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("run(sessions list --state-dir missing) stdout = %q, want empty", stdout)
	}
}

// TestSessions_Show_PrintsSessionJSON: write one session.json into
// <tmp>/<id>/session.json. run(sessions show <id> --state-dir <tmp>)
// returns 0 and stdout contains a pretty-printed JSON with all the
// fields (status, exit_code, config, started_at, ended_at,
// session_id, events_path).
func TestSessions_Show_PrintsSessionJSON(t *testing.T) {
	tmp := t.TempDir()
	id := "01930000-0000-7000-8000-000000000ccc"
	started := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	writeSessionFixture(t, tmp, id, session.Session{
		SessionID: id,
		StartedAt: started,
		EndedAt:   started.Add(2 * time.Second),
		Status:    session.StatusFailed,
		ExitCode:  3,
		Config: session.Config{
			BaseURL:    "http://example.invalid",
			Model:      "tg",
			Workspace:  "/tmp",
			Permission: "READ_ONLY",
		},
		EventsPath: "events.jsonl",
	})

	code, stdout, _ := captureSessions(t, []string{"sessions", "show", id, "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions show <id>) returned %d, want 0", code)
	}
	for _, want := range []string{
		`"session_id": "` + id + `"`,
		`"status": "failed"`,
		`"exit_code": 3`,
		`"base_url": "http://example.invalid"`,
		`"model": "tg"`,
		`"workspace": "/tmp"`,
		`"permission": "READ_ONLY"`,
		`"events_path": "events.jsonl"`,
		`"started_at":`,
		`"ended_at":`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run(sessions show <id>) stdout missing %q; got %q", want, stdout)
		}
	}
}

// TestSessions_Show_NotFound: pass an id that has no <tmp>/<id>/
// directory. Returns 1; stderr contains "session not found".
func TestSessions_Show_NotFound(t *testing.T) {
	tmp := t.TempDir()
	id := "01930000-0000-7000-8000-000000000fff"
	code, _, stderr := captureSessions(t, []string{"sessions", "show", id, "--state-dir", tmp})
	if code != 1 {
		t.Fatalf("run(sessions show missing) returned %d, want 1", code)
	}
	if !strings.Contains(stderr, "session not found") {
		t.Fatalf("run(sessions show missing) stderr missing %q; got %q",
			"session not found", stderr)
	}
}

// TestSessions_Show_NoID: run(sessions show) returns 1; stderr
// contains the per-verb usage line.
func TestSessions_Show_NoID(t *testing.T) {
	code, _, stderr := captureSessions(t, []string{"sessions", "show"})
	if code != 1 {
		t.Fatalf("run(sessions show) returned %d, want 1", code)
	}
	if !strings.Contains(stderr, "Usage: simple-harness sessions show") {
		t.Fatalf("run(sessions show) stderr missing usage line; got %q", stderr)
	}
}

// TestSessions_Show_CorruptJSON: write garbage into <tmp>/<id>/
// session.json. Returns 1; stderr contains "parse".
func TestSessions_Show_CorruptJSON(t *testing.T) {
	tmp := t.TempDir()
	id := "01930000-0000-7000-8000-000000000ddd"
	dir := filepath.Join(tmp, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"),
		[]byte("garbage{{{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	code, _, stderr := captureSessions(t, []string{"sessions", "show", id, "--state-dir", tmp})
	if code != 1 {
		t.Fatalf("run(sessions show corrupt) returned %d, want 1", code)
	}
	if !strings.Contains(stderr, "parse") {
		t.Fatalf("run(sessions show corrupt) stderr missing %q; got %q", "parse", stderr)
	}
}

// TestSessions_Show_RoundTripsCanonicalSchema: write a session.json
// with all canonical fields populated (status=completed,
// exit_code=0, config with all 5 sub-fields, started_at / ended_at
// with UTC RFC3339, session_id=UUIDv7, events_path=events.jsonl).
// Read it back via runSessionsShow, parse the stdout JSON, assert
// every field byte-matches the input. This is the binding contract
// from handoff 030's "Public-surface changes" section.
func TestSessions_Show_RoundTripsCanonicalSchema(t *testing.T) {
	tmp := t.TempDir()
	id := "01930000-0000-7000-8000-000000abcdee"
	started := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	ended := time.Date(2026, 3, 1, 10, 30, 5, 0, time.UTC)
	want := session.Session{
		SessionID: id,
		StartedAt: started,
		EndedAt:   ended,
		Status:    session.StatusCompleted,
		ExitCode:  0,
		Config: session.Config{
			BaseURL:    "http://example.invalid",
			Model:      "tg",
			Workspace:  "/tmp/ws",
			Permission: "WORKSPACE_WRITE",
			OutputMode: "jsonl",
		},
		EventsPath: "events.jsonl",
	}
	writeSessionFixture(t, tmp, id, want)

	code, stdout, _ := captureSessions(t, []string{"sessions", "show", id, "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions show <id>) returned %d, want 0", code)
	}

	var got session.Session
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse stdout JSON: %v; stdout=%q", err, stdout)
	}
	if got.SessionID != want.SessionID {
		t.Fatalf("SessionID: got %q, want %q", got.SessionID, want.SessionID)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("StartedAt: got %v, want %v", got.StartedAt, want.StartedAt)
	}
	if !got.EndedAt.Equal(want.EndedAt) {
		t.Fatalf("EndedAt: got %v, want %v", got.EndedAt, want.EndedAt)
	}
	if got.Status != want.Status {
		t.Fatalf("Status: got %q, want %q", got.Status, want.Status)
	}
	if got.ExitCode != want.ExitCode {
		t.Fatalf("ExitCode: got %d, want %d", got.ExitCode, want.ExitCode)
	}
	if got.Config != want.Config {
		t.Fatalf("Config: got %+v, want %+v", got.Config, want.Config)
	}
	if got.EventsPath != want.EventsPath {
		t.Fatalf("EventsPath: got %q, want %q", got.EventsPath, want.EventsPath)
	}
}

// TestSessions_Show_NoSecretLeakage: write a session.json that
// contains a literal `api_key` value that the production cmd-side
// would have redacted (the test pins what the inspector emits when
// the underlying file happens to contain sensitive-looking content;
// today the production Writer never writes `api_key` to session.json
// because the cmd-side constructs session.Config from non-secret
// fields only — `cfg.Model.Model` + workspace + permission — and
// never touches `cfg.Model.APIKey`). The test writes a session.json
// that DOES contain a "secret" string in `config.model`, calls
// `runSessionsShow`, and asserts the JSON in stdout contains the
// secret verbatim (the inspector is a `cat`-style reader; it does
// not redact). This guards against a future regression where a
// careless edit accidentally adds redaction to the inspector
// (which would be a fence violation — the inspector is supposed
// to be a pure reader).
func TestSessions_Show_NoSecretLeakage(t *testing.T) {
	tmp := t.TempDir()
	id := "01930000-0000-7000-8000-000000abcdee"
	// Build the fixture manually (not via writeSessionFixture) so
	// we can include a "secret" string in config.model. The
	// inspector round-trips through json.Unmarshal -> session.
	// Session and then json.MarshalIndent; since session.Config
	// has a Model field, the secret value flows through verbatim.
	dir := filepath.Join(tmp, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw := `{
  "session_id": "` + id + `",
  "started_at": "2026-03-01T10:30:00Z",
  "ended_at": "2026-03-01T10:30:05Z",
  "status": "completed",
  "exit_code": 0,
  "config": {
    "base_url": "http://example.invalid",
    "model": "sk-leak-test-DO-NOT-EMBED",
    "workspace": "/tmp",
    "permission": "READ_ONLY"
  },
  "events_path": "events.jsonl"
}`
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	code, stdout, _ := captureSessions(t, []string{"sessions", "show", id, "--state-dir", tmp})
	if code != 0 {
		t.Fatalf("run(sessions show <id>) returned %d, want 0", code)
	}
	// The inspector is a pure `cat`-style reader — it does NOT
	// redact. The secret-looking model string flows through
	// verbatim. A future regression that adds redaction here
	// would silently break this test (and would be a fence
	// violation — redaction is the production-side Writer's
	// responsibility, not the inspector's).
	if !strings.Contains(stdout, "sk-leak-test-DO-NOT-EMBED") {
		t.Fatalf("run(sessions show <id>) stdout missing the verbatim secret; got %q", stdout)
	}
	if !strings.Contains(stdout, `"session_id": "`+id+`"`) {
		t.Fatalf("run(sessions show <id>) stdout missing session_id; got %q", stdout)
	}
}
