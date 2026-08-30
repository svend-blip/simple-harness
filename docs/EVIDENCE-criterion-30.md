# Criterion 30 — live closure evidence (HONEST FAIL)

**Date (UTC):** 2026-08-30T12:42:00Z
**Run:** 022 (handoff 072)
**Model:** MiniMax-M3
**Base URL:** https://api.minimax.io/v1
**Harness commit measured:** 6fb6011356f2c54359c47448086411a148e3cfcb
**Repo:** /home/svend/simple-harness
**Working tree:** clean at session start (6fb6011)
**Outcome:** FAILED — live e2e acceptance did not produce a PASS line

> **Honest disclosure:** this evidence file documents a FAILED live
> acceptance, not a PASS. The TG2 criterion-30 closure requires a PASS
> line from `scripts/e2e-coding.sh`; none was produced across 9 attempts
> (3 invocations × 3 internal attempts) using the supervisor-corrected
> command shape. **TG2 does NOT close at this handoff.**

## Live acceptance — scripts/e2e-coding.sh

**Command shape (supervisor-corrected per the erratum in runs/022/GOAL.md — the handoff's original `PATH="..." : "..." && ./script.sh` shape did not propagate PATH to the subprocess):**

```bash
cd /home/svend/simple-harness && \
  PATH="$PWD/bin:$PATH" \
  SIMPLE_HARNESS_API_KEY="$MINIMAX_API_KEY" \
  ./scripts/e2e-coding.sh https://api.minimax.io/v1 MiniMax-M3 \
  > /tmp/e2e-coding-072-r{N}.stdout 2> /tmp/e2e-coding-072-r{N}.stderr
```

**Total attempts: 9 (3 invocations × 3 internal attempts). All 9 FAILED.**

| Invocation | run_exit (harness) | All 3 internal attempts | stderr file |
|------------|-------------------|------------------------|-------------|
| 1 | 0 | FAIL (A) calculator.py did not change | /tmp/e2e-coding-072-r1.stderr |
| 2 | 0 | FAIL (A) calculator.py did not change | /tmp/e2e-coding-072-r2.stderr |
| 3 | 0 | FAIL (A) calculator.py did not change | /tmp/e2e-coding-072-r3.stderr |

**Verbatim stderr pattern (all 9 attempts):**

```
attempt 1: FAIL — (A) calculator.py did not change (run_exit=0)
attempt 2: FAIL — (A) calculator.py did not change (run_exit=0)
attempt 3: FAIL — (A) calculator.py did not change (run_exit=0)
0/3 attempts passed; FAILED criterion per GOAL §2
```

**Exit code (script):** 1 (the script returns 1 when 0/3 attempts pass)

**Session id (quoted):** N/A — no PASS attempt produced a session id; the script exits before extracting `SESSION_ID` from the JSONL `started` event because assertion (A) fails first (the diff check fires before the JSONL extraction).

## Three assertion outcomes (NONE PASS)

- **(A) diff assertion (workspace file must differ from pristine):** FAIL —
  `diff -q example-project/calculator.py <workspace>/calculator.py` exited 0
  (files are IDENTICAL — the model did not patch the `a - b` defect).
- **(B) pytest assertion (post-patch tests pass):** NOT REACHED — assertion
  (A) failed first; pytest was never run.
- **(C) tool_call/tool_result events:** NOT REACHED — assertion (A) failed
  first; the JSONL was never inspected for `tool_call` / `tool_result`.

## Root cause analysis (harness-level, measured)

The harness never advertises its tool inventory to any model.
`internal/model`'s `ChatRequest` (`internal/model/client.go:58-60`)
carries only `Messages []Message` — no `tools` field, no
`tool_choice` field. The only `json:"tools"` in the codebase are
MCP wire-protocol types (`internal/mcp/transport_stdio.go:121` +
`internal/mcp/transport_http.go:149`), not the outgoing
chat-completions request. `loop.HarnessSystem`
(`internal/loop/loop.go:52`) is a generic identity string that never
enumerates tool names or schemas. Consequently no model, regardless
of capability, can invoke tools through the documented mechanism —
the 9/9 live e2e failure is consistent with a harness wiring gap,
not (only) model behavior. Criterion 30 is unpassable by ANY model
until a tools-in-request fix lands in a follow-up run; runs 018/013
misattributed the same gap to model behavior.

The harness is unchanged from commit `6fb6011` at this handoff's
session end; no Go source is touched in this Run. The tools-in-
request fix is a NEW run (Run 023) — `internal/model/` + loop
wiring + live re-proof with a tool-capable model.

## Portability fix (TG1 — closed at handoff 071)

`scripts/contract-check.sh` derives `SIMPLE_HARNESS_BIN` default from
the script's own location
(`$(cd "$(dirname "$0")/.." && pwd)/bin/simple-harness`); the literal
`/home/svend/...` is gone. `PASS=4 FAIL=0 SKIP=1` is unchanged.
Committed at `6fb6011` (handoff 071 Git-gate close).

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
pasted. ANTHROPIC_* variables stayed UNSET throughout (no `ANTHROPIC_*`
variable was set at any point during this handoff).

## TG2 status

**TG2 does NOT close at this handoff.** Criterion 30 requires a
PASS line from the live e2e acceptance; no PASS line was produced
across 9 attempts (3 invocations × 3 internal attempts) using the
supervisor-corrected command shape. The harness-level root cause
is documented in the "Root cause analysis" section above: the
harness omits `tools` from the outgoing chat-completions request,
so no model can invoke tools through the documented mechanism.
The tools-in-request fix is deferred to Run 023 (a new run that
opens `internal/model/` + `internal/loop/` for the fix + live
re-proof with a tool-capable model).

---

*This evidence file was written by the implementer (handoff 072) with
honest disclosure of the FAIL outcome. The binding PASS-format from
the handoff (which requires an `attempt N: PASS — session_id=<id>`
line) does not apply because no PASS occurred. The file format
follows the spirit of the handoff's evidence template (date, run,
model, base URL, harness commit, repo, secrets discipline) while
documenting the FAIL outcome rather than a fabricated PASS.*
