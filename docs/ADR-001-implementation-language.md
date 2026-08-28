# ADR-001 — Implementation Language for Simple Harness

Status: Accepted
Date: 2026-08-28

## Context

Simple Harness is a small, independent, terminal-first AI execution
harness for running one externally assigned AI role against one externally
managed model endpoint (SCOPE §"Mission"). It is being built as a separate
repository at `/home/svend/simple-harness`, taking architectural
inspiration from Pi (TypeScript/Node) and Whip (Go) without becoming a
clone of either. SCOPE §2 requires an explicit ADR before any production
code is written, comparing at minimum **Python** and **Go** across
twelve mandated factors.

This decision is binding for the V1 implementation. The chosen language
sets the runtime contract, the build/deploy story, the testing surface,
and the operational shape of the standalone executable. A change later
is permitted but expensive.

## Summary

Two candidates weighed: **Python** and **Go**. Both are evaluated
honestly against the twelve factors from SCOPE §2, and the weighing
engages with what `docs/RECON.md` actually found: Pi is
TypeScript/Node on Node 22, Whip is a stripped Go ELF. Neither
candidate language is also a reference implementation; that
asymmetry is a fact, not a defect to paper over. The decision is
recorded below as `Decision: Go` and is **not** driven by ecosystem
consistency with surrounding DPMtF infrastructure (which is Python);
the decision would stand unchanged if DPMtF were rewritten in another
language. The technical case is that Go's stdlib coverage and
runtime model fit the load-bearing requirements SCOPE §5, §25–§27,
§32–§34 impose (single-binary deployment, deterministic signal
handling, child-process ownership with process groups, and an
architecture that does not preclude safe future concurrency), and
that Whip's design demonstrates the language makes this exact harness
shape cheap to build.

## Candidates

### Python

Python is the language of the surrounding DPMtF infrastructure
(Father, Model Allocator, parts of Harness Allocator) and is the
language in which most of the existing local test suite, tooling,
and operator scripts are written. Its standard library plus the
mature third-party ecosystem (`httpx`, `aiohttp`, `pytest`, `pydantic`,
`anyio`) covers the surface area Simple Harness needs — HTTP client,
async streaming, JSON schema validation, subprocess management,
structured logging — at high quality and with broad team familiarity.

Python's runtime model is interpretive with optional JIT (3.13+);
process startup is dominated by interpreter boot and import-time
side effects. The deployment story is therefore a distribution of
"a script plus a pinned interpreter plus a venv", not a single
binary, which has consequences for `simple-harness` as a
standalone CLI tool that may be invoked from tmux sessions, cron,
Harness Allocator adapters, or a Human shell.

The Python advantages SCOPE §2 names (development speed, mature
libraries, pytest, easy experimentation, existing local
familiarity) are real. They bias toward faster V1 iteration and
lower up-front cost. The Python disadvantages for the V1 problem
shape (single-binary deployment, deterministic signal handling
under multi-threaded runtimes, ergonomic process-group management,
cheap concurrency primitives, low runtime dependency footprint) are
also real, and they fall in the exact areas SCOPE §5, §25–§27, and
§32–§34 pin down.

### Go

Go is the language in which Whip is written. The Whip binary at
`/home/svend/.local/bin/whip` (v0.4.0) is a stripped static Go ELF
that ships as a single executable on `PATH`; `RECON.md` records that
Whip uses `os/exec` for child processes, `internal/acp.(*Bridge)
.ResumeSession` for ACP-bridge work, an embedded `rod`
browser-driver, and a `whip-bash-%d` log-naming convention that
mirrors Pi's `pi-bash-<id>.log` shape. None of those choices is
exotic — they are the affordances Go makes cheap.

Go's stdlib covers `os/exec` (with `SysProcAttr{Setpgid: true}` for
process groups), `os/signal.Notify` (with channels), `net/http`
(for both server and client, with streaming), `encoding/json`
(streaming decoder/encoder), and a built-in `testing` package. The
deployment story is a single static binary; the runtime dependency
footprint is the binary itself plus libc on Linux. Concurrency is
goroutines plus channels, which SCOPE §32–§34 explicitly invites
("The internal design should nevertheless avoid making future safe
parallel tool execution impossible").

The Go disadvantages for V1 are real and worth naming: more
ceremony in type-and-struct definitions than Python; less rich
ecosystem for fast LLM-protocol iteration; no equivalent of `pytest`'s
fixture-and-parametrize ergonomics out of the box; and a smaller
local familiarity pool relative to Python on this machine. Those
costs are amortised over the lifetime of the harness, which SCOPE
frames as long-lived.

## Factor weighing

### process reliability

Both languages can be made to run long-lived processes reliably;
neither is intrinsically unsafe. The difference is where the
default sits.

**Python.** A long-running Python process must guard against
interpreter-level surprises: GC pauses on multi-million-object
heaps, fork-without-exec pitfalls for `multiprocessing`,
non-deterministic teardown order on signals, and the recurring
problem of leaked file descriptors from third-party libraries that
swallow `OSError`. Python's signal story in multi-threaded programs
is officially "only the main thread receives signals", which is
correct but easy to get wrong — a `signal.signal()` registered in a
worker thread silently does nothing.

**Go.** The Go runtime gives every goroutine a small fixed stack,
a non-blocking preemption model, and a deterministic GC that emits
a brief stop-the-world pause rather than a long one. Signals arrive
on dedicated channels via `signal.Notify`, which composes cleanly
with `select` — the same idiom used everywhere else in the program.
For Simple Harness, which sits between a model endpoint, an event
stream, and a set of owned child processes (per SCOPE §11), the Go
model maps more directly to the problem shape.

The Go design is not free of footguns (goroutine leaks, channel
deadlocks, `init()` side effects) but its defaults are closer to
what a deterministic execution kernel needs.

### streaming

**Python.** `httpx` and `aiohttp` both stream SSE / chunked HTTP
cleanly via async iterators; `asyncio` gives back-pressure through
the queue. The model is mature and battle-tested in the LLM client
ecosystem.

**Go.** `net/http` exposes a streaming body via `io.Reader`, and the
stdlib's `encoding/json` decoder can drive an event loop off
`json.Decoder.Token()` or incremental `Decode()` calls. Streaming
is not a separate subsystem — it is the default mode of the
stdlib's I/O surface.

`RECON.md`'s `## Streaming` section records that both Pi and Whip
expose per-block streaming events (`agent_message_chunk`,
`agent_thought_chunk` in Whip; `contentIndex` + deltas with
`bash_execution_update` chunks in Pi). The shape is not language-
specific; it is a wire-protocol choice. Both candidate languages
can emit it. Go has a small edge because `json.Decoder` plus a
`for {}` loop is a closer match to the "single goroutine driving
the stream" idiom than Python's `async for` plus a task.

### signal handling

This is the strongest single-factor case for Go in this ADR.

**Python.** `signal.signal()` is a thin wrapper around `sigaction`
that registers a C-level handler invoked on the main thread. The
handler must be cheap, must not acquire the GIL-blocking locks,
and cannot meaningfully compose with `asyncio` (the standard
pattern is to set an `Event` from the handler and let the event
loop observe it). Cross-platform semantics around `SIGTERM`
behaviour on Windows and around process groups on Linux are a
known source of bugs.

**Go.** `os/signal.Notify` is a stdlib primitive. Signals arrive on
a channel that any goroutine can `select` on. Composing "cancel
the active model request, tear down the child, persist the session,
emit the final event, exit" in one readable `select` block is
idiomatic.

`RECON.md`'s `## Interrupts` section makes this load-bearing for
Simple Harness. SCOPE §25 and §26 demand explicit, tested,
deterministic signal behaviour for both interactive Ctrl+C and
headless SIGINT/SIGTERM. Whip's "esc cancel" string in the binary
and Pi's five-abort-cascade (`abortRetry`, `abortCompaction`,
`abortBranchSummary`, `abortBash`, `agent.abort()`) both encode
"we took this seriously". Go makes that seriousness cheap at the
runtime boundary.

### headless execution

SCOPE §5 demands headless execution as a primary requirement:
"require no browser, require no interactive confirmation unless
explicitly configured, work correctly under tmux, produce
deterministic exit codes, emit machine-readable execution events,
respond correctly to SIGINT/SIGTERM".

Neither candidate language is intrinsically headless-unfriendly.
The question is which language's stdlib and idioms make the
headless surface clean.

**Python.** Headless execution in Python means "don't import
`readline`, don't import any curses-based TUI, drive everything
from `argv` and stdin". This is achievable but requires discipline
— Python's TUI ecosystem is large and attractive, and a careless
import can pull in `pygments` or `prompt_toolkit` on a code path
that should be pure.

**Go.** Headless execution in Go is the default mode of any program
that doesn't import a TUI library, which most programs don't.
There is no hidden interactive layer to forget about.

Both pass; Go has a small edge because there is less to forget.

### deployment simplicity

This is the second strongest single-factor case for Go.

**Python.** Distribution is "a script + a pinned interpreter + a
venv + a requirements file + an entry point script". Even with
`shiv`, `pyinstaller`, or `pdm-pex`, the produced artefact is
either a directory tree, a zipapp, or a binary that bundles an
interpreter. Cross-machine reproducibility requires either the
same Python version on every host or a bundled interpreter.

**Go.** Distribution is the single static binary the toolchain
produces by default. `go build` → `simple-harness`. No interpreter,
no venv, no `requirements.txt`, no `pip install` step on the
target host. Cross-compilation (`GOOS=linux GOARCH=amd64 go
build`) is a one-liner.

`RECON.md` notes that Whip ships as a stripped Go ELF at
`/home/svend/.local/bin/whip`. That is the deployment shape SCOPE
§5 and §36 imply for Simple Harness: a CLI invoked from tmux,
from Harness Allocator adapters, or from a Human shell, with no
runtime prerequisites beyond libc.

### concurrency

**Python.** Concurrency in Python is `asyncio` (single-threaded
cooperative), `threading` (GIL-bottlenecked for CPU work but fine
for I/O), or `multiprocessing` (separate interpreters, expensive
startup, IPC overhead). The async model is well-developed for
network I/O but awkward for "many concurrent things that share
state", because shared state in `asyncio` means careful
ownership and disciplined cancellation.

**Go.** Concurrency is goroutines, which are cheap (a few KB of
stack, scheduled cooperatively-ish onto OS threads by the runtime),
and channels, which compose. The model is "share memory by
communicating"; race conditions are detected at runtime by the
built-in race detector (`go test -race`).

SCOPE §32–§34 explicitly say V1 may be sequential but the
architecture must not preclude future safe parallel tool execution.
A goroutine-and-channel model is the natural starting point for
that future; a single-threaded `asyncio` model is also fine but
the path from "single-threaded event loop" to "many concurrent
tool calls" is a bigger redesign than "goroutine per tool call".

`RECON.md`'s `## Execution loop` and `## Child processes` sections
record that Whip's child-process story uses Go's `os/exec` with
concurrent goroutines — i.e., Whip's "concurrency concepts" are
already implemented in Go, and Simple Harness can take inspiration
from a Go reference rather than re-deriving the pattern.

### child-process ownership

SCOPE §27 makes child-process ownership a runtime contract
requirement: process groups, signal propagation, timeout handling,
controlled SIGTERM, controlled escalation where required, and the
guarantee that a terminated harness does not routinely leave
behind pytest processes, build commands, shell children, or
tool-owned background processes.

**Python.** `subprocess.Popen` plus `os.setsid` plus `os.killpg`
plus careful teardown. Achievable but requires the developer to
remember the process-group dance; the stdlib does not enforce it.
The `psutil` library helps (process enumeration, `wait_procs`) but
is a third-party dependency for a load-bearing responsibility.

**Go.** `os/exec.Cmd` plus `SysProcAttr{Setpgid: true}` is one
line, and the pattern is widely documented. `syscall.Kill(-pid,
syscall.SIGTERM)` (negative pid = process group) is the matching
teardown. The stdlib covers it; the developer does not have to
remember.

`RECON.md`'s `## Child processes` section notes that Pi's
`BashOperations` abstraction (`dist/core/bash-executor.js` lines
27–84, with `pi-bash-<id>.log` spillover and an
`abort_bash` RPC command) is the shape Simple Harness wants —
independent of the language — and that Whip implements the same
shape with `os/exec` plus `whip-bash-%d` log naming. The Go
implementation is shorter and more obviously correct.

### testability

**Python.** `pytest` is mature: fixtures, parametrize, markers,
tmp_path, monkeypatch, capsys, and a rich plugin ecosystem. The
ergonomics are well known. Test discovery and assertion failure
output are best-in-class.

**Go.** The built-in `testing` package covers table-driven tests,
subtests (`t.Run`), `t.TempDir`, `t.Cleanup`, and the race
detector. There is no fixture-injection framework by default;
idiomatic Go uses constructor functions and table-driven tests
instead. The `testify` ecosystem (assertions, mocks) is the
common extension, but stdlib-only is sufficient for most needs.

Both are excellent. SCOPE §39's test catalogue
(configuration precedence, permission enforcement, shell success /
non-zero / timeout, signal handling, child-process cleanup, etc.)
is achievable in either language. pytest is slightly more
ergonomic; Go's stdlib is slightly more uniform. The difference is
not enough to drive the decision.

### maintainability

**Python.** Dynamic typing, duck typing, and a permissive import
system make Python code short. They also make refactoring riskier
— a renamed method on a dataclass won't break until the
attribute is touched, and the absence of a compiler pass means
typos in attribute names survive into runtime.

**Go.** Static typing, a single canonical formatter (`gofmt`),
and an explicit visibility model (exported vs unexported names)
make Go code more verbose but more uniform. Refactoring is
mechanical: the compiler finds the call sites.

For a small standalone tool intended to outlive its first author,
Go's refactorability has real value. Pi's source tree is a case
study in the same trade-off — TypeScript gives you the verbosity
of Go with less of the runtime story, and the project mitigates
that with discipline and tests.

### implementation complexity

For V1, the minimum harness has roughly: an OpenAI-compatible HTTP
client with streaming, a tool-call loop with seven tools (read,
write, edit, grep, find, ls, bash), a permission enforcement
boundary, a session store, an event emitter, a signal handler,
and a child-process supervisor.

**Python.** A first cut can be very short — `httpx` for the HTTP
client, `pytest` for tests, `subprocess` for child processes,
`json` for messages, `argparse` for CLI. Iteration speed is the
strongest Python advantage.

**Go.** A first cut is longer: structs for messages, interfaces
for the tool surface, explicit error returns, a `main` package
plus an internal package split. The code is more boilerplate but
also more obviously correct.

The two balance out for the V1 size: Python wins on time-to-first-
working-version, Go wins on time-to-refactor-without-breaking.
For a project whose stated end state is "a small, boring,
predictable, observable execution kernel" (SCOPE §"Mission"),
the second axis matters more.

### dependency footprint

**Python.** The V1 stack pulls in `httpx` (or `aiohttp`), `pytest`,
`pydantic` (or similar) for tool-schema validation, and a small
number of supporting libraries. A `requirements.txt` plus a pinned
interpreter is the realistic deployment story. The footprint is
modest but real, and version drift between local dev and the
target host is a recurring operational pain.

**Go.** The V1 stack uses stdlib for HTTP, JSON, signals, subprocess,
process groups, and CLI flag parsing. The optional `go.mod` may
list `mattn/go-sqlite3` (or a pure-Go alternative) for the session
store and one or two small libraries for ergonomics. The runtime
binary has no third-party dependency beyond libc.

For a standalone tool that SCOPE §35 demands be operable with no
Model Allocator, no Harness Allocator, and no DPMtF, the smallest
runtime footprint is a real advantage. It also matches what
`RECON.md` observed for Whip.

### long-term independence from DPMtF

This factor is the one the SCOPE flags most explicitly: the
decision must not be made solely for ecosystem consistency with
the surrounding DPMtF infrastructure, which is Python. SCOPE §2:
"Do not assume Python merely because surrounding DPMtF
infrastructure uses Python."

The weighing is honest about the asymmetry. DPMtF's surrounding
infrastructure is Python; Pi is TypeScript/Node; Whip is Go.
**Neither candidate language is also a reference implementation.**
That asymmetry matters in two ways.

First, neither reference is a candidate language — so the
"language consistency with our reference" argument is unavailable
in either direction. Choosing Go does not mean "we will copy Whip"
(Pi demonstrates the same primitives in a different language),
and choosing Python does not mean "we will copy Pi" (Whip
demonstrates the same primitives in yet another language).

Second, the references are evidence about the cost of staying off
their respective platforms. Pi ships its primitives in
TypeScript/Node — but TypeScript/Node gives Pi affordances
(`stream` ecosystem, `AsyncIterator`, npm package availability for
JSON-RPC clients) that neither Python nor Go gives for free, and
that Simple Harness would have to re-derive if it wanted Pi's
exact shape. Whip ships its primitives in Go — and Go gives Whip
affordances (single-binary deployment, `os/exec` process groups,
goroutines for the per-tool concurrency design) that map directly
onto Simple Harness's stated needs.

In this factor's weighing, "ecosystem consistency with DPMtF"
appears as one input — it would make Python slightly easier for
operators who already maintain DPMtF — and is explicitly *not*
the deciding input. The decision would stand unchanged if the
surrounding DPMtF infrastructure were rewritten in another
language, because the technical factors (deployment, signals,
child-process ownership, concurrency) carry the weight.

## Decision

Decision: Go

## Consequences

Simple Harness V1 is implemented in Go.

**What this commits Simple Harness to:**

- A single static binary as the distribution artefact (`go build`
  → `simple-harness`), matching the Whip deployment shape.
- Use of Go stdlib for HTTP, JSON, signals, subprocess, and CLI
  parsing; minimal third-party dependencies.
- A goroutine-and-channel concurrency model in the core loop,
  with V1 executing tool calls sequentially per SCOPE §32 but the
  architecture preserving the option for future parallel tool
  execution per SCOPE §33.
- Process-group ownership of every spawned child (`Setpgid` on
  `SysProcAttr`), with SIGTERM-then-SIGKILL teardown, as the
  load-bearing runtime contract per SCOPE §27.
- `signal.Notify` channels composed with `select` as the
  cancellation primitive for both interactive Ctrl+C and headless
  SIGINT/SIGTERM, per SCOPE §§25–26.
- A `go test -race` standard for the test suite, with the stdlib
  `testing` package plus table-driven tests as the default
  idiom.

**What this commits Simple Harness out of:**

- Using `pytest`, `pydantic`, `httpx`, `aiohttp`, or any Python-
  ecosystem library. Any such library would have to be re-
  implemented (or its Go equivalent added to `go.mod` after an
  explicit decision in a later ADR).
- A `requirements.txt`-style dependency story. Simple Harness V1
  ships as a binary, not as an installable Python package.
- A `pip install`-based onboarding. Operators either download the
  binary or build from source with the Go toolchain.
- Treating the Pi implementation as a code reference. Pi's design
  ideas (per-block streaming, `BashOperations` seam,
  `BashExecutionMessage` as a first-class message type, abort
  cascade, session JSONL with version migration) are *conceptual*
  references and are translated into Go idioms, not ported
  verbatim.

**Reversibility.** Switching implementation languages later is
permitted but expensive: every tool schema, every event envelope,
every test, and every external contract would have to be re-
implemented. This is acceptable because the public V1 contract
(SCOPE §42 — CLI invocation, event schema, exit codes, signal
semantics, session identity, permission semantics) is
language-agnostic and remains stable across a hypothetical future
rewrite.
