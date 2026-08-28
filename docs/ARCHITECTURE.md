# Simple Harness — Architecture (V1 minimal)

> Status: Accepted
> Date: 2026-08-28
> Run: 001 · Deliverable 3 of 3 (RECON.md and ADR-001 already approved at commit `49770a6`)

# Scope

This document describes the **minimal V1 architecture** that the Simple
Harness Run 002+ will build. It does not yet describe a v2, an MCP layer,
a concurrency topology, or a future compaction design. Those are flagged
as **extension points** in §7.

The architecture is derived from the needs stated in `SCOPE.md` and
verified against the reference harnesses in `docs/RECON.md`. The
implementation language is **Go** (`Decision: Go`, recorded at
`docs/ADR-001-implementation-language.md:413`); Go-specific affordances
are named where natural. The architecture does not relitigate that
decision.

This document is **architecture, not code**. It contains no Go source,
no test scaffolding, and no module manifest. Run 002 introduces those.

# Responsibility boundary (verbatim restatement — binding)

Simple Harness sits inside a four-system responsibility boundary. The
following equalities are restated **verbatim** from SCOPE
("Responsibility boundaries") and are binding for every component below:

```text
DPMtF              = WHAT happens and in what sequence
Harness Allocator  = WHICH execution frontend executes a role
Model Allocator    = WHERE/HOW the model runs
Simple Harness     = HOW one assigned AI role interacts with its model, workspace, context and tools
```

A paraphrase is not a substitute. The four equalities together define the
fence around every component in this document.

From this boundary follows the **inviolable rule**:

> Simple Harness executes one role.
>
> It does not decide which role runs next.

Implications, applied to every component below:

- A component that **decides which role runs next**, sequences
  multi-role workflows, routes a verdict, or supervises a sub-harness
  is **orchestration** and belongs to DPMtF. Simple Harness does not
  contain it.
- A component that **selects a model**, allocates GPU/VRAM, loads or
  unloads a runtime, or chooses local-vs-cloud is **model lifecycle**
  and belongs to Model Allocator. Simple Harness consumes a resolved
  endpoint; it does not resolve one.
- A component that **chooses among Pi / Whip / Codex / Claude Code /
  OpenCode / Simple Harness** is **harness selection** and belongs to
  Harness Allocator. Simple Harness is one of the choices; it does not
  make the choice.

Components whose ownership would cross any of those lines are out of
scope for V1 and are not designed here.

# Module / component cut

This section names the concrete V1 components, the Go package paths the
implementation will live under, and a one-paragraph responsibility
statement for each. Every component names (a) what it owns, (b) what it
explicitly does **not** own, and (c) which of the four system boundaries
it sits on.

The package root is `github.com/simple-harness/simple-harness`
(stub path; Run 002 fixes the exact module path). Go module layout uses
`internal/` for all non-public packages; no package crosses the
internal/public boundary except at the CLI entry point.

## `cmd/simple-harness/` — CLI entry point

**(a) Owns:** process startup, flag parsing, signal handler
installation, and the choice between interactive (REPL) mode and
headless (`run` subcommand) mode. The binary name at the repository
root is `bin/simple-harness` — a thin POSIX `sh` wrapper that
`exec`s the compiled `simple-harness` runtime binary so that signals
delivered to `bin/simple-harness` reach the runtime process directly
(RUNS-BACKLOG §"Cross-run bound decisions"). The runtime itself is the
single static binary Go produces.

**(b) Does not own:** any orchestration, any harness-selection logic,
any model lifecycle. The `run` subcommand takes a `--base-url`,
`--model`, `--workspace`, and `--permission`; it does not pick
between them.

**(c) Boundary:** **Simple Harness.** This is the seam Harness
Allocator's future adapter reaches. SCOPE §§4, 5, 36.

## `internal/config/` — Configuration loader

**(a) Owns:** the configuration precedence chain (defaults → user
config at `~/.simple-harness/config.yaml` → project config at
`.simple-harness/config.yaml` → environment variables → CLI flags),
typed unmarshalling into a frozen struct, secret-resolution (env vars
and config only — never CLI where avoidable), and a `config show`
command that prints the fully resolved configuration for operator
inspection. `config show` is the only configuration-touching verb in
V1.

**(b) Does not own:** runtime decision-making. A resolved config is
read-only data passed into the loop; the loader never acts on it.

**(c) Boundary:** **Simple Harness.** SCOPE §§14, 29, 30.

## `internal/session/` — Session store + identity

**(a) Owns:** session identity (a stable `session_id` derived at
startup, persisted as `sessions/<session-id>/session.json`), the
append-only JSONL stores (`messages.jsonl`, `events.jsonl`), the
creation/lifetime rules, and the `sessions list` / `sessions show`
verbs. Persists execution history.

**(b) Does not own:** semantic long-term memory. There is no vector
store, no embedding memory, no autonomous memory retrieval. Session
persistence is execution history, not semantic long-term memory
(SCOPE §17, last sentence). An autonomous memory system is out of
scope (also SCOPE §17).

**(c) Boundary:** **Simple Harness.** SCOPE §§17, 36.

## `internal/context/` — Context assembly

**(a) Owns:** deterministic instruction ordering (minimal harness
system → external system/governance → loaded skills → task),
token-aware assembly (with `--model-context-window` honoured when
supplied), and fail-predictable overflow behaviour (an assembled
context that exceeds the limit produces an explicit observable
result, never a silently truncated conversation).

**(b) Does not own:** automatic summarisation. V1 does not implement
compaction; the architecture leaves a clean seam where a future
compactor would be plugged (see §7).

**(c) Boundary:** **Simple Harness.** SCOPE §§14, 18.

## `internal/model/` — Model client

**(a) Owns:** an OpenAI-compatible `/v1/chat/completions` client with
streaming (`text/event-stream` parsing), tool-call parsing,
configurable base URL, model name, optional API key (env-var or
config-only), `temperature`, `max_output_tokens`, and `request_timeout`.
Reads the resolved endpoint; never resolves one.

**(b) Does not own:** endpoint discovery, provider catalog
maintenance, credential storage beyond the supplied env var or
config field, retry/back-off orchestration (the loop owns retry
policy — see §3), or any vendor-specific protocol fork beyond what
the generic OpenAI-compat boundary supplies (SCOPE §6, §7).

**(c) Boundary:** **Simple Harness** (consumes a resolved endpoint).
SCOPE §6.

## `internal/event/` — Event/status surface

**(a) Owns:** the versioned JSONL event protocol (`protocol_version:
1`), the event-type enum emitted by the loop, the status enum
(SCOPE §23's `WAITING_FOR_MODEL`, `STREAMING`, `RUNNING_TOOL`,
`COMPLETED`, `FAILED`, `INTERRUPTING`, `INTERRUPTED`, `CLEANUP`, …),
and the output-mode split between terminal presentation and
machine-readable JSONL (`--output terminal|jsonl`). Emits to stdout
in JSONL mode and to a sidecar in interactive mode.

**(b) Does not own:** terminal scraping. The invariant from SCOPE §21
("Harness Allocator or another external controller must not need to
scrape decorative terminal output to understand Simple Harness
execution") is binding: machine-readable state lives in the JSONL
protocol, not in human-facing UI.

**(c) Boundary:** **Simple Harness.** SCOPE §§21, 22, 23, 36.

## `internal/tools/` — Tool registry + executor

**(a) Owns:** the V1 tool set (`read_file`, `write_file`,
`apply_patch`, `list_directory`, `search_files`, `grep`, `shell` —
the SCOPE §8 candidates), each tool's JSON-schema declaration, the
streaming tool-result shape, and a deterministic dispatch loop that
calls into the permission gate (§4) before invoking the tool
implementation. Tools are a fixed, registered set; V1 does not
implement a plugin marketplace or auto-discovery from disk.

**(b) Does not own:** arbitrary external tool loading (no
unrestricted third-party tool framework — SCOPE §15 standing
constraint). MCP tool exposure is an extension point, not a V1
feature (§7).

**(c) Boundary:** **Simple Harness.** SCOPE §§8, 9, 10, 11.

## `internal/perm/` — Permission gate

**(a) Owns:** the three explicit modes (`READ_ONLY`,
`WORKSPACE_WRITE`, `FULL_ACCESS`) and the SCOPE §13 pipeline:

```text
schema validation
        ↓
path normalization
        ↓
permission policy
        ↓
execution
```

Every relevant tool request passes through this pipeline before the
tool executor runs. Permission enforcement lives in deterministic Go
code — never in model-obeisance to prose instructions (SCOPE §13,
standing constraint). The active effective permission level is
externally observable (printed by `config show`, emitted as a
`status` event, and reflected in the CLI's startup banner; pick
the canonical surface in Run 004 where the gate itself is implemented).

**(b) Does not own:** workflow-level policy. The harness never
silently escalates permission (SCOPE §12 last sentence). The harness
does not negotiate permission with the model — it enforces.

**(c) Boundary:** **Simple Harness.** SCOPE §§12, 13.

## `internal/proc/` — Child-process ownership layer

**(a) Owns:** the lifecycle of every subprocess Simple Harness
spawns. Uses Go's `os/exec.Cmd` with `SysProcAttr{Setpgid: true}` so
each child is its own process group, with deterministic signal
escalation (`SIGTERM` then `SIGKILL` after a grace period) on
timeout or interruption, and a cleanup pass on every harness exit
so no `pytest`, no `make`, and no shell child outlives the harness
that started it. The shape is the same one RECON.md's `## Child
processes` section records for Pi's `BashOperations` abstraction
and for Whip's `os/exec` use.

**(b) Does not own:** unrelated user processes. The cleanup pass
targets process groups the harness itself created (SCOPE §27
last sentence).

**(c) Boundary:** **Simple Harness.** SCOPE §§11, 26, 27.

## `internal/skills/` — Skills loader

**(a) Owns:** discovery from `~/.simple-harness/skills/` and
`.simple-harness/skills/`, the per-skill `<skill-name>/SKILL.md`
shape (frontmatter `name`/`description` + body), the
progressive-disclosure model (names + descriptions in system prompt;
full body loaded on demand via a `/skill <name>` invocation or an
explicit read), and the `--skill <path>` CLI override. The
`cold-start` reference skill described in SCOPE §16 ships as a
frozen file under `share/skills/cold-start/SKILL.md`.

**(b) Does not own:** a plugin marketplace or auto-discovery from
arbitrary paths (SCOPE §15). V1 skills are **reusable instruction
packages**, not an internal orchestration framework (standing
constraint).

**(c) Boundary:** **Simple Harness.** SCOPE §§15, 16.

## `internal/diag/` — Context observability + diagnostics

**(a) Owns:** the `context show` and `context doctor` commands
(SCOPE §§19–20) that report approximate token consumption per
contributor (harness system, governance, skills, task, conversation,
tool schemas, tool results) and identify large contributors. Where
exact tokenisation is unavailable, the report labels its numbers
clearly as estimates (SCOPE §19 closing sentence).

**(b) Does not own:** automatic context surgery. Diagnostics
**report**; they do not silently discard content in a way that may
change execution meaning (SCOPE §20).

**(c) Boundary:** **Simple Harness.** SCOPE §§19, 20.

## `internal/loop/` — Headless runner + the model/tool loop

**(a) Owns:** the SCOPE §3 loop (task → assemble context → model
request → stream response → tool calls? → validate → authorize →
execute → record → append → next model request), the configurable
safety limits (`max_turns`, `max_tool_calls`, `max_execution_time`)
with their default values, the exit-code scheme (SCOPE §28: 0
success, 1 generic failure, 2 configuration error, 3 model/API
failure, 4 permission violation, 5 tool failure, 6 interrupted),
the deterministic headless `run` subcommand flow (no browser, no
interactive confirmation, works under tmux, deterministic exit
codes — SCOPE §5), and signal handling (`os/signal.Notify` for
`SIGINT`/`SIGTERM`, with the SCOPE §26 cascade).

**(b) Does not own:** orchestration, harness selection, model
lifecycle. The loop executes one assigned role against one
resolved model endpoint; it does not decide which role is next
and it does not pick the endpoint.

**(c) Boundary:** **Simple Harness.** SCOPE §§3, 5, 25, 26, 28, 32.

# Model/tool loop shape (SCOPE §3)

The conceptual loop from SCOPE §3 is reproduced and then concretised
for V1:

```text
task
 ↓
assemble context            (internal/context)
 ↓
model request               (internal/loop → internal/model)
 ↓
stream response             (internal/event, internal/model)
 ↓
tool calls?
 ├── no  → final response   (internal/loop)
 └── yes
        ↓
     validate               (internal/perm: schema check)
        ↓
     authorize              (internal/perm: path normalization → permission policy)
        ↓
     execute                (internal/tools → internal/proc for shell)
        ↓
     record                 (internal/event → JSONL; internal/session → messages.jsonl)
        ↓
     append                 (internal/context: tool result added to next model request)
        ↓
     model request
```

**Validate** in V1 is the schema-check step in the permission
pipeline: each tool call is checked against the tool's declared
JSON schema (tool name known, required parameters present, argument
types correct, paths within bounds, timeout within configured
limits). Malformed LLM output is untrusted input (SCOPE §31) and
never reaches the OS or filesystem unvalidated.

**Authorize** in V1 is the second and third steps of the permission
pipeline (path normalization then permission policy lookup), keyed
off the active mode (`READ_ONLY` / `WORKSPACE_WRITE` /
`FULL_ACCESS`). The mode is resolved at startup from CLI flag →
config → environment → defaults.

**Execute** in V1 is the tool executor's contract: each registered
tool has a single `Execute(ctx, call) → Result` signature; the
executor passes the validated and authorised call into the tool,
captures the streamed result, and propagates the result back up
the loop.

**Record** in V1 means an event line on the JSONL stream
(`event: tool_result`, SCOPE §21) and an entry in `messages.jsonl`
that ties the result to the originating `assistant` message and
the `tool_call` invocation.

**Append** in V1 means the tool result becomes part of the next
model request's messages list, with the assistant's preceding
tool-call included so the model sees the causal pair.

## Configurable safety limits

The three limits from SCOPE §3 are explicit in V1, with their default
values, their location in config (`internal/config`), and the
"exceeded a limit" behaviour:

```text
max_turns           default 32       (configurable; SCOPE §3)
max_tool_calls      default 128      (configurable; SCOPE §3)
max_execution_time  default 30m      (configurable; SCOPE §3)
```

Exceeding any limit produces an explicit observable result: the
loop emits a `status: LIMIT_EXCEEDED` event, the JSONL stream
records a final `completed` event with a non-zero exit code (1,
generic failure — the limit was hit before the role signalled
completion), and `session.json` records the limit-hit in its
termination metadata. The session is preserved; the role does
not crash the harness.

# Permission boundary placement (SCOPE §§12–13)

The three permission modes are restated verbatim from SCOPE §12:

```text
READ_ONLY         Permit appropriate repository inspection.
                  Reject modifications.

WORKSPACE_WRITE   Permit reading, writing, and patching the workspace
                  and running normal development/test commands.
                  Reject unauthorized writes outside the workspace.
                  This is the normal coding-agent mode.

FULL_ACCESS       Permit broader operations subject to OS permissions.
                  Must be explicitly selected.
```

Simple Harness **never silently escalates** permission (SCOPE §12
last sentence). The active mode is resolved once at startup and
held constant for the duration of the session.

## Enforcement placement

Permission enforcement lives in **`internal/perm`**, in a function
with the signature:

```text
Authorize(ctx context.Context, call tool.Call, mode Mode) Decision
```

`Authorize` runs as step 3 of the SCOPE §13 pipeline:

```text
schema validation  (internal/tools: schema check on the call)
        ↓
path normalization (internal/perm: resolve and normalize the path)
        ↓
permission policy  (internal/perm: lookup by (mode, tool, path))
        ↓
execution          (internal/tools: call the tool implementation)
```

The permission check is enforced in **deterministic Go code** —
the architecture explicitly forbids relying on the model obeying
prose instructions about which operations are allowed (SCOPE §13,
standing constraint).

## External observability of the active mode

The active effective permission level is externally observable in
three places (the canonical one is `config show`, per RUNS-BACKLOG
§"Cross-run bound decisions"):

- **`config show`** prints the resolved permission mode alongside
  the rest of the resolved configuration.
- **Startup banner** in interactive mode prints the active mode.
- **`status` event** in JSONL mode carries the active mode at
  `started` time so external controllers can see it without
  parsing human-facing output (SCOPE §13 last sentence).

# Session identity (SCOPE §17)

A **session identity** in V1 is a stable string `session_id`
generated at startup (UUIDv7 — sortable by creation time, low
collision risk). The session's on-disk layout is the one SCOPE §17
suggests:

```text
sessions/<session-id>/
    session.json
    messages.jsonl
    events.jsonl
```

## Persistence rules

- `session.json` — created at session start with the resolved
  configuration snapshot, the role/task identifier, and the
  created-at timestamp. Updated at termination with the final
  status and exit metadata.
- `messages.jsonl` — append-only JSONL of every message exchanged
  with the model (system / governance / skills / task / assistant
  / tool results), in the order the loop assembled them.
- `events.jsonl` — append-only JSONL of every event the harness
  emitted (matches the JSONL protocol from §6 below).

## What persists and what doesn't

**Persists:** every message the loop sent or received, every
event the harness emitted, the resolved configuration, the
session_id, the final status. The session is **execution history**.

**Does not persist:** nothing semantic. The system deliberately
does not extract a summary, an embedding, or a "what I learned"
note from the conversation.

## Verbatim SCOPE §17 closing line

> Session persistence is execution history.
> It is not semantic long-term memory.

An autonomous memory system is out of scope (also SCOPE §17).
SCOPE "Out of scope" §10 makes this explicit: no vector
databases, no embedding memory, no autonomous memory retrieval,
no cross-project agent memory. The architecture does not
implement these.

## Lifetime

A session is created at harness startup. It is closed on harness
exit (`completed`, `failed`, or `interrupted`). A future `resume`
verb (not in V1) would re-open a closed session by appending
new messages and events to the same `<session-id>/` directory.

# Event/status surface (SCOPE §§21–23)

The event protocol is **versioned JSONL** on stdout (or a sidecar
FIFO if stdout is being used for terminal presentation). The
schema is versioned from V1 forward; `protocol_version` is a
mandatory field on every event, and breaking schema changes bump
the version.

## Schema (V1)

Every event is a single JSON object on its own line, with at
minimum these fields:

```text
protocol_version  string   always "1" in V1
event             string   the event type (see enumeration)
timestamp         string   RFC 3339 UTC
session_id        string   the session this event belongs to
```

Event-specific fields extend the base schema per type. The
`completed` event additionally carries `exit_code` (int, SCOPE
§28). The `status` event carries a `status` field whose value is
one of the SCOPE §23 status values.

## Event types in V1

At minimum, the loop emits:

```text
started                 — session has begun; carries resolved config
status                  — emits the active status; allowed values per SCOPE §23:
                          STARTING, READY, WAITING_FOR_MODEL, STREAMING,
                          READING, SEARCHING, WRITING, PATCHING,
                          RUNNING_TOOL, INTERRUPTING, COMPLETED, FAILED,
                          CLEANUP, INTERRUPTED
model_request           — a model call is being made (carries turn number)
assistant_stream        — a streaming chunk arrived from the model
tool_call               — a tool was invoked (carries call_id, tool name)
tool_result             — a tool finished (carries call_id, status, duration)
completed               — final event; carries exit_code
```

## External subscription (V1)

V1 emits JSONL on **stdout** when `--output jsonl` is selected,
and on a sidecar `events.jsonl` file in `sessions/<session-id>/`
regardless of output mode. The terminal-mode presentation lives
on a separate stream/file descriptor (SCOPE §22).

## SCOPE §21 invariant (binding)

> Harness Allocator or another external controller must not need
> to scrape decorative terminal output to understand Simple
> Harness execution.

This is the load-bearing invariant of the whole event/status
surface. The architecture treats it as binding: if a status
value is not in the JSONL event stream, it does not exist for an
external controller. Statuses correspond to actual execution
state (SCOPE §23 last sentence) — not to inferred chain-of-thought
or hidden model reasoning.

# Extension points (not implemented designs — binding)

The following three areas are **extension points**, not designs.
V1 does not implement them; the architecture names them so future
Runs know where they would slot in. Each entry below is one short
paragraph: what the extension point is for, and which component
would own it when added. The architecture **does not** specify
implementation, message shapes, queue topology, or migration
story.

## Concurrency (SCOPE §§33, 34, 36; ADR §"concurrency")

For future safe parallel tool execution. The owning component
when added would be **`internal/loop`** (the loop owns the tool
dispatch order) plus a thin new package such as
`internal/concurrency/` for the per-path locking seam. SCOPE §32
specifies the priority: correctness → observability →
determinism → reliability → performance. V1 deliberately executes
tool calls sequentially. The current architecture does not make
future safe parallelism impossible — the loop's tool dispatch is
the only place that would change — but it does not include the
locking, scheduler, or shared-state machinery.

## MCP (Model Context Protocol) tool exposure (SCOPE §15; OOS §11)

For exposing tools defined by external MCP servers to the model,
subject to the same permission gate as built-in tools. The owning
component when added would be **`internal/tools`**, which would
grow a new tool source for MCP servers and apply the existing
permission pipeline to MCP tool calls. V1 deliberately does not
require MCP. Whip's MCP aggregation (RECON.md `## Tool exposure`)
is the shape being studied, but the architecture does not
replicate it. A future MCP implementation requires explicit
re-consideration of the permission and trust boundaries.

## Compaction (SCOPE §18 closing sentence; OOS §10 interaction)

For explicit context compaction when a session grows past the
configured context window. The owning component when added would
be **`internal/context`** (which already owns deterministic
instruction ordering and overflow-fail-predictable behaviour),
plus a thin new package such as `internal/compaction/` for the
summary engine. V1 does not implement automatic summarisation.
The architecture's context-assembly seam is the right place to
insert a compactor without changing the loop, the tools, or the
event protocol.

# SCOPE-candidate cross-reference

This section maps each V1 component to the SCOPE numbered sections
it contributes to. The audit trail: a reviewer can point at any
component and answer "which SCOPE candidates does it
implement?".

```text
Component                            SCOPE sections
─────────────────────────────         ─────────────
cmd/simple-harness/                   §4, §5, §36
internal/config/                      §14, §29, §30
internal/session/                     §17, §36
internal/context/                     §14, §18
internal/model/                       §6
internal/event/                       §21, §22, §23, §36
internal/tools/                       §8, §9, §10, §11
internal/perm/                        §12, §13
internal/proc/                        §11, §26, §27
internal/skills/                      §15, §16
internal/diag/                        §19, §20
internal/loop/                        §3, §5, §25, §26, §28, §32
```

Reading the table the other way:

```text
SCOPE §3   minimal loop               → internal/loop
SCOPE §4   interactive terminal       → cmd/simple-harness
SCOPE §5   headless execution         → cmd/simple-harness, internal/loop
SCOPE §6   OpenAI-compatible          → internal/model
SCOPE §7   model lifecycle separation → (no V1 component — boundary upheld by absence)
SCOPE §8   core tool set              → internal/tools
SCOPE §9   file reading               → internal/tools
SCOPE §10  deterministic modification → internal/tools
SCOPE §11  shell execution            → internal/tools, internal/proc
SCOPE §12  permission modes           → internal/perm
SCOPE §13  permission enforcement     → internal/perm
SCOPE §14  external instructions      → internal/config, internal/context
SCOPE §15  skills                     → internal/skills
SCOPE §16  cold-start skill           → internal/skills
SCOPE §17  sessions                   → internal/session
SCOPE §18  context management         → internal/context
SCOPE §19  context observability      → internal/diag
SCOPE §20  context diagnostics        → internal/diag
SCOPE §21  structured events          → internal/event
SCOPE §22  human/machine separation   → internal/event
SCOPE §23  observable status          → internal/event
SCOPE §25  Ctrl+C semantics           → internal/loop
SCOPE §26  SIGINT/SIGTERM             → internal/loop, internal/proc
SCOPE §27  child-process ownership    → internal/proc
SCOPE §28  exit codes                 → internal/loop
SCOPE §29  configuration              → internal/config
SCOPE §30  secret handling            → internal/config, internal/event
SCOPE §36  Harness Allocator readiness → cmd/simple-harness, internal/event, internal/session
```

# Distribution shape

The external contract Run 002+ builds toward:

- **Entry point:** `bin/simple-harness` — a committed POSIX `sh`
  wrapper at the repository root that `exec`s the compiled Go
  runtime binary. The `exec` (not fork-then-exec) is load-bearing:
  it ensures signals delivered to `bin/simple-harness` reach the
  runtime process directly, so SIGINT/SIGTERM behaviour is
  predictable (RUNS-BACKLOG §"Cross-run bound decisions").
- **Binary shape:** a single static Go binary. Go's stdlib
  covers everything V1 needs (`net/http` for the model client,
  `encoding/json` for JSONL, `os/exec` + `SysProcAttr{Setpgid: true}`
  for child-process ownership, `os/signal.Notify` for signals,
  `testing` for the unit suite); the deployment shape is the
  binary plus libc on Linux, nothing else.
- **Test suite contract:** `scripts/test.sh` — a committed
  executable at the repository root whose exit code is the
  authoritative test verdict (RUNS-BACKLOG §"Cross-run bound
  decisions"; SCOPE §39).
- **Exit codes:** the SCOPE §28 scheme (0 success, 1 generic
  failure, 2 configuration error, 3 model/API failure, 4
  permission violation, 5 tool failure, 6 interrupted) is the V1
  public contract.
- **`config show`:** the `simple-harness config show` command
  prints the fully resolved configuration, including the active
  permission mode (RUNS-BACKLOG §"Cross-run bound decisions";
  SCOPE §29).

# Non-goals (binding)

The architecture does **not** introduce, and a future revision
must not silently reintroduce, any of the following. Each is
either explicitly out of scope per SCOPE or follows from the
four-system responsibility boundary in §1.

- **Orchestration** — deciding which role runs next, sequencing
  multi-role workflows, supervisor/implementer/reviewer routing,
  planner loops, autonomous role selection. DPMtF owns this.
- **Model lifecycle** — GPU selection, VRAM allocation, model
  loading/unloading, runtime profile choice, local-vs-cloud
  decision, Model Allocator policy. Model Allocator owns this
  (SCOPE §7, "Out of scope" §5).
- **Harness selection** — choosing among Pi / Whip / Codex /
  Claude Code / OpenCode / Simple Harness. Harness Allocator owns
  this ("Out of scope" §4).
- **Wholesale cloning of Pi or Whip modules** — RECON.md is a
  study of references, not a parts list. Where the architecture
  borrows a shape (e.g. the `BashOperations`-style child-process
  abstraction RECON.md `## Child processes` records), it
  re-derives the shape from Simple Harness's needs; it does not
  name a Pi or Whip module as the V1 implementation.
- **Semantic long-term memory** — no vector store, no embedding
  memory, no autonomous memory retrieval, no cross-project agent
  memory (SCOPE §17, "Out of scope" §10). Session persistence is
  execution history; it is not semantic memory.
- **Generalised plugin/marketplace layer** — no plugin store, no
  unrestricted third-party tool-loading framework (SCOPE §15,
  "Out of scope" §12, §15).
- **Mandatory parallel tool execution** — V1 is sequential by
  design (SCOPE §32, "Out of scope" §14). Concurrency is an
  extension point (§7), not a V1 feature.
- **Multi-agent concurrency policy** — workspace-level
  multi-agent concurrency is Harness Allocator's responsibility,
  not Simple Harness's ("Out of scope" §15).

# What this document is NOT

- It is **not** an ADR. The language decision lives in
  `docs/ADR-001-implementation-language.md` and is referenced
  here only as the implementation language for the components
  above.
- It is **not** an implementation. No Go source, no module
  manifest, no test scaffolding lives in this document. Run 002
  introduces those.
- It is **not** a feature-list of Pi or Whip. The architecture
  borrows shapes the recon identified (child-process ownership,
  JSONL event protocol, progressive-disclosure skills,
  permission-pipeline enforcement), but it does not replicate
  Pi's TUI, Whip's browser-driver subsystem, or either harness's
  provider catalog.
- It is **not** a public V1 contract on its own. The public
  contract is the union of this document, the SCOPE,
  `docs/HARNESS-CONTRACT.md` (forthcoming in Run 014), and the
  exit-code / signal / session / event schemas. Until those are
  pinned together as a versioned contract (SCOPE §42), this
  document is internal architecture, not external promise.

# Cross-references

- `docs/RECON.md` — the reference-harness study this architecture
  borrows shapes from (loop, child processes, tool exposure,
  skills, OpenAI-compat layer).
- `docs/ADR-001-implementation-language.md` — `Decision: Go`; the
  Go affordances named in §2 and §6 above lean on this decision.
- `/home/svend/flows/1010/SCOPE.md` — the binding scope this
  architecture is derived from; the four-system responsibility
  boundary in §1 is restated verbatim from SCOPE
  "Responsibility boundaries".
- `/home/svend/flows/1010/RUNS-BACKLOG.md` §"Cross-run bound
  decisions" — the pinned external contract this architecture
  commits to (`bin/simple-harness` exec wrapper, `scripts/test.sh`
  suite contract, exit-code scheme, `config show` inspectability).
