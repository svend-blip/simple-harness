# Simple Harness V1 Public Contract

Pin: simple-harness 95fcfc94deef461c51a205186735ba2b48f14fe3

This document is the **compatibility-sensitive V1 public contract** for
Simple Harness. It freezes, for any external controller — Harness
Allocator first among them — exactly what Simple Harness guarantees
about its process, CLI, JSONL event stream, exit codes, signal
semantics, session layout, status states, and process-lifecycle
guarantees, so a Harness Allocator adapter can treat Simple Harness as
a normal harness backend **without reading its internal source**.

This contract is the SCOPE §36 + §37 + §42 deliverable from Run 014
(GOAL §§1–2; handoff 050). It is **observed behaviour**, not
aspiration; every claim is verifiable against the conformance checker
that ships with the harness (see *Conformance-check anchors* below).

From this contract forward, the SCOPE §42 compatibility discipline is
binding:

```text
Additive changes preserve the wire shape.
Intentional breaking changes bump protocol_version.
```

---

## Scope

This document defines the public surface that an external controller
may rely on. It is intentionally narrower than the full set of SCOPE
sections — the public surface is what an adapter would integrate
against, not the full architectural rationale. The architectural
rationale (the component cut, the module boundaries, the four-system
responsibility boundary) lives in `docs/ARCHITECTURE.md`; this
document references it but does not redeclare it.

**In scope (this contract):**

* `simple-harness` CLI invocation grammar — every subcommand and flag
  that is part of the V1 surface.
* JSONL event protocol — every event type V1 emits, with fields and
  `protocol_version` semantics.
* Exit codes — the 7-code scheme from SCOPE §28, including the
  triggering condition for each.
* Signal semantics — the SIGINT/SIGTERM behaviour under interactive
  mode (SCOPE §25) and headless mode (SCOPE §26).
* Status states — the SCOPE §23 status set V1 emits vs reserved names.
* Session identity and on-disk layout — the canonical
  `<state-dir>/<session-id>/{session.json, events.jsonl, messages.jsonl}`
  directory from SCOPE §17.
* Process-lifecycle guarantees — one binary, one session per
  invocation; child-process ownership; the HEADLESS_GUARD clause.
* Compatibility policy — SCOPE §42's additivity + `protocol_version`
  bump seam.

**Out of scope (this contract):**

* Internal package boundaries, module paths, function signatures.
* Harness selection logic (Harness Allocator's job).
* Model allocation / endpoint selection (Model Allocator's job).
* Multi-agent orchestration (DPMtF's job).
* Internal tool implementations, schema details, or permission
  pipeline internals beyond the observable rejection shape (a
  `permission_denied` exit code 4 is the surface; the pipeline
  ordering is internal).

**Compatibility-sensitive from Run 014 forward.** Once this document
is published, changes to the public surface are compatibility
breaks and must follow the §"Compatibility Policy" rule below.

---

## CLI Invocation Grammar

The V1 CLI surface is `simple-harness [global flags] [subcommand]`.
Global flags must be parsed before subcommand dispatch.

### Global flags

| Flag | Effect |
|------|--------|
| `--permission <mode>` | Resolve the active permission mode (`read_only`, `workspace_write`, `full_access`). Global: applies to every subcommand and the interactive mode. Unknown value exits 2 (SCOPE §28, configuration error). Default `read_only` (SCOPE §12: never silent escalation). |
| `--help` | Print the usage summary and exit 0. |
| `--version` | Print the runtime version literal and exit 0. |

### Subcommands

| Subcommand | Purpose | V1 contract |
|------------|---------|-------------|
| `config show` | Print the fully resolved configuration (secrets redacted). Includes the active permission mode as a top-level `"permission"` field. | Exits 0 on success; exits 2 on configuration error. |
| `tools` | Print the sorted tool inventory (one tool name per line on stdout). | Exits 0; empty stdout when the registry is empty. |
| `sessions list` | Enumerate session ids under `--state-dir`, sorted by `started_at` descending. One session_id per line on stdout. | Exits 0; empty stdout when the state directory does not exist. |
| `sessions show <id>` | Print `session.json` for `<id>` (pretty-printed JSON). | Exits 0; exits 1 when the session does not exist or `session.json` is corrupt. |
| `context show` | Print the SCOPE §19 accounting report for the flag-parsed composition **without invoking the model client**. | Exits 0; exits 2 on configuration error or context overflow. |
| `context doctor` | Print the SCOPE §20 doctor diagnostics for the flag-parsed composition **without invoking the model client**. | Exits 0; exits 2 on configuration error or context overflow. |
| `run` | Execute one turn non-interactively; emit JSONL events. The headless execution surface; this is the SCOPE §5 requirement. | Exits per the SCOPE §28 exit-code scheme documented below. |
| *(no subcommand)* | Interactive (REPL) mode: print the startup banner, read prompts from stdin, stream model output to stdout, emit JSONL to a sidecar. | Exits 0 on EOF / `/exit`; exits 6 on second Ctrl+C; exits per the SCOPE §28 scheme on error paths. |

### `simple-harness run` flags

The `run` subcommand takes the following flags. The text below mirrors
`runUsage` in `cmd/simple-harness/run.go:65` verbatim.

| Flag | Required | Default | Effect |
|------|----------|---------|--------|
| `--base-url <url>` | yes | — | Base URL of the OpenAI-compatible endpoint. Empty exits 2 (SCOPE §28). |
| `--model <name>` | yes | — | Model name to send in the chat request. Empty exits 2. |
| `--prompt-file <path>` | yes (unless `--prompt`) | — | Path to the prompt file; use `-` to read from stdin. Empty exits 2. |
| `--output <mode>` | no | `terminal` | `terminal` (default — streamed assistant text to stdout) or `jsonl` (every line on stdout is a structured event). |
| `--workspace <dir>` | no | cwd | Workspace directory. |
| `--state-dir <dir>` | no | `~/.simple-harness/sessions` | State directory for session persistence. |
| `--system <text>` | no | — | Inline external system / governance prompt. Mutually exclusive with `--system-file`. |
| `--system-file <path>` | no | — | External system / governance prompt from a file. Mutually exclusive with `--system`. |
| `--skill <name>` | no | — | Skill name to load. Unknown name exits 2. |
| `--skills-dir <dir>` | no | `~/.simple-harness/skills + <workspace>/.simple-harness/skills` | Skills directory override. Test-only deterministic handle. |
| `--max-turns <n>` | no | `8` | Upper bound on agent's model-request / tool-execution cycles. `< 0` exits 2. |
| `--limit <n>` | no | `0` | Configured context limit in tokens. `<= 0` disables the overflow check. Set + overflow exits 2 (SCOPE §18). |
| `--version` | — | — | Print the runtime version and exit 0. |
| `--help` | — | — | Print the run-mode usage and exit 0. |

### `simple-harness context show|doctor` flags

Same flags as `run` (the surface's contract is "inspect a composition
without executing it", per GOAL §2 bound decision 1). The following
flag is added:

* `--prompt-file <path>` — required; `-` is rejected with exit 2.

### `simple-harness sessions <verb>` flags

| Flag | Effect |
|------|--------|
| `--state-dir <dir>` | Override the default `~/.simple-harness/sessions`. |

### Interactive-mode flags

| Flag | Effect |
|------|--------|
| `--workspace <dir>` | Workspace directory (default: cwd). |
| `--state-dir <dir>` | State directory (default: `~/.simple-harness/sessions`). |
| `--skill <name>` | Skill to load at startup. |
| `--skills-dir <dir>` | Skills directory override. |
| `--limit <n>` | Configured context limit in tokens. |

Interactive mode REPL commands (at the prompt):

```text
/help              print the interactive-mode usage
/version           print the runtime version literal
/context           print the SCOPE §19 accounting report from the current ledger
/context-doctor    print the SCOPE §20 doctor diagnostics
/skill <name>      load a skill mid-session
/exit, /quit       exit cleanly (code 0)
Ctrl+D             exit cleanly (code 0)
Ctrl+C (first)     cancel the active request, return to prompt
Ctrl+C (second)    terminate with exit code 6
```

---

## JSONL Event Protocol

The event protocol is **versioned JSONL**: one JSON object per line,
UTF-8, newline-terminated. Every event has a `protocol_version`
field; `protocol_version` is the version-bump seam for any
intentional breaking schema change.

### Base schema (every event)

```text
protocol_version   string   the event-protocol version; "1" for V1
event              string   the event type (see enumeration)
timestamp          string   RFC 3339 UTC, nanosecond precision
session_id         string   the session this event belongs to
```

The `protocol_version` value is `"1"` for V1. A future V1.x that adds
a new optional field keeps `"1"`; a V2 that changes a field's type or
removes a field bumps the value to `"2"`. External controllers must
parse the field as a string and reject events whose
`protocol_version` is greater than their supported set.

The `timestamp` is RFC 3339 UTC with nanosecond precision. The
`session_id` is a UUIDv7 (sortable by creation time, low collision
risk).

### Event types in V1

The V1 protocol defines 8 event types. Event-specific fields extend
the base schema per type.

| Event | Fields (beyond base) | When emitted |
|-------|----------------------|--------------|
| `started` | `config` (object: `model`, `endpoint`, `workspace`, `permission`) | Once at the start of every session. The identity card. |
| `status` | `status` (string — SCOPE §23 status name) | On every state transition. |
| `model_request` | *(none)* | Immediately before each model-client invocation. |
| `assistant_stream` | `delta` (string), `role` (string — `"assistant"`) | On each streamed chunk from the model. |
| `tool_call` | `call_id` (string), `tool` (string) | When the model emits a tool-call delta the harness has chosen to dispatch. |
| `tool_result` | `call_id` (string), `tool_result_status` (string — `"ok"` or `"error"`), `content` (string, optional) | After the dispatch pipeline returns for a call. Carries the matching `call_id` of the `tool_call` event for correlation. |
| `interrupted` | *(none)* | Terminal signal emitted on SIGINT/SIGTERM in headless mode (SCOPE §26). Precedes `completed(exit_code: 6)`. |
| `completed` | `exit_code` (int — SCOPE §28 code) | Terminal event; emitted once per session, after the final status. |

The JSONL `status` event is the canonical observability surface for
runtime state (SCOPE §23 binding). One `status` event per state
transition; an external controller reconstructs the runtime timeline
from the sequence of `status` events.

### Output channels

The event stream lands on two channels:

* **JSONL sidecar** — `<state-dir>/<session-id>/events.jsonl` is
  ALWAYS written (regardless of `--output` mode). This is the
  canonical collection surface for an external controller.
* **stdout (live)** — under `--output jsonl`, the same event stream
  ALSO streams to stdout (one JSON object per line). Under
  `--output terminal`, only the assistant text is on stdout; the
  sidecar carries the events.

### Schema invariants (SCOPE §21 binding)

```text
Harness Allocator or another external controller must not need
to scrape decorative terminal output to understand Simple
Harness execution.
```

The wire shape respects this invariant: every observable runtime
state is in the JSONL stream or the exit code, not in the terminal
UI. The terminal UI exists for humans; an external controller must
read the JSONL stream.

### Conformance-check anchor

The `simple-harness run --output jsonl` surface guarantees that
**every line on stdout parses as a JSON object** (Run 006 TG3).
The `protocol_version: "1"` field is stamped automatically by the
event emitter (`internal/event/event.go:119-132`).

---

## Exit Codes (per SCOPE §28)

The V1 exit-code scheme is the SCOPE §28 7-code table. The codes are
stable; an external controller can rely on them across V1.x.

| Code | Name | Triggering condition | Example |
|------|------|----------------------|---------|
| `0` | success | Clean exit. Run-mode validation passed AND the model turn completed (response was streamed); interactive mode reached EOF or `/exit`. | `simple-harness run ... --prompt-file task.md` runs end-to-end without error. |
| `1` | generic failure | Unhandled error: flag parse error, runtime I/O error, max-turns overflow (`loop.MaxTurnsError` → exit 1), or any other error not mapped to a more specific code. | `simple-harness run --no-such-flag ...` |
| `2` | configuration error | Invalid configuration: missing or empty `--base-url`, empty `--model`, missing `--prompt-file`, missing `--workspace`, invalid `--output`, missing or unreadable `--system-file`, unknown skill name, `--system` and `--system-file` both set, `--max-turns < 0`, invalid permission mode, context overflow when `--limit <n>` is set. | `simple-harness run --base-url "" --model kimi --workspace /tmp/x --prompt-file t --output jsonl` |
| `3` | model / API failure | HTTP error, parse error, upstream error from the model endpoint. Emits a `status: FAILED` event and a `completed(exit_code: 3)` event before exit. | `simple-harness run --base-url http://127.0.0.1:1/v1 --model kimi ... --output jsonl --max-turns 1` against an unreachable endpoint. |
| `4` | permission violation | A tool call failed the permission gate (`internal/perm/perm.go:78` produces `DecisionError{Stage: "policy", Reason: "permission_denied"}`). The loop's `RunAgent` emits `status(FAILED)` + `completed(exit_code: 4)` and returns `*loop.PermissionError`, which maps to exit 4. | `simple-harness run --permission read_only ...` with a write tool call against an out-of-workspace path. |
| `5` | tool failure | A tool returned a structured failure (`tools.Result.Status == "error"`). *(Reserved name; no V1 path emits it under the current tool set. The code is part of the V1 scheme per SCOPE §28; it stays reserved for future tool failure surfaces.)* | Reserved. |
| `6` | interrupted | SIGINT or SIGTERM received. Headless mode emits `interrupted` event + `completed(exit_code: 6)` then exits 6. Interactive mode's second Ctrl+C emits `interrupted` and exits 6. | Send SIGTERM to a running harness mid-flight. |

### Conformance-check anchor

The 7-code table is verified by `scripts/contract-check.sh` (handoff
051). The contract document names the exact assertions:

| Behavior | Observable | Assertion |
|----------|------------|-----------|
| (a) | `simple-harness --version` exits 0 with the `Version` literal on stdout. | exit 0 + stdout begins with `"simple-harness "`. |
| (b) | `simple-harness run --base-url ""` exits 2 with stderr `config error: --base-url is required`. | exit 2 + stderr matches `/config error: --base-url is required/`. |
| (c) | `simple-harness run --base-url http://127.0.0.1:1/v1` (unreachable) exits 3 with valid JSONL including a `started` event and a `completed(exit_code: 3)` event. | exit 3 + JSONL is non-empty + every line is parseable JSON + at least one `"event":"started"` line + at least one `"event":"completed"` line with `"exit_code":3`. |
| (d) | `SIGTERM` to a running harness mid-flight exits 6 with `interrupted` event + `completed(exit_code: 6)`. | Harness exits within 5s after SIGTERM + sidecar JSONL contains an `interrupted` event + sidecar JSONL's terminal event has `"exit_code":6`. (Best-effort — SKIP if the live endpoint is down.) |
| (e) | After a sample run, `~/.simple-harness/sessions/<session-id>/{session.json, events.jsonl, messages.jsonl}` exists with the SessionConfig identity card in `session.json`. | Each session directory has `session.json` (valid JSON carrying `session_id`, `base_url`, `model`, `permission`, `workspace`, `created_at`) + `events.jsonl` (≥1 line, every line parseable JSON) + `messages.jsonl` (non-empty). |

---

## Signal Semantics (per SCOPE §§25–26)

Signal behaviour is part of the public runtime contract (SCOPE §25
closing line + SCOPE §26 opening line). V1 implements both regimes.

### Interactive mode (SCOPE §25)

| Signal | Behaviour |
|--------|-----------|
| First `SIGINT` (Ctrl+C) | Cancel the active model request. The session is preserved. The REPL returns to the prompt. The first-press cancellation distinguishes signal-triggered from model-timeout via `errors.Is(me.Err, context.Canceled)` + `cancelPressed`. |
| Second `SIGINT` | Terminate with exit code 6. The harness emits `interrupted` event, flushes the sidecar, and exits 6 (SCOPE §28). |
| `SIGTERM` | Treated as a SIGINT (cancel active work; second signal exits 6). |
| EOF on stdin | Clean exit, code 0. |
| `/exit`, `/quit` | Clean exit, code 0. |

### Headless mode (SCOPE §26)

```text
SIGINT / SIGTERM
        ↓
mark interruption
        ↓
cancel active model/tool work
        ↓
terminate harness-owned child processes
        ↓
persist useful session state
        ↓
emit final interruption event
        ↓
flush event output
        ↓
exit using documented code (6)
```

Concretely (`cmd/simple-harness/run.go:591-598`):

```text
signal.Notify(sigCh, SIGINT, SIGTERM)
goroutine: <-sigCh
    interrupted = true
    cancel()   // cancels runCtx
```

The deferred `interrupt` check after `r.RunAgent` returns emits the
`interrupted` event, syncs the sidecar, and returns 6.

### Process-group kill semantics (HEADLESS_GUARD)

> Do not allow a single task interrupt to accidentally destroy the
> parent tmux or Harness Allocator execution chain.
> — SCOPE §25 closing line

Simple Harness owns the lifecycle of every subprocess it spawns.
Child processes are spawned in their own process group
(`SysProcAttr{Setpgid: true}` per `internal/proc/`); signal propagation
goes through the process group. The signal cascade on interrupt is:

```text
SIGINT/SIGTERM
    ↓
context.Cancel of runCtx
    ↓
active model request cancels (model.Client maps context.Canceled to
*model.ModelError{ErrTimeout})
    ↓
tool dispatch goroutine cancels
    ↓
owned subprocesses terminate (controlled SIGTERM, escalation to SIGKILL
after grace period)
    ↓
sidecar flushed, events.jsonl closed
    ↓
exit 6
```

A terminated harness does not routinely leave behind `pytest`
processes, build commands, shell children, or tool-owned background
processes. The cleanup pass targets process groups the harness itself
created — unrelated user processes are unaffected.

---

## Status States (per SCOPE §23)

The status enum is the SCOPE §23 list, verbatim. V1 emits a subset
of these states; the remainder are reserved names for future
extensions. The JSONL `status` event carries the SCOPE value
verbatim; the loop is the single owner of which status to emit
when.

| State | Meaning | V1 emits? |
|-------|---------|-----------|
| `STARTING` | Harness is initializing (config load, session id generation). | yes |
| `READY` | Configuration loaded, session identity established, awaiting the model. | yes |
| `WAITING_FOR_MODEL` | An active model request is in flight (HTTP request pending). | yes |
| `STREAMING` | Model is streaming a response. | yes |
| `READING` | A `read_file` tool call is in flight. | reserved (post-V1 — V1 tool set per `internal/tools/builtins/` will emit this) |
| `SEARCHING` | A `search_files` tool call is in flight. | reserved |
| `WRITING` | A `write_file` tool call is in flight. | reserved |
| `PATCHING` | An `apply_patch` tool call is in flight. | reserved |
| `RUNNING_TOOL` | A tool is executing (generic state for tools without a more specific state). | yes |
| `INTERRUPTING` | A signal has been received; cleanup in progress. | yes |
| `COMPLETED` | The run finished successfully. | yes |
| `FAILED` | The run terminated with an error. | yes |
| `CLEANUP` | Subprocess cleanup in progress (post-completion). | yes |
| `INTERRUPTED` | The run was interrupted by SIGINT/SIGTERM (terminal state). | yes |

The status string is the SCOPE value verbatim. Statuses correspond to
actual execution state — not to inferred chain-of-thought or hidden
model reasoning (SCOPE §23 closing line).

---

## Session Identity and Layout (per SCOPE §17)

### Session ID

A `session_id` is generated at harness startup as a UUIDv7 (16 bytes,
RFC 9562 §5.7; sortable by creation time, low collision risk). The
ID is the canonical correlation handle between the harness process,
the sidecar, `session.json`, `messages.jsonl`, and any external
record.

### Canonical directory layout

```text
<state-dir>/<session-id>/
    session.json     identity card + resolved config snapshot
                     atomic-rename write (writer.go:113-117)
                     read-only after the run completes for V1
    events.jsonl     append-only JSONL of every event
                     ALWAYS written regardless of --output mode
                     (Run 008 handoff 030)
    messages.jsonl   append-only JSONL of every message
                     (system / governance / skills / task /
                      assistant / tool results)
```

The default `<state-dir>` is `~/.simple-harness/sessions`. The flag
`--state-dir <dir>` overrides it.

### `session.json` schema (per `internal/session/writer.go`)

```text
{
  "session_id":   "<UUIDv7>",
  "started_at":   "<RFC 3339 UTC>",
  "ended_at":     "<RFC 3339 UTC>",
  "status":       "completed" | "interrupted" | "failed",
  "exit_code":    <int>,
  "config": {
    "base_url":    "<string>",
    "model":       "<string>",
    "workspace":   "<string>",
    "permission":  "<upper-case enum: READ_ONLY | WORKSPACE_WRITE | FULL_ACCESS>",
    "output_mode": "<string, omitempty>"
  },
  "events_path":  "events.jsonl"
}
```

`session.json` is written **exactly once** at session end via an
atomic rename (`writer.go:113-117`: write to `.tmp`, then
`os.Rename`). A partial `session.json` is never visible.

### Inspection surfaces

* `simple-harness sessions list` — enumerates session ids sorted by
  `started_at` descending (one per line on stdout).
* `simple-harness sessions show <id>` — prints `session.json` for
  `<id>` (pretty-printed JSON).

Both inspection surfaces are read-only. An external controller may
read but must not mutate `session.json`, `events.jsonl`, or
`messages.jsonl`. The V1 contract does not cover mutation by an
external tool — Harness Allocator is expected to integrate via the
CLI subcommands above, not by writing to the directory directly.

### Lifetime

A session is created at harness startup and closed on harness exit.
A future resume subcommand (deferred — see §resume below) would
re-open a closed session by appending new messages and events to the
same `<session-id>/` directory.

---

## Process-Lifecycle Guarantees

### One binary, one session per invocation

Each `simple-harness` invocation owns exactly one session. There is no
forking, no orchestration, no second loop layer. The entry point is
a single static Go binary wrapped by `bin/simple-harness` (a POSIX
`sh` script that `exec`s the compiled runtime binary so signals
delivered to `bin/simple-harness` reach the runtime process
directly — RUNS-BACKLOG §"Cross-run bound decisions").

### Child-process ownership

Simple Harness explicitly owns the lifecycle of every subprocess it
spawns (`internal/proc/`, SCOPE §27):

* Each child is spawned with `SysProcAttr{Setpgid: true}` so it has
  its own process group.
* Signal propagation goes through the process group.
* On timeout or interruption: controlled `SIGTERM`, then `SIGKILL`
  after a grace period.
* A cleanup pass on every harness exit terminates the owned
  process group.

A terminated harness does not routinely leave behind `pytest`
processes, build commands, shell children, or tool-owned background
processes. The cleanup pass targets process groups the harness
itself created (SCOPE §27 closing line) — unrelated user processes
are unaffected.

### HEADLESS_GUARD clause

> Do not allow a single task interrupt to accidentally destroy the
> parent tmux or Harness Allocator execution chain.

The HEADLESS_GUARD is enforced by:

1. Process-group isolation (every owned subprocess is its own
   process group; signal propagation stays inside the group).
2. Documented signal sequence (SIGINT cancels, second SIGINT
   terminates — the loop never sends a signal beyond the owned
   group).
3. No escalates-beyond-group behavior — a second SIGINT terminates
   only the harness process; the harness does not signal its parent
   tmux or the controller.

### Atomic file writes

* `session.json` is written via `os.WriteFile` to a `.tmp` path
   followed by `os.Rename` (`writer.go:113-117`).
* `events.jsonl` is `Sync()`-ed before close on the headless exit
   path (`run.go:734-735`).
* `messages.jsonl` is `Sync()`-ed before close on `Write`
   (`writer.go:120-122`).

### Help surfaces

The canonical help text for each subcommand is the matching `const`
in the source (`usage`, `runUsage`, `contextUsage`, `sessionsUsage`).
The text is part of the public contract — a regression that drops a
named flag from the help text is a regression in the contract, not
just in the parser.

| Subcommand | Help source |
|------------|-------------|
| (none — global) | `cmd/simple-harness/main.go:112` `usage` |
| `simple-harness run` | `cmd/simple-harness/run.go:65` `runUsage` |
| `simple-harness context` | `cmd/simple-harness/context.go:71` `contextUsage` |
| `simple-harness sessions` | `cmd/simple-harness/sessions.go:54` `sessionsUsage` |

---

## Compatibility Policy (per SCOPE §42)

This document is the **compatibility-sensitive** V1 contract from
Run 014 forward (per SCOPE §42). The policy:

```text
Additive changes preserve the wire shape.

Intentional breaking changes bump protocol_version.
```

### Additive changes (V1.x)

The following are V1-compatible and do NOT require a `protocol_version`
bump:

* Adding a new event type (controllers ignore unknown event types).
* Adding a new optional field to an existing event type (controllers
  ignore unknown fields).
* Adding a new subcommand or verb to the CLI.
* Adding a new SCOPE §23 status name.
* Adding a new SCOPE §28 exit code is **NOT** an additive change —
  controllers may not be prepared for an unknown exit code, so a new
  exit code is a breaking change.

### Breaking changes (V2)

The following require a `protocol_version` bump:

* Changing an event field's name, type, or semantics.
* Removing an event type or field.
* Changing an exit code's meaning (e.g. moving "interrupted" from
  6 to a different number).
* Removing a subcommand or verb from the CLI.
* Changing the SCOPE §17 directory layout.
* Changing the JSONL event ordering in a way that breaks the
  invariant (e.g. emitting `completed` before the final `status`).
* Changing the JSONL `status` enum's spelling.

### Versioning seam

The `protocol_version` field is the canonical version-bump seam. The
V1 value is `"1"` (string). A V2 with breaking changes sets the
value to `"2"` and is published as a separate document (a
`docs/HARNESS-CONTRACT-V2.md` or successor). External controllers
parse `protocol_version` as a string and reject events with a value
greater than their supported set.

---

### probe

The `probe` operation asks the harness to advertise its identity
without launching a session or calling the model endpoint. V1
implements `probe` via three CLI surfaces, all read-only, all
without a session id and without a model-server call.

**Public surface:**

| Invocation | Effect | V1 contract |
|------------|--------|-------------|
| `simple-harness --version` | Print the runtime version literal and exit 0. | Exit 0; stdout contains the `Version` literal from `cmd/simple-harness/main.go:88`. |
| `simple-harness tools` | Print the sorted tool inventory. | Exit 0; one tool name per line on stdout; empty stdout when the registry is empty. |
| `simple-harness config show` | Print the resolved configuration with the active permission mode. | Exit 0; secrets redacted (`api_key` field rendered as `"<redacted>"` per SCOPE §30); top-level `"permission"` field carries the active mode (`read_only`, `workspace_write`, or `full_access`). |

**Example session transcript (probe):**

```text
$ simple-harness --version
simple-harness 0.1.0-dev (Run 012, handoff 047)
$ echo $?
0
$ simple-harness tools
apply_patch
grep
list_directory
read_file
search_files
shell
write_file
$ simple-harness config show
{
  "model": {
    "base_url": "http://127.0.0.1:11434/v1",
    "model": "kimi-k3:cloud",
    "api_key": "<redacted>",
    "temperature": 0.2,
    "max_output_tokens": 8192,
    "request_timeout": "30s"
  },
  "permission": "read_only"
}
```

**What the controller learns:**

* Whether the runtime binary is reachable and runnable (exit code 0).
* The exact version literal (for version compatibility assertions).
* The registered tool set (for tool-aware routing).
* The resolved configuration including the active permission mode
  (for permission-aware routing).

**Conformance-check anchor (behavior a — version flag):**

* Invocation: `simple-harness --version`.
* Assertion: exit 0 + stdout begins with `simple-harness `.

---

### prepare

The `prepare` operation asks the harness to pre-flight a
configuration without launching a session — to validate that the
flags produce a runnable configuration. V1 implements `prepare` via
the `simple-harness run` flag parser: missing or empty required
flags exit 2 with a deterministic `config error: <flag> is required`
message on stderr **before** any model interaction.

**Public surface:**

```text
simple-harness run \
    --permission <mode> \
    --base-url <url> \
    --model <name> \
    --workspace <dir> \
    --system-file <path> \
    --output <mode> \
    --prompt-file <path> \
    --max-turns <n>
```

Required flags (empty values exit 2 with `config error: <flag> is
required` on stderr):

* `--base-url` (run.go:259-262)
* `--model` (run.go:266-269)
* `--prompt-file` (run.go:399-402)
* `--workspace` (the workspace directory must exist; run.go:525-528)

Additional config-error paths (also exit 2):

* `--output` is not `terminal` or `jsonl` (run.go:289-295).
* `--system` and `--system-file` both set (run.go:320-323).
* `--system-file` set but file unreadable (run.go:324-335).
* `--skill <name>` with unknown name (run.go:368-371).
* `--permission <mode>` with unknown mode (main.go:217-221, parse
  error → exit 2).
* `--max-turns < 0` (run.go:388-391).

**Example invocation (config-error path):**

```text
$ simple-harness run --base-url "" --model kimi-k3:cloud \
    --workspace /tmp/ws --permission workspace_write \
    --prompt "hi" --output jsonl
config error: --base-url is required
$ echo $?
2
```

**What the controller learns:**

* Whether the flag-parsed configuration is valid (exit 0 = valid;
  exit 2 = invalid + which flag is the cause).
* The resolved `--state-dir` path (via a successful `config show`
  run with the same flags).
* The resolved `--workspace` (via the flag-parsed value or cwd).

**Conformance-check anchor (behavior b — config-error exit 2):**

* Invocation: `simple-harness run --base-url "" --model <name> --workspace <dir> --permission <mode> --prompt "hi" --output jsonl`.
* Assertion: exit 2 + stderr matches `/config error: --base-url is required/`.

---

### start

The `start` operation asks the harness to launch a session. V1
implements `start` via `simple-harness run` (headless) or the
interactive REPL (no-args). The canonical start surface for an
external controller is `--output jsonl`, which emits the full JSONL
event stream to stdout.

**Public surface:**

```text
simple-harness run --base-url <url> --model <name> \
    --workspace <dir> --permission <mode> \
    --output jsonl --prompt-file <path>
```

On `start`, the harness:

1. Generates a `session_id` (UUIDv7).
2. Creates `<state-dir>/<session-id>/` (mkdir 0o755).
3. Opens `events.jsonl` in the session directory (and, under
   `--output jsonl`, `io.MultiWriter`s it to stdout).
4. Opens `messages.jsonl` via `session.NewWriter` for per-message
   appends.
5. Emits a `started` event with the SessionConfig identity card.
6. Runs the model/tool loop.

**The `started` event payload** (canonical SessionConfig):

```text
{
  "protocol_version": "1",
  "event":            "started",
  "timestamp":        "<RFC 3339 UTC>",
  "session_id":       "<UUIDv7>",
  "config": {
    "model":      "<string>",
    "endpoint":   "<URL>",
    "workspace":  "<path>",
    "permission": "READ_ONLY" | "WORKSPACE_WRITE" | "FULL_ACCESS"
  }
}
```

The `session_id` returned to the controller is the one the harness
stamps onto every event. The controller records it for `collect` and
future `cleanup` operations.

**Example shell invocation + the resulting `started` event:**

```text
$ simple-harness run --base-url http://127.0.0.1:11434/v1 \
    --model kimi-k3:cloud --workspace /tmp/ws \
    --permission workspace_write --output jsonl \
    --prompt-file task.md
{"protocol_version":"1","event":"started","timestamp":"...","session_id":"01936...","config":{"model":"kimi-k3:cloud","endpoint":"http://127.0.0.1:11434/v1","workspace":"/tmp/ws","permission":"WORKSPACE_WRITE"}}
```

**What the controller learns:**

* A `session_id` it can use for `collect` (read `events.jsonl`) and
  future `cleanup` operations.
* The full SessionConfig identity card (model, endpoint, workspace,
  permission).
* Confirmation that the harness process is alive and the loop has
  started.

**Conformance-check anchor (behavior c — unreachable endpoint):**

* Invocation: `simple-harness run --base-url http://127.0.0.1:1/v1 --model <name> --workspace <dir> --permission <mode> --prompt "hi" --output jsonl --max-turns 1`.
* Assertion: exit 3 + JSONL is non-empty + every line is parseable JSON + at least one `"event":"started"` line + at least one `"event":"completed"` line with `"exit_code":3`.

---

### send

The `send` operation asks the harness to deliver the user's task to
the loop. V1 implements `send` via the headless `--prompt-file` /
`--prompt` flags (and via `--system-file` / `--system` for external
governance), or via the interactive REPL prompt loop.

**Public surface (headless):**

| Flag | Effect |
|------|--------|
| `--prompt-file <path>` | Path to the prompt file (the canonical headless `send`). The file is read once at start; mid-run additions are not part of V1's contract. |
| `--prompt "<text>"` | Inline prompt on the CLI. |
| `--system-file <path>` | External governance / system prompt from a file. Mutually exclusive with `--system`. |
| `--system "<text>"` | Inline external governance / system prompt. Mutually exclusive with `--system-file`. |

**Public surface (interactive):**

* The interactive REPL reads prompts from stdin. Multiline-paste is
  supported via trailing `\` continuation (SCOPE §24).
* `/paste` mode is the canonical explicit mode for multiline
  prompts (SCOPE §24 closing line).

The prompt is captured into `messages.jsonl` per SCOPE §17 (a
`user` message with role=`"user"` is appended before the model call).
The `assistant` response is appended after the turn completes.

**Composition order (SCOPE §14 binding):**

```text
minimal harness system (loop.HarnessSystem)
        ↓
external system / governance (--system or --system-file)
        ↓
loaded skills (--skill <name>)
        ↓
user task (--prompt-file / --prompt / interactive)
```

The composition is observable via `simple-harness context show` (SCOPE
§19 accounting report) and `simple-harness context doctor` (SCOPE §20
diagnostics) **without** invoking the model client.

**Example invocation:**

```text
simple-harness run --base-url http://127.0.0.1:11434/v1 \
    --model kimi-k3:cloud --workspace /tmp/ws \
    --permission workspace_write \
    --prompt "Inspect calculator.py and propose a fix" \
    --output terminal
```

**What the controller learns:**

* The task has been captured into the session's `messages.jsonl`.
* The composition order is observable (via `context show` /
  `context doctor`).

**V1 limitations:**

* `--prompt-file -` (stdin) is a parseable sentinel but does NOT
  actually read stdin in V1 — stdin handling is a future handoff
  (per `run.go:403-415`).
* `simple-harness context show` rejects `--prompt-file -` with exit 2.

---

### status

The `status` operation asks the harness to report its current state.
V1 implements `status` via three surfaces: two offline (read-only
inspection) and one live (the JSONL `status` event stream).

**Public surface (offline — no model call, no session):**

| Invocation | Effect | V1 contract |
|------------|--------|-------------|
| `simple-harness context show` | Print the SCOPE §19 accounting report for the flag-parsed composition. | Exit 0; per-entry token estimate + Total line. |
| `simple-harness context doctor` | Print the SCOPE §20 doctor diagnostics (large contributors, duplicates, schema overhead). | Exit 0; the findings are SCOPE §20-shaped: `doctor findings (N):` header + one finding per line. |
| `simple-harness sessions list` | Enumerate session ids under `--state-dir`. | Exit 0; one session_id per line, sorted by `started_at` descending. |

**Public surface (live — JSONL stream):**

The JSONL `status` event is the canonical live status surface
(SCOPE §23 binding). One `status` event per state transition; an
external controller reconstructs the runtime timeline from the
sequence of `status` events.

```text
{"protocol_version":"1","event":"status","timestamp":"...","session_id":"...","status":"WAITING_FOR_MODEL"}
{"protocol_version":"1","event":"status","timestamp":"...","session_id":"...","status":"STREAMING"}
...
{"protocol_version":"1","event":"status","timestamp":"...","session_id":"...","status":"COMPLETED"}
```

**No V1 TUI `/status` slash command.** SCOPE §36 closes with "a
CLI/process/event implementation may satisfy several of them"; the
JSONL stream is the canonical `status` surface; the offline
`context show` / `context doctor` surfaces supplement it for
pre-launch composition inspection.

**What the controller learns (live):**

* The harness's current SCOPE §23 status (`WAITING_FOR_MODEL`,
  `STREAMING`, `RUNNING_TOOL`, `INTERRUPTING`, `COMPLETED`,
  `FAILED`, `CLEANUP`, `INTERRUPTED`).
* The full sequence of state transitions since session start.

**What the controller learns (offline):**

* The estimated token count per context contributor (HarnessSystem,
  ExternalSystem, Skill, Task, ToolSchemas).
* The doctor findings (large contributors, duplicates, schema
  overhead) **without** invoking the model client.

---

### interrupt

The `interrupt` operation asks the harness to cancel the active work
and exit deterministically. V1 implements `interrupt` via OS signal
delivery (SIGINT / SIGTERM) — the SCOPE §25 (interactive) and
SCOPE §26 (headless) regimes.

**Public surface:**

| Context | Signal | Behaviour | Exit code |
|---------|--------|-----------|-----------|
| Interactive (SCOPE §25) | First SIGINT | Cancel the active model request; preserve session; return to prompt. | — (session preserved) |
| Interactive (SCOPE §25) | Second SIGINT (or SIGTERM treated as SIGINT) | Terminate the harness. | 6 |
| Headless (SCOPE §26) | SIGINT or SIGTERM mid-flight | Cancel active work; terminate owned subprocesses; emit `interrupted` event; flush sidecar. | 6 |

The headless signal sequence (`run.go:591-738`):

```text
SIGINT/SIGTERM
    ↓
context.Cancel of runCtx
    ↓
model.Client returns *model.ModelError{ErrTimeout, context.Canceled}
    ↓
loop returns
    ↓
run-after-loop check: interrupted == true
    ↓
em.Interrupted(sessionID)        // emits "interrupted" event
sidecar.Sync() + sidecar.Close()  // flushes events.jsonl
return 6
```

The HEADLESS_GUARD clause (SCOPE §25 closing line) protects the parent
tmux / Harness Allocator chain: signal propagation is scoped to the
owned process group; the harness never signals its parent.

**Example (headless SIGTERM mid-flight):**

```text
# Launch a long-running run in the background.
$ simple-harness run --base-url http://127.0.0.1:11434/v1 \
    --model kimi-k3:cloud --workspace /tmp/ws \
    --permission workspace_write --output jsonl \
    --prompt-file task.md > out.jsonl 2> err.log &
$ PID=$!
sleep 2
kill -TERM $PID
wait $PID
$ echo $?
6
$ tail -2 out.jsonl
{"protocol_version":"1","event":"interrupted","timestamp":"...","session_id":"..."}
{"protocol_version":"1","event":"completed","timestamp":"...","session_id":"...","exit_code":6}
```

**What the controller learns:**

* The harness exited within the signal-handling grace period
  (bounded; the JSONL sidecar is flushed before exit).
* The exit code is 6 (interrupted).
* The `interrupted` event was emitted (terminal signal preceding
  `completed(exit_code: 6)`).

**Conformance-check anchor (behavior d — SIGTERM exit 6):**

* Invocation: launch `simple-harness run --output jsonl --prompt-file <long-prompt>` against a live endpoint; after a brief startup wait, send SIGTERM.
* Assertion: harness exits within 5s after SIGTERM + sidecar JSONL contains an `interrupted` event + sidecar JSONL's terminal event has `"exit_code":6`. (Best-effort — SKIP if the endpoint is down.)

---

### collect

The `collect` operation asks the harness to hand off the session's
output to the controller. V1 implements `collect` via two channels:
the JSONL sidecar (always written) and stdout under `--output jsonl`
(live).

**Public surface:**

| Channel | Path / stream | When written |
|---------|---------------|--------------|
| JSONL sidecar | `<state-dir>/<session-id>/events.jsonl` | ALWAYS, regardless of `--output` mode. (Run 008 handoff 030.) |
| stdout (live) | `--output jsonl` only | One JSON object per line on stdout while the session is in flight. |
| messages.jsonl | `<state-dir>/<session-id>/messages.jsonl` | ALWAYS, one message per append (system / governance / skills / task / assistant / tool results). |
| session.json | `<state-dir>/<session-id>/session.json` | ALWAYS, exactly once at session end (atomic rename). |

The canonical collection target is the JSONL sidecar — a controller
that prefers to read after the fact reads `<state-dir>/<session-id>/events.jsonl`
once the harness process exits. The TG3 stdout-purity guarantee
(every line on stdout under `--output jsonl` is a JSON object) means
a live-collecting controller can tail stdout while the harness is
running.

**Flush guarantees:**

* On `completed` (success path): the sidecar is `Sync()`-ed and
  closed before exit (`run.go:749-752`).
* On signal-driven exit (interrupted): the sidecar is `Sync()`-ed
  and closed before exit (`run.go:733-736`).
* On error paths: the sidecar is `Sync()`-ed and closed before
  exit.

There is no buffered loss: every event the loop emitted is on disk
before the harness exits.

**Pair-call-id assertion (SCOPE §21):**

Every `tool_call` event with a given `call_id` is paired with a
matching `tool_result` event with the same `call_id`. An external
controller can pair the two events deterministically. The V1
contract requires that every `tool_call` produce exactly one
`tool_result` (no orphan `tool_call`s, no orphan `tool_result`s).

**Example collection pattern:**

```text
# Live collection (--output jsonl).
$ simple-harness run --base-url <url> --model <name> \
    --workspace /tmp/ws --permission workspace_write \
    --output jsonl --prompt-file task.md > live.jsonl
$ cat live.jsonl | jq -c 'select(.event | test("^(started|completed)$"))'
{"event":"started",...}
{"event":"completed","exit_code":0,...}

# After-the-fact collection (sidecar).
$ cat ~/.simple-harness/sessions/<id>/events.jsonl | \
    jq -c 'select(.event == "tool_call" or .event == "tool_result") | {event, call_id, tool, tool_result_status}'
{"event":"tool_call","call_id":"t001","tool":"read_file"}
{"event":"tool_result","call_id":"t001","tool_result_status":"ok"}
```

**Conformance-check anchor (behavior e — session layout):**

* After any sample run completes:
  * `~/.simple-harness/sessions/<session-id>/session.json` exists and
    is valid JSON carrying:
    * top-level: `session_id` + `started_at` (RFC 3339 UTC timestamp
      of session creation) + `ended_at` (RFC 3339 UTC timestamp of
      session close, omitted on interrupted sessions) + `status`
      (`completed` / `failed` / `interrupted`) + `exit_code` +
      `events_path` (relative path, `"events.jsonl"`);
    * nested under `config`: `base_url` + `model` + `workspace` +
      `permission` + `output_mode` (the latter omitted when empty).
  * `events.jsonl` is ≥ 1 line + every line is valid JSON.
  * `messages.jsonl` is non-empty.

Note: the four identity-card fields `base_url`, `model`, `workspace`,
`permission` are nested under a `config` sub-object in `session.json`
to match the same nested-config shape emitted in the JSONL `started`
event's `config` sub-object (per `internal/event/event.go`'s
`SessionConfig`). The session-creation timestamp is named `started_at`
(distinct from any future `created_at` field); the session-close
timestamp is `ended_at`.

---

### resume

**Deferred.** V1 has no `--resume <session-id>` CLI flag and no
resume subcommand. Session persistence IS implemented
(`<state-dir>/<session-id>/{session.json, events.jsonl, messages.jsonl}`
per SCOPE §17), and `simple-harness sessions show <session-id>`
lets a controller inspect a prior session's transcript, but a
V1-native `--resume <session-id>` invocation that re-attaches the
loop to a prior session's `messages.jsonl` is OUT of V1's scope.

What V1 offers instead: an external adapter may implement resume
**OUT-OF-BAND** by reading `messages.jsonl` + replaying the prior
context into a fresh `simple-harness run --prompt-file <replayed-prompt>`
invocation, treating resume as a new session whose `messages.jsonl`
contains the prior session's user/assistant/tool messages as the
prelude to the user's new prompt. This workaround satisfies most
"look at the prior session's context and continue" use cases but
does NOT satisfy "fork the prior session's in-memory loop state and
resume from where we left off" (the loop's in-memory state —
pending tool calls, partial-streaming deltas, active cancellation
tokens — is ephemeral; only the on-disk transcript is durable).

**Why deferred:**

* The V1 loop's tool-dispatch state lives in-memory; a future resume
  must capture the loop's in-memory state on `completed` /
  `interrupted` (or ship the live session) and re-hydrate it on
  `--resume`.
* Replaying `assistant_stream` deltas without re-emitting them
  requires a wire-level change (a "replay" mode for the event
  emitter) that does not exist in V1.

**Future Work:** a V2-native resume subcommand requires (i)
capturing the loop's in-memory state on `completed` /
`interrupted` (or shipping the live session), (ii) re-hydrating the
loop from that state on `--resume`, (iii) replaying
`assistant_stream` deltas without re-emitting them. The planning
supervisor may pick this up in a future Run whose GOAL explicitly
opens the loop's session-rehydration surface.

---

### cleanup

**Partial.** V1 has no explicit `cleanup` subcommand; cleanup is
**IMPLICIT** at session boundaries:

**Per-session cleanup:**

Each `simple-harness run` invocation cleans up its tool-owned
subprocesses BEFORE exit (SCOPE §27 + Run 005 semantics — `defer`-
disciplined cleanup + the signal handler's context-cancel
propagation); persists the JSONL sidecar (so the controller's
`collect` step reads what was persisted, not what was buffered);
and exits with the documented exit code (the controller's "did
the process terminate?" check is the cleanup-gate signal).

* No leaked subprocesses survive a clean exit.
* The signal-handling goroutine + the active model request + the
  tool-dispatch goroutine are all cancelled via `context.Cancel`.
* The JSONL emitter's `sync.Mutex`-serialized writes prevent
  interleaved partial JSON lines per `internal/event/event.go`'s
  V1 contract.
* The sidecar is `Sync()`-ed before close (no buffered loss on
  signal-driven exit).
* `messages.jsonl` is `Sync()`-ed and closed (no buffered loss
  on session end).

**Storage cleanup:**

The `<state-dir>/<session-id>/` directory tree is **APPEND-ONLY**;
V1 does NOT garbage-collect old sessions. The `simple-harness
sessions list` subcommand surfaces the session directory listing
for an external controller to implement its own retention policy.

* A V1 garbage collector would risk deleting sessions an external
  controller is still inspecting; the out-of-band retention sweep
  is the safer default.
* The `~/.simple-harness/sessions/` directory accumulates
  indefinitely; an external controller is expected to run its own
  retention sweep out-of-band (e.g. a cron job that removes
  session directories older than N days).

**What V1 covers (per-session cleanup):**

* Tool-owned subprocesses terminated before exit.
* JSONL sidecar flushed + closed before exit.
* `session.json` written atomically before exit.
* Exit code matches the documented exit-code scheme.

**What V1 does NOT cover (storage cleanup):**

* No `cleanup` subcommand.
* No retention-policy enforcement.
* No garbage collection of old sessions.
* No per-session delete (a controller may implement delete
  out-of-band by removing the session directory; V1 does not
  expose a CLI surface for this).

**Future Work:** a `cleanup` subcommand that (i) takes a
`--session-id <id>` for per-session cleanup + (ii) takes a
`--older-than <duration>` for retention-policy cleanup, with the
policy read from a `--config <file>` or env var (per the standard
SCOPE §29 precedence), is deferred. The planning supervisor may
pick this up in a future Run whose GOAL explicitly opens the
session-cleanup surface.

---

## Conformance-check anchors (cross-reference)

The 5 observable behaviors this contract enumerates are the
assertion set `scripts/contract-check.sh` (handoff 051) will run.
The handoff-051 contract-check is the live verification of every
claim above.

| Behavior | Subsection | Assertion |
|----------|-----------|-----------|
| (a) version flag | `### probe` | exit 0 + stdout begins with `"simple-harness "`. |
| (b) config-error exit 2 | `### prepare` | exit 2 + stderr matches `/config error: --base-url is required/`. |
| (c) unreachable endpoint | `### start` | exit 3 + JSONL is non-empty + every line is parseable JSON + at least one `"event":"started"` line + at least one `"event":"completed"` line with `"exit_code":3`. |
| (d) SIGTERM exit 6 | `### interrupt` | Harness exits within 5s after SIGTERM + sidecar JSONL contains an `interrupted` event + terminal event has `"exit_code":6`. (Best-effort — SKIP if endpoint down.) |
| (e) session layout | `### collect` | Each session directory has `session.json` (valid JSON carrying top-level `session_id` + `started_at` + `ended_at` + `status` + `exit_code` + `events_path` + nested `config.base_url` + `config.model` + `config.workspace` + `config.permission` + `config.output_mode`) + `events.jsonl` (≥ 1 line, every line valid JSON) + `messages.jsonl` (non-empty). |

---

## Cross-references

* `docs/ARCHITECTURE.md` — the architectural rationale (component
  cut, module boundaries, four-system responsibility boundary).
  The architecture is internal; this document is the external
  contract.
* `docs/RECON.md` — the reference-harness study (Pi, Whip). Source
  for many V1 shapes (event protocol, child-process ownership,
  skills mechanism).
* `docs/ADR-001-implementation-language.md` — `Decision: Go`; the
  implementation language whose affordances (static binary,
  `os/exec`, `os/signal`, `encoding/json`) shape this contract.
* `/home/svend/flows/1010/SCOPE.md` — the binding scope this
  contract is derived from. SCOPE §36 (Harness Allocator
  readiness) is the §"## CLI Invocation Grammar" / "## JSONL
  Event Protocol" / "## Exit Codes" / "## Signal Semantics"
  authority; SCOPE §42 is the §"## Compatibility Policy"
  authority; SCOPE §17 is the §"## Session Identity and Layout"
  authority; SCOPE §§25–26 is the §"## Signal Semantics"
  authority; SCOPE §23 is the §"## Status States" authority; SCOPE
  §28 is the §"## Exit Codes" authority.
* `internal/event/event.go` — the V1 event-wire-shape source of
  truth (`protocol_version: "1"`, the 8 event types, the
  SessionConfig identity card, the mutex-serialized writes).
* `internal/session/writer.go` — the V1 session-directory layout
  source of truth (`session.json` + `messages.jsonl`, the
  atomic-rename write).
* `internal/perm/perm.go:78` — the V1 permission-rejection shape
  (`DecisionError{Stage: "policy", Reason: "permission_denied"}` →
  exit 4).
* `cmd/simple-harness/run.go:65` (`runUsage`), `cmd/simple-harness/main.go:112`
  (`usage`), `cmd/simple-harness/context.go:71` (`contextUsage`),
  `cmd/simple-harness/sessions.go:54` (`sessionsUsage`) — the
  help-text sources of truth for each subcommand.

---

## Pin

This document is pinned to the Simple Harness commit hash it
describes. The structural Pin line at the top of this document
(`Pin: simple-harness 95fcfc94deef461c51a205186735ba2b48f14fe3`) is
the version-bump anchor for the contract itself. Any change to this
document's contract — exit codes, event fields, CLI surface, signal
semantics, session layout, status enum — is a breaking change
and must follow the §"## Compatibility Policy" rule above (which for
in-document changes means: bump the document's Pin or rewrite the
relevant section with an additive compatibility note).
