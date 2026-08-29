package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriter_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{BaseURL: "http://x", Model: "m", Workspace: "/w", Permission: "READ_ONLY"}
	w, err := NewWriter(dir, "sid-test", cfg)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.AppendMessage("user", "hello"); err != nil {
		t.Fatalf("AppendMessage user: %v", err)
	}
	if err := w.AppendMessage("assistant", "hi"); err != nil {
		t.Fatalf("AppendMessage assistant: %v", err)
	}
	if err := w.Write(StatusCompleted, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// session.json parses and matches expected fields.
	b, err := os.ReadFile(filepath.Join(w.SessionDir(), "session.json"))
	if err != nil {
		t.Fatalf("read session.json: %v", err)
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal session.json: %v", err)
	}
	if s.SessionID != "sid-test" {
		t.Errorf("SessionID=%q want sid-test", s.SessionID)
	}
	if s.Status != StatusCompleted {
		t.Errorf("Status=%q want %q", s.Status, StatusCompleted)
	}
	if s.ExitCode != 0 {
		t.Errorf("ExitCode=%d want 0", s.ExitCode)
	}
	if s.Config.Workspace != "/w" {
		t.Errorf("Config.Workspace=%q want /w", s.Config.Workspace)
	}
	if s.EventsPath != "events.jsonl" {
		t.Errorf("EventsPath=%q want events.jsonl", s.EventsPath)
	}
	// messages.jsonl has exactly two JSON objects on two lines.
	mb, err := os.ReadFile(filepath.Join(w.SessionDir(), "messages.jsonl"))
	if err != nil {
		t.Fatalf("read messages.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(mb), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("messages.jsonl has %d lines, want 2; raw=%q", len(lines), mb)
	}
	var m0 Message
	if err := json.Unmarshal([]byte(lines[0]), &m0); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if m0.Role != "user" || m0.Content != "hello" {
		t.Errorf("line 0: role=%q content=%q want user/hello", m0.Role, m0.Content)
	}
	var m1 Message
	if err := json.Unmarshal([]byte(lines[1]), &m1); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}
	if m1.Role != "assistant" || m1.Content != "hi" {
		t.Errorf("line 1: role=%q content=%q want assistant/hi", m1.Role, m1.Content)
	}
}

func TestWriter_InterruptedStatus(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(dir, "sid-int", Config{Model: "m", Permission: "READ_ONLY"})
	if err := w.Write(StatusInterrupted, 6); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(w.SessionDir(), "session.json"))
	var s Session
	_ = json.Unmarshal(b, &s)
	if s.Status != StatusInterrupted || s.ExitCode != 6 {
		t.Errorf("Status=%q ExitCode=%d want interrupted/6", s.Status, s.ExitCode)
	}
}

func TestWriter_FailedStatus(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(dir, "sid-fail", Config{Model: "m", Permission: "READ_ONLY"})
	if err := w.Write(StatusFailed, 3); err != nil {
		t.Fatalf("Write: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(w.SessionDir(), "session.json"))
	var s Session
	_ = json.Unmarshal(b, &s)
	if s.Status != StatusFailed || s.ExitCode != 3 {
		t.Errorf("Status=%q ExitCode=%d want failed/3", s.Status, s.ExitCode)
	}
}

func TestWriter_AtomicWrite(t *testing.T) {
	// After Write returns, session.json exists and .tmp does not.
	dir := t.TempDir()
	w, _ := NewWriter(dir, "sid-atomic", Config{Model: "m", Permission: "READ_ONLY"})
	if err := w.Write(StatusCompleted, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.SessionDir(), "session.json")); err != nil {
		t.Errorf("session.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.SessionDir(), "session.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("session.json.tmp should be absent after atomic rename, got err=%v", err)
	}
}

func TestWriter_EmptyStateDir(t *testing.T) {
	// NewWriter("", ...) returns an error, not a panic.
	if _, err := NewWriter("", "sid", Config{}); err == nil {
		t.Fatal("NewWriter with empty stateDir returned nil error")
	}
}

func TestWriter_EmptySessionID(t *testing.T) {
	// NewWriter(dir, "", ...) returns an error, not a panic.
	if _, err := NewWriter(t.TempDir(), "", Config{}); err == nil {
		t.Fatal("NewWriter with empty sessionID returned nil error")
	}
}
