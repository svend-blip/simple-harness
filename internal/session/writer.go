package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer accumulates a session's record and persists it on Close.
// The Writer is NOT safe for concurrent use; the cmd-side serializes
// message appends (one AppendMessage per goroutine at a time, in the
// same goroutine that calls Write). The mutex is a defence-in-depth
// against future callers; today no goroutine shares the Writer.
//
// The Writer owns two files under <stateDir>/<sessionID>/:
//   - session.json  — written atomically on Write()
//   - messages.jsonl — appended on every AppendMessage()
//
// The events.jsonl file is owned by the event.Emitter; the Writer
// records its relative path in Session.EventsPath but does not
// touch the file.
type Writer struct {
	mu         sync.Mutex
	stateDir   string
	sessionID  string
	sessionDir string
	startedAt  time.Time
	endedAt    time.Time
	cfg        Config
	messages   []Message
	msgsFile   *os.File
}

// NewWriter opens a fresh Writer at <stateDir>/<sessionID>/.
// It creates the session directory if missing, opens messages.jsonl
// for append, and returns the Writer. The stateDir is created with
// mode 0o755 (the same perm mode the existing <workspace>/sessions/
// sidecar uses).
//
// Returns an error if the state directory or session directory
// cannot be created, or if messages.jsonl cannot be opened.
func NewWriter(stateDir, sessionID string, cfg Config) (*Writer, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("session: state-dir must not be empty")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session: session-id must not be empty")
	}
	sessionDir := filepath.Join(stateDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", sessionDir, err)
	}
	msgsPath := filepath.Join(sessionDir, "messages.jsonl")
	mf, err := os.OpenFile(msgsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", msgsPath, err)
	}
	return &Writer{
		stateDir:   stateDir,
		sessionID:  sessionID,
		sessionDir: sessionDir,
		startedAt:  time.Now().UTC(),
		cfg:        cfg,
		msgsFile:   mf,
	}, nil
}

// AppendMessage adds a message to messages.jsonl. One JSON object
// per line, no trailing newline concerns (json.Encoder.Encode adds
// the newline). The timestamp is auto-stamped if zero.
func (w *Writer) AppendMessage(role, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := Message{
		Timestamp: time.Now().UTC(),
		Role:      role,
		Content:   content,
	}
	if err := json.NewEncoder(w.msgsFile).Encode(m); err != nil {
		return fmt.Errorf("session: encode message: %w", err)
	}
	return nil
}

// Write persists session.json atomically (write to .tmp + rename).
// It closes the messages.jsonl file handle. Call exactly once at
// session end.
//
// status is one of StatusCompleted / StatusInterrupted / StatusFailed.
// exitCode is the SCOPE §28 exit code the harness will return.
func (w *Writer) Write(status Status, exitCode int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.endedAt = time.Now().UTC()
	s := Session{
		SessionID:  w.sessionID,
		StartedAt:  w.startedAt,
		EndedAt:    w.endedAt,
		Status:     status,
		ExitCode:   exitCode,
		Config:     w.cfg,
		EventsPath: "events.jsonl",
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("session: marshal session.json: %w", err)
	}
	sessionPath := filepath.Join(w.sessionDir, "session.json")
	tmp := sessionPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("session: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, sessionPath); err != nil {
		return fmt.Errorf("session: rename %s -> %s: %w", tmp, sessionPath, err)
	}
	if w.msgsFile != nil {
		if err := w.msgsFile.Sync(); err != nil {
			return fmt.Errorf("session: sync messages.jsonl: %w", err)
		}
		if err := w.msgsFile.Close(); err != nil {
			return fmt.Errorf("session: close messages.jsonl: %w", err)
		}
		w.msgsFile = nil
	}
	return nil
}

// SessionDir returns the absolute path of the session directory.
// Useful for the cmd-side to construct the events.jsonl path
// (`<session-dir>/events.jsonl`) and to record EventsPath.
func (w *Writer) SessionDir() string {
	return w.sessionDir
}

// Close aborts the writer without writing session.json. It closes
// the messages.jsonl file handle. Use this on paths where the
// session record should NOT be persisted (e.g. the stdin-policy
// sentinel `--prompt-file -` returns 0 with no events; that path
// may close the writer without Write).
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.msgsFile != nil {
		if err := w.msgsFile.Close(); err != nil {
			return fmt.Errorf("session: close messages.jsonl: %w", err)
		}
		w.msgsFile = nil
	}
	return nil
}
