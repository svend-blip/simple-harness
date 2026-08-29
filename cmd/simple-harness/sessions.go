// Package main's sessions subcommand: read-only inspection of
// session records persisted by the run-mode and interactive-mode
// handoffs of Run 008 (handoff 030).
//
// The dispatch entry is `runSessions(args) int`; it parses the
// verb ("list" or "show") and delegates. Both verbs are
// READ-ONLY — no resume, no mutation. Per GOAL §2 "Read-only
// inspection — no resume in this Run unless it falls out safely"
// the inspector never writes to the session directory.
//
// The wire shape consumed is the session.json schema produced by
// internal/session.Writer (handoff 030's binding contract):
//
//	{
//	  "session_id":  "<UUIDv7>",
//	  "started_at":  "<RFC 3339 UTC>",
//	  "ended_at":    "<RFC 3339 UTC>",
//	  "status":      "completed" | "interrupted" | "failed",
//	  "exit_code":   <int>,
//	  "config": {
//	    "base_url":    "<string>",
//	    "model":       "<string>",
//	    "workspace":   "<string>",
//	    "permission":  "<upper-case enum: READ_ONLY | WORKSPACE_WRITE | FULL_ACCESS>",
//	    "output_mode": "<string, omitempty>"
//	  },
//	  "events_path": "events.jsonl"
//	}
//
// Both verbs accept `--state-dir DIR` (overrides the default
// `~/.simple-harness/sessions`); both reject unknown flags with
// exit 1 (SCOPE §28, generic failure) and unknown verbs with
// exit 1 + a usage line.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/svend-blip/simple-harness/internal/session"
)

// sessionsUsage is the per-subcommand usage line printed on
// unknown-verb / missing-arg errors. Concise on purpose; the
// full picture is in the parent `usage` const in main.go.
const sessionsUsage = `Usage: simple-harness sessions <verb> [flags]

Verbs:
  list    enumerate session ids (one per line, sorted by started_at desc)
  show    print session.json for <id> (pretty-printed JSON)

Flags (shared):
  --state-dir <dir>   state directory (default: ~/.simple-harness/sessions)
`

// runSessions dispatches the "sessions" subcommand. It mirrors
// runConfig's verb-dispatch shape (runConfig at main.go:294-332):
// parse the verb, route to the verb-specific helper, return the
// verb's exit code.
//
// Known verbs: "list", "show". Unknown verbs print sessionsUsage
// and exit 1 (SCOPE §28, generic failure). Missing verb prints
// sessionsUsage and exits 1.
func runSessions(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "%s", sessionsUsage)
		return 1
	}
	switch args[0] {
	case "list":
		return runSessionsList(args[1:])
	case "show":
		return runSessionsShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown sessions verb %q\n%s", args[0], sessionsUsage)
		return 1
	}
}

// resolveStateDir applies the --state-dir default shared with the
// run-mode and interactive-mode flag parsers (the same
// ~/.simple-harness/sessions default). Returns the resolved
// absolute or relative path.
func resolveStateDir(stateDirFlag string) (string, error) {
	if stateDirFlag != "" {
		return stateDirFlag, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".simple-harness", "sessions"), nil
}

// splitStateDirFlag pre-scans args for "--state-dir <value>" or
// "--state-dir=<value>", extracts the value, and returns the
// remaining args with the flag removed. This lets the flag
// parser see only positional args, which is what TG2 needs
// (the canonical CLI shape is `sessions show <id>
// --state-dir DIR` — Go's stdlib flag.Parse stops scanning for
// flags once it sees a positional arg, so an interspersed flag
// after <id> would be treated as positional). Mirrors the
// parsePermissionGlobal pattern at main.go:247-275.
func splitStateDirFlag(args []string) (string, []string) {
	var value string
	var consumed []int
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--state-dir":
			if i+1 < len(args) {
				value = args[i+1]
				consumed = append(consumed, i, i+1)
				i++
			}
		case strings.HasPrefix(args[i], "--state-dir="):
			value = strings.TrimPrefix(args[i], "--state-dir=")
			consumed = append(consumed, i)
		}
	}
	if len(consumed) == 0 {
		return "", args
	}
	consumedSet := make(map[int]bool, len(consumed))
	for _, idx := range consumed {
		consumedSet[idx] = true
	}
	remaining := make([]string, 0, len(args)-len(consumed))
	for i, a := range args {
		if !consumedSet[i] {
			remaining = append(remaining, a)
		}
	}
	return value, remaining
}

// runSessionsList enumerates session ids under --state-dir.
//
// Output: one session_id per line to stdout, sorted by
// started_at DESCENDING (most recent first). Sessions that
// failed to parse (corrupt or partial session.json) are skipped
// with a stderr warning — the inspector is best-effort, not
// fail-fast, because a single corrupt session must not block
// inspection of the rest (this matches the canonical
// filesystem-listing UX: `ls` does not abort on a single
// unreadable entry).
//
// Empty state-dir: print nothing, exit 0.
//
// Flags:
//   --state-dir <dir>  override the default state directory
func runSessionsList(args []string) int {
	stateDirFlag, args := splitStateDirFlag(args)

	flagSet := flag.NewFlagSet("simple-harness sessions list", flag.ContinueOnError)
	flagSet.SetOutput(os.Stderr)
	flagSet.String("state-dir", "", "state directory (default: ~/.simple-harness/sessions)")
	if err := flagSet.Parse(args); err != nil {
		return 1
	}

	stateDir, err := resolveStateDir(stateDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Empty state-dir: nothing to list.
			return 0
		}
		fmt.Fprintf(os.Stderr, "sessions list: read %s: %v\n", stateDir, err)
		return 1
	}

	type sessionInfo struct {
		id        string
		startedAt time.Time
	}
	var infos []sessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionPath := filepath.Join(stateDir, e.Name(), "session.json")
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", sessionPath, err)
			continue
		}
		var s session.Session
		if err := json.Unmarshal(data, &s); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping %s: parse: %v\n", sessionPath, err)
			continue
		}
		infos = append(infos, sessionInfo{id: s.SessionID, startedAt: s.StartedAt})
	}

	sort.Slice(infos, func(i, j int) bool {
		// Most recent first.
		return infos[i].startedAt.After(infos[j].startedAt)
	})

	for _, info := range infos {
		fmt.Println(info.id)
	}
	return 0
}

// runSessionsShow prints session.json for the given session id.
//
// Output: pretty-printed JSON of session.json to stdout
// (json.MarshalIndent with two-space indent). The id must be the
// session directory name under --state-dir; the inspector does
// NOT validate that the id is a UUIDv7 or that the directory
// name matches the JSON's session_id field — it just reads
// <state-dir>/<id>/session.json and prints it.
//
// Errors:
//   id missing            -> print sessionsUsage, exit 1
//   session.json missing  -> print "session not found", exit 1
//   session.json corrupt  -> print "parse error", exit 1
//
// Flags:
//   --state-dir <dir>  override the default state directory
func runSessionsShow(args []string) int {
	stateDirFlag, args := splitStateDirFlag(args)

	flagSet := flag.NewFlagSet("simple-harness sessions show", flag.ContinueOnError)
	flagSet.SetOutput(os.Stderr)
	flagSet.String("state-dir", "", "state directory (default: ~/.simple-harness/sessions)")
	if err := flagSet.Parse(args); err != nil {
		return 1
	}

	// The id is a positional argument (the first non-flag arg
	// after parse). Reject empty id and more than one positional.
	positional := flagSet.Args()
	if len(positional) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: simple-harness sessions show <id> [--state-dir DIR]\n")
		return 1
	}
	if len(positional) > 1 {
		fmt.Fprintf(os.Stderr, "Usage: simple-harness sessions show <id> [--state-dir DIR]\n")
		fmt.Fprintf(os.Stderr, "error: got %d positional arguments, want 1\n", len(positional))
		return 1
	}
	id := positional[0]

	stateDir, err := resolveStateDir(stateDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}

	sessionPath := filepath.Join(stateDir, id, "session.json")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "session not found: %s\n", id)
			return 1
		}
		fmt.Fprintf(os.Stderr, "sessions show: read %s: %v\n", sessionPath, err)
		return 1
	}

	// Validate the JSON parses against the session.Session type
	// (proves it is a real session.json, not garbage in the
	// directory). Then re-marshal with stable indent for output.
	var s session.Session
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintf(os.Stderr, "sessions show: parse %s: %v\n", sessionPath, err)
		return 1
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessions show: marshal: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}
