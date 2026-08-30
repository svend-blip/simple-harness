# Criterion 30 — live closure evidence (HONEST FAIL — post wire-shape fix)

**Date (UTC):** 2026-08-30T12:02:13Z
**Run:** 023 (handoff 074)
**Model:** MiniMax-M3
**Base URL:** https://api.minimax.io/v1
**Harness commit measured:** a33557c5c586e2f05450d6807a63ed060a7a3255
**Repo:** /home/svend/simple-harness
**Working tree:** clean at session start (a33557c)
**Outcome:** FAILED — live e2e acceptance did not produce a PASS line, even with the handoff 073 wire-shape fix in the rebuilt runtime binary

> **Honest disclosure:** this evidence file documents a FAILED live
> acceptance, not a PASS. The TG3 criterion-30 closure requires a PASS
> line from `scripts/e2e-coding.sh`; none was produced across 9 attempts
> (3 invocations × 3 internal attempts) using the supervisor-corrected
> command shape, even with the handoff 073 tools-in-request fix (the
> ChatRequest now carries `Tools` + `ToolChoice` fields populated from
> the registered tool registry) and the runtime binary rebuilt from
> source at this handoff. **TG3 honest-FAILS at this handoff.** The
> implementer has escalated per GOAL §2 + Sub-deliverable B at
> `/home/svend/flows/1010/escalations/074-honest-fail.md`.

## Live acceptance — scripts/e2e-coding.sh

**Command shape (verbatim from the GOAL header — the handoff's
PATH-propagation erratum is honored; the
`PATH="$PWD/bin:$PATH"` prefix picks up the rebuilt
`bin/simple-harness-runtime` carrying the handoff 073 wire-shape surface
+ this handoff's Version literal advance):**

```bash
cd /home/svend/simple-harness && \
  PATH="$PWD/bin:$PATH" \
  SIMPLE_HARNESS_API_KEY="$MINIMAX_API_KEY" \
  ./scripts/e2e-coding.sh https://api.minimax.io/v1 MiniMax-M3 \
  > /tmp/e2e-coding-074-r{N}.stdout 2> /tmp/e2e-coding-074-r{N}.stderr
```

**Total attempts: 9 (3 invocations × 3 internal attempts). All 9 FAILED.**

| Invocation | run_exit (harness) | All 3 internal attempts | stdout file                     | stderr file                     |
|------------|-------------------|------------------------|---------------------------------|---------------------------------|
| 1          | 0                 | FAIL (A)               | /tmp/e2e-coding-074-r1.stdout   | /tmp/e2e-coding-074-r1.stderr   |
| 2          | 0                 | FAIL (A)               | /tmp/e2e-coding-074-r2.stdout   | /tmp/e2e-coding-074-r2.stderr   |
| 3          | 0                 | FAIL (A)               | /tmp/e2e-coding-074-r3.stdout   | /tmp/e2e-coding-074-r3.stderr   |

**Verbatim stderr pattern (all 9 attempts, identical across the 3 invocations):**

```
attempt 1: FAIL — (A) calculator.py did not change (run_exit=0)
attempt 2: FAIL — (A) calculator.py did not change (run_exit=0)
attempt 3: FAIL — (A) calculator.py did not change (run_exit=0)
0/3 attempts passed; FAILED criterion per GOAL §2
```

**Exit code (script):** 1 (per invocation; the script returns 1 when 0/3 attempts pass).

**Session id (quoted):** N/A — no PASS attempt produced a session id; the script
exits before extracting `SESSION_ID` from the JSONL `started` event
because assertion (A) fails first (the diff check fires before the
JSONL extraction).

## Three assertion outcomes (NONE PASS)

- **(A) diff assertion (workspace file must differ from
  pristine):** FAIL —
  `diff -q example-project/calculator.py <workspace>/calculator.py` exited 0
  (files are IDENTICAL — the model did not patch the `a - b` defect).
- **(B) pytest assertion (post-patch tests pass):** NOT REACHED — assertion
  (A) failed first; pytest was never run.
- **(C) tool_call/tool_result events:** NOT REACHED — assertion (A) failed
  first; the JSONL was never inspected for `tool_call` / `tool_result`.

## History note — pre-fix vs. post-fix measurements

Runs 013/018/022 measured the PRE-FIX behavior: `model.ChatRequest`
carried only `Messages`, so no live model was ever offered the
harness's tools (the comment at `internal/model/client.go:54-57`
"when handoff 010+ lands them" was never fulfilled). The 9/9
live e2e failure in those runs is consistent with a harness wiring
gap, not (only) model behavior.

Run 023 handoff 073 landed the wire-shape fix (Tools + ToolChoice
fields populated from the configured `tools.Registry`); this handoff
(074) measures the POST-FIX outcome on top of that fix. The POST-FIX
measurement is ALSO a 9/9 FAIL — the failure mode is identical to
the pre-fix state (assertion (A), files identical), so the wire-shape
fix alone is not sufficient to close criterion 30 with this model.
The escalation question at
`/home/svend/flows/1010/escalations/074-honest-fail.md` lists the
three possible causes the planning supervisor / Human needs to
investigate:

1. Wire-shape mismatch (the harness sends `tools` but the endpoint
   may not parse it correctly)
2. Tool invocation gap (model receives tools but does not invoke
   them — same model-behavior pattern as Runs 013/018)
3. Loop wiring gap (model returns tool call but harness does not
   translate the response into a workspace file change)

The handoff 072 EVIDENCE file (still on disk at the `a33557c` HEAD
before this rewrite) is the HONEST-FAIL reference for the pre-fix state.

## Secrets discipline

The MiniMax API key was supplied to `scripts/e2e-coding.sh` via the
`SIMPLE_HARNESS_API_KEY` env var for each of the 3 invocations. The
key was read from the login env var `$MINIMAX_API_KEY` (length=125 at
session start, verified NOT the 22-char opencode config template)
per GOAL §2 amendment 2.

**The key value NEVER appears in this evidence file, the result file,
the ledger, or any commit.** The implementer used `$MINIMAX_API_KEY`
(env var reference) + `SIMPLE_HARNESS_API_KEY="$MINIMAX_API_KEY"`
(env var export) — no key value was ever typed, echoed, grepped, or
pasted. `unset SIMPLE_HARNESS_API_KEY ANTHROPIC_API_KEY` was invoked
immediately after each invocation. ANTHROPIC_* variables stayed UNSET
throughout (no `ANTHROPIC_*` variable was set at any point during
this handoff).

## TG3 status

**TG3 does NOT close at this handoff.** Criterion 30 requires a
PASS line from the live e2e acceptance; no PASS line was produced
across 9 attempts (3 invocations × 3 internal attempts) using the
supervisor-corrected command shape, even with the handoff 073
wire-shape fix in the rebuilt runtime binary. The harness-side
wire-shape verification (TG1 + TG2 from handoff 073) is intact:
`Tools` + `ToolChoice` fields are populated from the registry at the
call site, and the 3 `TestChatRequest_Tools` pins PASS under `-race`.

The 9/9 live e2e failure on top of the wire-shape fix is a real
defect — NOT a weakened assertion. The escalation question at
`/home/svend/flows/1010/escalations/074-honest-fail.md` lists the
three possible causes for the planning supervisor / Human to
investigate (wire-shape mismatch, tool invocation gap, or loop
wiring gap). The next Run's GOAL will need to enumerate which cause
is the binding one and open the appropriate fence for the cure.

---

*This evidence file was written by the implementer (handoff 074) with
honest disclosure of the FAIL outcome. The binding PASS-format from
the handoff (which requires an `attempt N: PASS — session_id=<id>`
line) does not apply because no PASS occurred. The file format
follows the spirit of the handoff's evidence template (date, run,
model, base URL, harness commit, repo, secrets discipline) while
documenting the FAIL outcome rather than a fabricated PASS. The
file REPLACES the handoff 072 EVIDENCE file at `a33557c`.*
