# Criterion 30 — live closure evidence (HONEST FAIL — post wire-shape + Message+loop fix)

**Date (UTC):** 2026-08-30T12:50:50Z
**Run:** 023 (handoff 076)
**Model:** MiniMax-M3
**Base URL:** https://api.minimax.io/v1
**Harness commit measured:** 9d487766157d6c81c00f96c22991d154393ac9eb
**Repo:** /home/svend/simple-harness
**Working tree:** clean at session start (9d48776)
**Outcome:** FAILED — live e2e acceptance did not produce a PASS line, even with BOTH the handoff 073 wire-shape fix AND the handoff 075 Message+loop fix in the rebuilt runtime binary

> **Honest disclosure:** this evidence file documents a FAILED live
> acceptance, not a PASS. The TG3 criterion-30 closure requires a
> PASS line from `scripts/e2e-coding.sh`; none was produced across
> 9 attempts (3 invocations × 3 internal attempts) using the
> supervisor-corrected command shape, even with BOTH the handoff
> 073 wire-shape fix (`Tools []ToolDefinition` + `ToolChoice any`
> fields populated at loop.go:302+645 from the registered tool
> registry) AND the handoff 075 Message+loop fix (`ToolCalls
> []ToolCall` + `ToolCallID string` on `model.Message`,
> `ToolCall.Type string`, the marshal-layer conversion at
> `ChatStream`'s outbound marshal in `internal/model/client.go`,
> the assistant-with-tool_calls message append at loop.go:784,
> the correlated tool-result append at loop.go:850) in the
> rebuilt runtime binary at `bin/simple-harness-runtime` (rebuilt
> at this handoff's Sub-deliverable A after the Version literal
> advance at Sub-deliverable E). **TG3 honest-FAILS at this
> handoff.** The implementer has escalated per GOAL §2 + Sub-
> deliverable C at
> `/home/svend/flows/1010/escalations/076-honest-fail.md` with
> the JSONL preserved at
> `/home/svend/flows/1010/results/076-e2e-jsonl-preserve/` for
> diagnosis.

## Live acceptance — scripts/e2e-coding.sh

**Command shape (verbatim from the GOAL header — the
`PATH="$PWD/bin:$PATH"` prefix picks up the rebuilt
`bin/simple-harness-runtime` carrying the handoff 073
wire-shape surface + the handoff 075 Message+loop surface +
this handoff's Version literal advance `(Run 023, handoff 076)`):**

```bash
cd /home/svend/simple-harness && \
  PATH="$PWD/bin:$PATH" \
  SIMPLE_HARNESS_API_KEY="$MINIMAX_API_KEY" \
  ./scripts/e2e-coding.sh https://api.minimax.io/v1 MiniMax-M3 \
  > /tmp/e2e-coding-076-r{N}.stdout 2> /tmp/e2e-coding-076-r{N}.stderr
```

**Total attempts: 9 (3 invocations × 3 internal attempts). All 9 FAILED. 0/9 attempts passed.**

| Invocation | run_exit (harness) | All 3 internal attempts                  | stdout file                     | stderr file                     |
|------------|--------------------|------------------------------------------|---------------------------------|---------------------------------|
| 1          | 1                  | FAIL (A) — calculator.py did not change  | /tmp/e2e-coding-076-r1.stdout   | /tmp/e2e-coding-076-r1.stderr   |
| 2          | 1                  | FAIL (A) — calculator.py did not change  | /tmp/e2e-coding-076-r2.stdout   | /tmp/e2e-coding-076-r2.stderr   |
| 3          | 1                  | FAIL (A) — calculator.py did not change  | /tmp/e2e-coding-076-r3.stdout   | /tmp/e2e-coding-076-r3.stderr   |

**Verbatim stderr pattern (all 9 attempts, identical across the 3 invocations):**

```
attempt 1: FAIL — (A) calculator.py did not change (run_exit=1)
attempt 2: FAIL — (A) calculator.py did not change (run_exit=1)
attempt 3: FAIL — (A) calculator.py did not change (run_exit=1)
0/3 attempts passed; FAILED criterion per GOAL §2
```

**Exit code (script):** 1 (per invocation; the script returns 1 when 0/3 attempts pass).

**Session id (quoted):** N/A — no PASS attempt produced a session id; the script
exits before extracting `SESSION_ID` from the JSONL `started` event
because assertion (A) fails first (the diff check fires before the
JSONL extraction). However, three session ids were captured by the
parallel-watcher infrastructure during the harness's `started` events
in invocation 3 (preserved at
`/home/svend/flows/1010/results/076-e2e-jsonl-preserve/r3/run.{1,2,3}.jsonl`):

- attempt 1: `01a052b8-1e8e-7fb5-9b24-5829d3091422`
- attempt 2: `01a052b8-2855-7119-a130-46d5ab9eb0c1`
- attempt 3: `01a052b8-2fac-72e2-b560-26d7090043e7`

These session ids are NOT a TG3 closure — the assertion (A) check
fired first because the workspace's `calculator.py` was byte-
identical to the pristine fixture. The session ids prove the
harness started each attempt and emitted the `started` event;
they do not prove the model invoked tools through the
documented mechanism.

## Three assertion outcomes (NONE PASS)

- **(A) diff assertion (workspace file must differ from
  pristine):** FAIL —
  `diff -q example-project/calculator.py <workspace>/calculator.py` exited 0
  (files are IDENTICAL — the model did not patch the `a - b` defect).
- **(B) pytest assertion (post-patch tests pass):** NOT REACHED — assertion
  (A) failed first; pytest was never run.
- **(C) tool_call/tool_result events:** NOT REACHED — assertion (A) failed
  first; the JSONL was never inspected for `tool_call` / `tool_result`.

**Behavioral difference from handoff 074:** in handoff 074 the
harness returned `run_exit=0` (zero, with the workspace file
unchanged — a clean exit that simply did not patch the file).
In this handoff the harness returns `run_exit=1` (the JSONL
`completed` event records `exit_code: 1` after a `status:
FAILED` event, with the streamed assistant content truncated
mid-stream). The model started streaming a <think> block and
the stream abruptly ended; the harness classified this as a
generic failure (exit code 1 per SCOPE §28 — NOT a model/API
failure which would be exit code 3). This is a measurable
behavior change between handoff 074 and handoff 076 — see the
**direct-invocation** preservation in the JSONL preservation
path below for the full 7-event JSONL trail.

## History note — pre-fix vs. post-fix measurements

Runs 013/018/022 measured the PRE-FIX behavior: `model.ChatRequest`
carried only `Messages`, so no live model was ever offered the
harness's tools (the comment at `internal/model/client.go:54-57`
"when handoff 010+ lands them" was never fulfilled). The 9/9
live e2e failure in those runs is consistent with a harness wiring
gap, not (only) model behavior. Run 023 closes the gap across
two handoff surface fixes:

- Handoff 073 landed the wire-shape fix (`Tools []ToolDefinition` +
  `ToolChoice any` fields populated at loop.go:302+645 from the
  configured `tools.Registry`).
- Handoff 075 landed the Message+loop fix (`ToolCalls []ToolCall` +
  `ToolCallID string` on `model.Message`, `Type string` on
  `model.ToolCall`, the marshal-layer conversion at `ChatStream`'s
  outbound marshal in `internal/model/client.go`, the assistant-
  with-tool_calls message append at loop.go:784, the correlated
  tool-result append at loop.go:850).

The Run 023 progression:

- Handoff 074 measured the wire-shape-only attempt (binary built
  at `273a80c`, before handoff 075): honest-FAIL on 0/9 attempts,
  with `run_exit=0` (clean exit, no patch).
- Handoff 076 (this handoff) measures the wire-shape-+Message+
  loop attempt (binary rebuilt at `9d48776`+local after
  Sub-deliverable A): honest-FAIL on 0/9 attempts, with
  `run_exit=1` (the harness marks `status: FAILED` after a
  truncated SSE stream — the model started streaming a <think>
  block, then the stream abruptly ended without producing a
  tool call or a complete assistant message).

The failure mode is observably different from the wire-shape-
only attempt at handoff 074 (where the harness returned
`run_exit=0` and the model returned a non-tool-invoking
response). The Message+loop fix at handoff 075 changed the
harness's marshaling + the loop's tool-result correlation;
the live model now sees the OpenAI-spec assistant-with-
tool_calls + correlated tool-result messages on subsequent
turns. But the FIRST turn's stream is truncated mid-<think>
in this handoff's measurement — the harness did not even
reach the loop iteration where the assistant-with-tool_calls
message append + the correlated tool-result append fire.

The escalation question at
`/home/svend/flows/1010/escalations/076-honest-fail.md` lists
the four possible causes the planning supervisor / Human
needs to investigate:

1. SSE stream truncation (the model's SSE stream ends mid-
   <think> block; the harness's `internal/loop/loop.go` or
   `internal/model/client.go` parses the truncated stream and
   treats it as a failure with `status: FAILED` / `exit_code:
   1` — the JSONL preservation path includes a direct
   invocation that shows the truncation point)
2. Wire-shape mismatch (the harness sends `tools` but the
   endpoint may not parse it correctly — the JSONL shows
   `endpoint: https://api.minimax.io` without the `/v1`
   suffix the `--base-url` was set to, suggesting possible
   URL-stripping in the harness's endpoint configuration)
3. Tool invocation gap (model receives tools but does not
   invoke them — same model-behavior pattern as Runs 013/018)
4. Loop wiring gap (model returns tool call but harness does
   not translate the response into a workspace file change —
   but the JSONL shows the harness never received a tool call,
   so this cause is RULED OUT at this handoff)

The handoff 074 EVIDENCE file (still on disk at the `9d48776`
HEAD before this rewrite) is the HONEST-FAIL reference for the
wire-shape-only state. This handoff's EVIDENCE file is the
Run 023 close EVIDENCE on top of the wire-shape-+Message+loop
surface.

## JSONL preservation on honest-FAIL (binding — per the
supervisor's directive)

The implementer set up JSONL-preservation infrastructure
BEFORE running the script per Sub-deliverable B's bullet.
Concretely:

- Invocation 1: `WORKSPACE_DIR_OVERRIDE` was NOT properly
  exported for the script (an export-scoped `VAR=val mkdir` in
  a chained `&&` expression does not propagate `VAR` to the
  next command); the script used its own `mktemp` dir and the
  EXIT trap `rm -rf "$WORKSPACE"` removed the JSONL. **No
  JSONL preserved for invocation 1.**
- Invocation 2: same mistake (export scoping); **No JSONL
  preserved for invocation 2.**
- Invocation 3: `export WORKSPACE_DIR_OVERRIDE=...` was used
  correctly + a parallel polling watcher (every 0.5s) copied
  `run.{1,2,3}.jsonl` and `run.{1,2,3}.err` from the script's
  workspace to a stable path. **JSONL preserved for invocation
  3.**

The preserved JSONL files for invocation 3 are partial (2 events
each — `started` + `model_request`) because the watcher copied
the files early before the streaming completed; the script
then overwrote `run.$attempt.jsonl` at the start of each
iteration before the next iteration's events could land. **The
watcher caught the START of each attempt but the harness's
streaming events came AFTER the watcher copied the file.**

To supplement the partial script-preserved JSONL, the
implementer invoked `simple-harness run` DIRECTLY (bypassing
the script) twice with `--workspace` pointing to a stable path
that does not get cleaned up by the script's EXIT trap. The
direct invocations produced FULL 7-event JSONL files preserved
at `/home/svend/flows/1010/results/076-e2e-jsonl-preserve/`:

- `direct-invocation.jsonl` (1402 bytes, 7 events) — the
  harness starts, sends `model_request`, gets a streaming
  response with a partial <think> block, marks `status:
  FAILED` and `completed(exit_code: 1)`.
- `direct-invocation-2.jsonl` (similar 7 events) — identical
  pattern: 4 assistant_stream deltas of a <think> block,
  `status: FAILED`, `completed(exit_code: 1)`. The stream
  ends immediately after the `</think>\n\n` token.

The direct-invocation JSONLs are the **primary diagnostic
evidence** for this honest-FAIL escalation. The script's
`run.attempt N.jsonl` from invocation 3 is the secondary
evidence (proving the harness started 3 attempts in
invocation 3 and recording the `started` event with session
ids).

## Secrets discipline

The MiniMax API key was supplied to `scripts/e2e-coding.sh` via
the `SIMPLE_HARNESS_API_KEY` env var for each of the 3
invocations, and to `simple-harness run` for the 2 direct
invocations. The key was read from the login env var
`$MINIMAX_API_KEY` (length=125 at session start, verified NOT
the 22-char opencode config template) per GOAL §2 amendment 2.

**The key value NEVER appears in this evidence file, the
result file, the ledger, the JSONL preservation path, or any
commit.** The implementer used `$MINIMAX_API_KEY` (env var
reference) + `SIMPLE_HARNESS_API_KEY="$MINIMAX_API_KEY"` (env
var export) — no key value was ever typed, echoed, grepped, or
pasted. `unset SIMPLE_HARNESS_API_KEY ANTHROPIC_API_KEY` was
invoked immediately after each invocation. ANTHROPIC_*
variables stayed UNSET throughout (no `ANTHROPIC_*` variable
was set at any point during this handoff).

## TG3 status

**TG3 does NOT close at this handoff.** Criterion 30 requires
a PASS line from the live e2e acceptance; no PASS line was
produced across 9 attempts (3 invocations × 3 internal
attempts) using the supervisor-corrected command shape, even
with BOTH the handoff 073 wire-shape fix AND the handoff 075
Message+loop fix in the rebuilt runtime binary. The harness-
side wire-shape verification (TG1 + TG2 from handoff 073) is
intact: `Tools` + `ToolChoice` fields are populated from the
registry at the call site, and the 3 `TestChatRequest_Tools`
pins PASS under `-race`. The Message+loop surface (handoff
075) is intact: the marshal-layer conversion + the
assistant-with-tool_calls message append + the correlated
tool-result append are committed at `9d48776` with the 2
`TestToolLoop_Messages_*` pins + the 1
`TestMessage_PlainOmitsToolFields` pin passing.

The 9/9 live e2e failure on top of BOTH the wire-shape +
Message+loop surface is a real defect — NOT a weakened
assertion. The escalation question at
`/home/svend/flows/1010/escalations/076-honest-fail.md` lists
the four possible causes (SSE stream truncation, wire-shape
URL-stripping, tool invocation gap, loop wiring gap — the
last RULED OUT by the JSONL preservation showing the harness
never received a tool call). The next Run's GOAL will need to
enumerate which cause is the binding one and open the
appropriate fence for the cure.

---

*This evidence file was written by the implementer (handoff
076) with honest disclosure of the FAIL outcome. The binding
PASS-format from the handoff (which requires an `attempt N:
PASS — session_id=<id>` line) does not apply because no PASS
occurred. The file format follows the spirit of the handoff's
evidence template (date, run, model, base URL, harness commit,
repo, secrets discipline) while documenting the FAIL outcome
rather than a fabricated PASS. The file REPLACES the handoff
074 EVIDENCE file at `9d48776` HEAD.*