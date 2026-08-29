# ADR-002 — Concurrency Architecture for Simple Harness

Pin: simple-harness 190e0b5538093443d4906a6e0def4648b5b3ea90
Status: Accepted
Date: 2026-08-29
Candidate: 015 (SCOPE §§32–34)

## Context

Simple Harness is a small, terminal-first AI execution kernel built
around a sequential V1 loop. SCOPE §"Mission" frames the harness as
"a small, boring, predictable, observable execution kernel"; SCOPE
§32–§34 carry that stance into the concurrency dimension:

- SCOPE §32 — "Sequential V1 execution with concurrency-ready
  architecture + priority order (correctness → observability →
  determinism → reliability → performance)."
- SCOPE §33 — "Future parallel read execution — the architecture
  MAY permit independent reads + searches; V1 is not required to
  implement."
- SCOPE §34 — "Future per-path locking — the
  read/read-parallel, read/write-synchronize, write/write-serialize
  table + the explicit 'Do not implement a complex locking
  subsystem before parallel execution exists. Document the
  extension point instead' directive."

This ADR reasons about the concurrency architecture against the
binary at `190e0b5` (the Run 014 / handoff 052 Git-gate close). No
parallel execution is enabled by this Run; all verdicts ground in
present-day kernel code or in the SCOPE §32–§34 directives
themselves. Whip evidence is observational only (OD-1 has not
closed; no local Whip source clone; the binary at
`/home/svend/.local/bin/whip` is a stripped Go ELF per Run 001's
`docs/RECON.md`).

## Summary

The Run 015 concurrency review engages four concepts in SCOPE
priority order (correctness → observability → determinism →
reliability → performance). For each concept the verdict grounds
in the Simple Harness kernel code at `190e0b5` (not in a
hypothetical post-concurrency future) and engages with the SCOPE
§§32–§34 directives verbatim. The result:

- Two concepts receive `DEFER` verdicts with crisp trigger
  conditions — `Parallel independent reads` and
  `Parallel searches`. Neither is enabled today; both are
  architectural possibilities under SCOPE §33, but the SCOPE §32
  priority order + V1's sequential-by-design constraint counsel
  waiting for evidence that the determinism-vs-throughput
  trade-off is worth opening. The trigger conditions name the
  specific benchmark, surface, and protocol-version change that
  would flip `DEFER → ADAPT/ADOPT`.
- One concept receives an `ADAPT` verdict — `Per-path locking`.
  SCOPE §34 explicitly directs "Document the extension point
  instead. Do not implement a complex locking subsystem before
  parallel execution exists." This ADR is the documentation
  surface; the kernel's existing mutex primitives are NOT
  per-path locks (they guard the tool registry, the JSONL
  emitter, and the session writer). Documenting the
  read/read-parallel, read/write-synchronize, write/write-serialize
  table here satisfies SCOPE §34 without changing observable
  behavior.
- One concept receives an `ADOPT` verdict — `Tool-call
  scheduling`. The kernel is ALREADY sequential per turn; the
  single `go func() { waitDone <- cmd.Wait() }()` goroutine
  inside `internal/tools/builtins/shell.go` exists to wait for a
  subprocess exit, not to schedule parallel tool calls. The
  current kernel honors the SCOPE §32 priority order
  mechanically; no behavior change is needed.

No kernel code is modified by this Run. No parallel execution is
enabled. The conditional handoff 054 ("named extension-point
interfaces and invariant tests") does NOT fire — none of the
four verdicts demands landed code; the `Per-path locking` ADAPT
is documentation-only per SCOPE §34's explicit directive.

## Decision drivers

1. **SCOPE §32 priority order.** Correctness → observability →
   determinism → reliability → performance. A speculative
   parallel-tool-execution architecture ranks higher than the
   kernel can defend right now: correctness (deterministic event
   ordering, no interleaved JSONL) and observability (every
   tool_call has a matching tool_result on the wire) are
   non-negotiable; performance is the bottom of the stack and is
   explicitly traded off against the others when they conflict.
2. **Present-day kernel code at `190e0b5`.** Every "this concept
   is present" claim cites the file path + line range of the
   code that bears on the verdict. No claim is made about a
   hypothetical future kernel.
3. **SCOPE §34's explicit directive.** "Do not implement a
   complex locking subsystem before parallel execution exists.
   Document the extension point instead." This is a direct
   ruling against speculative land-code for per-path locking; the
   ADR is the named documentation surface.
4. **Whip evidence is observational only.** `strings
   /home/svend/.local/bin/whip` extracts include `lock`, `Lock`,
   `Locked`, `/schedul`, `lockSlow`, `lockAddr`, `lockRankStruct`,
   `scheduler`, `runtime.goroutineProfileState` and similar — Go
   runtime/stdlib scheduler symbols that confirm Whip uses
   standard Go concurrency primitives (binary-level observation
   only; no source-level visibility because the ELF is stripped
   per `docs/RECON.md`).
5. **A `DEFER` with a crisp trigger condition beats a speculative
   `ADAPT/ADOPT`** (Human-supervisor kickoff, 2026-08-29). A
   `DEFER` whose trigger names the exact observation, benchmark,
   or new requirement that would flip the verdict is the
   well-formed answer when no present-day demand exists.

## Parallel independent reads

Two independent `read_file` calls — say, on `internal/loop/loop.go`
and `internal/event/event.go` to compare mutex usage — land on
separate paths. The question is whether the kernel should execute
them concurrently, partition the per-call work across goroutines,
and converge the tool_result events on the JSONL sidecar in some
well-defined order.

**Simple Harness evidence.** `internal/loop/loop.go:120-194` —
the `Run` struct does NOT spawn goroutines ("The Run does NOT
spawn goroutines itself; RunOne is the unit of work the cmd calls
once per prompt."). `internal/loop/loop.go:585-754` — the
`RunAgent` multi-turn loop dispatches accumulated tool calls in a
plain single-threaded `for idx := 0; idx < len(perIndexAccum); idx++`
loop. `internal/tools/builtins/read_file.go:122` — the
`Execute` method is a pure function: it reads bytes from
`os.File`, scans for NUL, splits by line, and returns a
`Result`. It does not own a goroutine. The dispatch pipeline in
`internal/tools/registry.go:62` calls `Execute` synchronously
(one tool at a time, in the calling goroutine).

**SCOPE anchors.** §32 makes V1 sequential-by-design and pins
the priority order (correctness → observability → determinism →
reliability → performance); §33 explicitly notes "It is not a
requirement that V1 implement parallel execution" and frames it
as a V1.x or V2 possibility. §34 bars implementing a "complex
locking subsystem" before parallel execution exists — the
extension-point table is the V1 deliverable, not the
implementation.

**Whip evidence (observational only).** `strings
/home/svend/.local/bin/whip` returns Go runtime/stdlib scheduler
symbols (`runtime.goroutineProfileState`, `lockSlow`,
`http2.priorityWriteSchedulerRFC9218`, etc.) consistent with
Whip using standard Go concurrency primitives; the binary is
stripped, so the source-level mechanics (which goroutine fire on
which tool, which scheduler discipline) are not source-citable.

**Reasoning.** The kernel does not enable parallel reads today
and SCOPE §33 explicitly does not require it. SCOPE §34's
directive applies independently to the locking surface. No
present-day benchmark, test, or workload in the harness test
suite (`./scripts/test.sh`) motivates turning this on. Speculative
parallel-read implementation would force a `protocol_version`
bump on the JSONL event stream (the current ordering invariant
"every `tool_call` event is matched by exactly one `tool_result`
in wire order" would need a convergence discipline documented in
`docs/HARNESS-CONTRACT.md`).

Trigger: a `ReadFileResult`-shaped benchmark on a representative
workspace replay shows that the per-turn read bottleneck is the
median-task wall-clock critical path AND the kernel gains a
first-class `protocol_version` bump (a documented in-protocol
"independent-call" marker so an external controller can
distinguish parallel-fanout turns from sequential ones) AND the
SCOPE §34 read/read-parallel, read/write-synchronize,
write/write-serialize table lands in code rather than as
documentation. Until those three are demonstrably in hand, the
verdict remains `DEFER` because implementing without the demand
ranking higher than determinism violates SCOPE §32.

Verdict: DEFER

## Parallel searches

Two independent `grep` calls (or two `search_files` calls) on
disjoint subtrees — say, "find every `sync.Mutex` usage under
`internal/event`" plus "find every `sync.Mutex` usage under
`internal/session`" — would benefit from parallel execution in a
deterministic-by-test world. The question is whether the kernel
should fanout the call set across goroutines and converge.

**Simple Harness evidence.** `internal/tools/builtins/grep.go:132`
— `Execute` shells out to `rg` when available (`rg --no-heading
--line-number --no-messages --with-filename [...] <pattern> <path>`),
else falls back to a `filepath.WalkDir` + `bufio.Scanner` +
`regexp.MatchString` walk. Either path returns a `Result` with a
sorted `(File, Line)` slice. The `sort.Slice` step at
`internal/tools/builtins/grep.go:250-255` is the existing
determinism seam — any parallel-fanout path must converge on the
same sorted output regardless of which goroutine finished first.
`internal/tools/builtins/search_files.go:100` —
`Execute` runs the same `filepath.WalkDir` walk as the
`grep` native-fallback path; sorted before slice at line 211.

**SCOPE anchors.** §32 + §33 apply identically to search-tools as
to read-tools: V1 is sequential-by-design, the priority order
bottoms out at performance, and the architecture MAY permit
parallel searches but V1 is not required to. §34's locking-table
directive is the V1 deliverable.

**Whip evidence (observational only).** Whip's binary contains
`runtime.heldLockInfo`, `lockRankStruct`, and `lockSlow`
indicators consistent with internal mutex use in the search/exec
path; binary-level observation only. There is no source-citable
claim about which path Whip parallelizes or in what order it
converges — that requires OD-1 to close.

**Reasoning.** The same argument applies as for parallel reads:
the kernel does not enable parallel searches today; SCOPE §33
explicitly does not require it; SCOPE §32's priority order
bottoms out at performance; and turning it on without a present-
day demand would force a `protocol_version` bump on the JSONL
event stream. The determinism seam (`sort.Slice` post-collection
at `grep.go:250-255`) is the natural convergence point, but
adopting it requires the workload evidence AND the protocol
discipline AND the locking subsystem — three preconditions that
SCOPE §34 explicitly defers.

Trigger: a recorded replay of a multi-search turn (a session
containing >= 3 independent `grep` or `search_files` calls per
turn on a workspace with ≥ 1,000 matching files) shows the
sequential median turn latency exceeds a named budget AND the
kernel's wire protocol gains a `protocol_version` bump with a
documented "parallel-search fanout" marker AND the SCOPE §34
table moves from documentation to implementation. Until those
three are demonstrably in hand, the verdict remains `DEFER` for
the same reason: implementing without the demand ranking higher
than determinism violates SCOPE §32.

Verdict: DEFER

## Per-path locking

Concurrent tool calls that share a workspace path — say, two
`read_file` calls on `docs/HARNESS-CONTRACT.md` from two parallel
goroutines, or a `read_file` racing with a `apply_patch` on the
same path — need a per-path lock discipline to keep the
filesystem state coherent. The question is whether Simple Harness
should implement the read/read-parallel, read/write-synchronize,
write/write-serialize table now, in code, with a real
`internal/tools/pathlock` package; or whether it should
document the table now and defer the implementation to a future
Run that has parallel execution in hand.

**SCOPE anchor (binding).** §34 says: "Future per-path locking —
the read/read-parallel, read/write-synchronize, write/write-
serialize table + the explicit 'Do not implement a complex
locking subsystem before parallel execution exists. Document the
extension point instead.'" The directive is unambiguous: **document
the extension point, do not implement.** This is the canonical
"ADAPT in the documentation surface, no code lands" case.

**Simple Harness evidence that the existing mutexes are NOT
per-path locks.** `internal/tools/registry.go:18` —
`mu sync.RWMutex` is the tool registry's mutation lock; it
guards the `Register`/`Get`/`Names` map against startup-time
concurrent registration. `internal/event/event.go:93` —
`mu sync.Mutex` is the emit-mutex; it serializes writes so a
streaming-callback goroutine cannot interleave the bytes of one
JSONL line with another. `internal/session/writer.go:26` —
`mu sync.Mutex` is the write-ordering mutex; the comment at
`internal/session/writer.go:13-16` explicitly states "The Writer
is NOT safe for concurrent use; the cmd-side serializes message
appends ... The mutex is a defence-in-depth against future
callers; today no goroutine shares the Writer." None of these
three primitives guards a per-path access — they guard
registry-map integrity, JSONL-line atomicity, and
session-writer-mutation order respectively.

**The extension-point table (SCOPE §34, documented here per
directive).**

| Operation A | Operation B | Lock discipline |
|------------|-------------|-----------------|
| read (read_file) | read (read_file) | parallel — no lock |
| read (read_file) | write (apply_patch / write_file) | synchronize — write blocks reads |
| write (apply_patch / write_file) | write (apply_patch / write_file) | serialize — one at a time |

The semantics ground in the OpenAPI/FSM model: a read observes a
point-in-time snapshot of the file's bytes, a write mutates the
file's bytes; two parallel reads observe the same snapshot
(parallel is safe); a read racing with a write returns either
the pre-write or post-write bytes but never a partial mutation
(synchronize); two parallel writes would corrupt the file unless
serialized (serialize). The table is the load-bearing future
contract for any parallel-tool-execution kernel and is
documented here exactly because SCOPE §34 mandates documentation
rather than implementation.

**Whip evidence (observational only).** Whip's binary contains
`lockSlow`, `lockRankStruct`, `runtime.heldLockInfo`, and
`sync/atomic.Bool` references consistent with internal mutex use;
binary-level observation only. Whether Whip implements a per-
path lock discipline, a global dispatcher lock, or no
path-level synchronization at all is not source-citable.

**Reasoning.** SCOPE §34's "Document the extension point instead"
directive binds this ADR to land the table here and not the
implementation. The kernel's three existing mutex primitives are
NOT per-path locks; documenting the table adds surface without
duplicating any existing primitive. ADAPT fits: a documented
extension point that documents what the future implementation
will look like, anchored in the SCOPE §34 table verbatim, with
no behavior change to the V1 kernel.

Verdict: ADAPT

## Tool-call scheduling

How does the kernel schedule model-driven tool calls? Two
possibilities exist: per-turn sequential dispatch in the calling
goroutine (the kernel's current behavior) or some form of
scheduler-driven dispatch (worker pool, priority queue,
parallel fanout). The question is which one matches the Simple
Harness contract, and which one the kernel implements today.

**Simple Harness evidence.** `internal/loop/loop.go:120-194` —
the `Run` struct's docstring names the unit of work:
"RunOne is the unit of work the cmd calls once per prompt."
The struct holds `cfg`, `client`, `em`, `out`, `ledger` — no
scheduler field, no worker pool, no goroutine launch. The
interactive REPL's single-goroutine discipline is explicit at
`internal/loop/loop.go:158-159, 189, 222` ("the interactive REPL
is single-goroutine; the scanner goroutine only feeds the
prompt loop; the prompt loop is the sole caller of RunOne ...
no locking is required"). The V1 multi-turn loop at
`internal/loop/loop.go:514-754` (`RunAgent`) iterates over
tool-call accumulators with `for idx := 0; idx < len(perIndexAccum); idx++`
and dispatches each through `r.cfg.Tools.Dispatch(ctx, toolsCall, ws, pol, perm.Authorize)`
synchronously. The dispatch pipeline at
`internal/tools/registry.go:86-111` is itself synchronous: `Get`
→ `auth` → `Execute`, no goroutines.

The only goroutine currently in the kernel —
`internal/tools/builtins/shell.go:314` —
`go func() { waitDone <- cmd.Wait() }()` — exists to wait for a
subprocess's exit, NOT to schedule parallel tool calls. The
goroutine is a single-purpose helper for `cmd.Wait()` so the
cancel/SIGTERM/SIGKILL escalation can fire on a separate path
from the main `select`. It is shell-tool-internal; it does not
span tool calls, does not own the registry, does not own the
dispatcher. Calling it "a goroutine" understates what it is;
calling it "a scheduler" overstates what it does.

**SCOPE anchors.** §32 mandates the priority order:
correctness → observability → determinism → reliability →
performance. The kernel's current per-turn sequential scheduling
serves correctness (deterministic JSONL event order), serves
observability (one tool_call ↔ one tool_result match), serves
determinism (same input → same event sequence, byte-for-byte),
and serves reliability (no partial-failure interleaving). It
trades off performance — and §32 says that is the right trade
when the four above conflict with it.

**Whip evidence (observational only).** Whip's binary contains
`runtime.schedInit`, `lockSched`, `sched.ncpu`,
`runtime.goroutineProfileState`, and `/schedul` indicators
consistent with internal scheduler use; binary-level observation
only. The discipline (sequential per turn vs. parallel fanout
vs. mixed) is not source-citable.

**Reasoning.** The kernel IS implementing this concept correctly
today. Sequential per-turn scheduling matches SCOPE §32's
priority order, honors SCOPE §33's V1-permitted sequential
default, and aligns with the documented unit-of-work contract at
`internal/loop/loop.go:120-194`. The single goroutine in shell.go
is correctly characterized as a wait-for-exit helper, not a
scheduler. There is no behavior change to implement; the verdict
is `ADOPT`.

Verdict: ADOPT

## Decision summary

| Concept | Verdict | Code lands? | Trigger for DEFER / Reason for ADOPT/ADAPT/REJECT |
|---------|---------|-------------|------------------------------------------------|
| Parallel independent reads | DEFER | No | Trigger: real workload bottleneck + protocol_version bump + locking subsystem in hand; without all three, ADAPT ranks below determinism on SCOPE §32's priority order |
| Parallel searches | DEFER | No | Same trigger as Parallel independent reads |
| Per-path locking | ADAPT | No (ADR-only) | SCOPE §34 explicit directive: "Document the extension point instead. Do not implement a complex locking subsystem before parallel execution exists." The table is the documentation; the kernel's three existing mutex primitives (registry, emitter, session-writer) are NOT per-path locks |
| Tool-call scheduling | ADOPT | No (already implemented) | `internal/loop/loop.go:120-194` documents Run as a non-goroutine-spawning sequencer; `RunOne` is the unit of work; `RunAgent` dispatches tool calls in a single `for idx` loop; the only goroutine in the kernel (`internal/tools/builtins/shell.go:314`) is shell-tool-internal `cmd.Wait()` helper, NOT parallel-tool scheduling |

**Conditional handoff 054 verdict.** None of the four verdicts
demands landed code. The `Per-path locking` ADAPT is
documentation-only per SCOPE §34's explicit directive. The
`Tool-call scheduling` ADOPT requires no change. The two `DEFER`
verdicts reserve implementation behind named trigger conditions.
The conditional handoff 054 ("named extension-point interfaces and
invariant tests") does NOT fire from this ADR.

## Consequences

What this ADR commits Simple Harness to (V1):

- Sequential per-turn tool-call dispatch (no parallelism
  enabled). The `Run` struct's "no goroutine" discipline is
  the load-bearing invariant; future parallel-execution work
  must coexist with that invariant or carry a
  `protocol_version` bump (SCOPE §42 + the `protocol_version`
  field's documented semantics in `docs/HARNESS-CONTRACT.md`).
- A documented per-path locking extension point (SCOPE §34's
  read/read-parallel, read/write-synchronize, write/write-
  serialize table). Future parallel-tool-execution work
  implements this table in code; the table is the contract;
  V1 stays sequential.
- A reserved ADOPT path for `Tool-call scheduling`'s current
  sequential behavior: any future change that introduces
  parallelism must ground in a benchmark + a
  `protocol_version` bump + the SCOPE §34 table in code.

What this ADR commits Simple Harness out of (V1):

- Speculative parallel-tool-execution that ranks performance
  above correctness/observability/determinism in SCOPE §32's
  priority order. A `DEFER` with a crisp trigger is the
  well-formed answer to a present-day demand that does not yet
  exist.
- Speculative per-path locking implementation before parallel
  execution exists (SCOPE §34 explicit directive). The table
  in this ADR is the V1 deliverable; the implementation lives
  in the Run that has the demand.
- Speculative future reservation of ADOPT/ADAPT semantics for
  any concurrency concept not in the four this Run reviewed.
  Future concurrency reviews reopen this ADR or write an
  ADR-003.

**Reversibility.** ADOPT verdicts can be revised in a future
Run with a new ADR (e.g. ADR-003) that grounds the change in
benchmark evidence + a `protocol_version` bump. ADAPT verdicts
move from documentation to implementation in the Run that has
the demand + the SCOPE §34 subsystem in code. DEFER verdicts
flip to ADAPT/ADOPT when the trigger conditions fire. REJECT
verdicts (none in this ADR) would require a new ADR to undo.

## References

- **Pin:** `simple-harness 190e0b5538093443d4906a6e0def4648b5b3ea90`
  — the binary behavior this ADR reasons about.
- **GOAL reference:** SCOPE §§32–34, Candidate 015.
- **Kernel code cited (file:line):**
  - `internal/loop/loop.go:120-194` (Run struct + unit-of-work)
  - `internal/loop/loop.go:158-159, 189, 222` (interactive REPL's
    single-goroutine discipline)
  - `internal/loop/loop.go:383` (ComposeMessages is pure)
  - `internal/loop/loop.go:514-754` (RunAgent sequential dispatch)
  - `internal/tools/registry.go:18, 30-38, 42-47, 51-60, 86-111`
    (registry mutex + dispatch pipeline)
  - `internal/event/event.go:93, 119-132` (emit-mutex)
  - `internal/session/writer.go:13-16, 25-26, 74-86, 94-129`
    (session-writer mutex, documented as defence-in-depth)
  - `internal/tools/builtins/shell.go:314` (the only goroutine
    in the kernel; `cmd.Wait()` helper, not scheduler)
  - `internal/tools/builtins/{read_file,grep,search_files}.go`
    (pure-function Execute methods)
- **Whip evidence (observational only):** `strings
  /home/svend/.local/bin/whip` extracts (`lock`, `Lock`,
  `Locked`, `/schedul`, `lockSlow`, `lockAddr`,
  `lockRankStruct`, `runtime.goroutineProfileState`,
  `runtime.heldLockInfo`, scheduler symbols — Go
  runtime/stdlib concurrency indicators). Documented in
  `docs/comparative/whip/{version,startup-timing,bench-output}.txt`
  + the binary's stripped-ELF status per `docs/RECON.md`.
  No source-citation; OD-1 has not closed.
- **Harness-contract anchor:** `docs/HARNESS-CONTRACT.md`
  `protocol_version` field — the version-bump seam for any
  future parallel-execution event-protocol change.
