# Bounded Context Lifecycle

Simple Harness keeps the context it sends to the model inside a budget derived
from the model's own context limit, instead of letting conversation, tool
calls and tool results grow until the runtime refuses the request.

The distinction the feature rests on:

```
durable execution history   !=   active model context
```

Nothing here deletes anything. The harness accumulates its history as it
always did, and the session log persists what it always persisted. What
changed is that the list sent on the wire is a bounded *view* of that history,
decided fresh before every inference.

## What it does, in order

Before each model call:

1. Account for the complete candidate context — messages, tool calls, tool
   results, and the tool schema surface, which the message list does not carry.
2. Compare it against the safe budget.
3. If it fits, send it unchanged. Nothing is reduced that does not need to be.
4. Otherwise replace older tool results with placeholders, oldest first,
   stopping as soon as it fits.
5. Otherwise compact older conversation into a working summary — a pinned
   user-role message headed `COMPACTED WORKING HISTORY` (not a system
   message: a system message after the task is rejected by runtimes that
   apply the model's chat template). A summary is never compacted again.
6. Otherwise narrow the recent verbatim window, halving it down to a floor
   of two messages and pruning at each step, when the alternative is
   failing a run that could continue.
7. Otherwise cut any single tool result larger than a quarter of the
   budget to an excerpt — its head and tail around a marker naming how
   much was cut and that the full result is in the session history. A
   15k-token file read into an 11k-token budget sits in the window's
   floor where nothing above can touch it; the model reads it again in
   ranges instead of the run ending.
8. Otherwise fail explicitly, naming what could not be reduced.

Steps 4 to 7 never touch pinned context. The view fitted for one call is
carried forward to the next, so a reduction is made once, not redone every
turn.

## The budget

```
model context limit
- generation reserve      room for the model's own output
- safety reserve          margin for estimation error
= active input budget
```

Estimation is approximate — four characters to a token, the same estimator
`context show` has always used — so the safety reserve is what makes acting on
an approximate figure safe. Below a 64k window the fixed reserves would
swallow most of it, so they scale instead; without that an 8k model would be
left no input budget at all.

**An unknown limit is not guessed at.** With no limit configured the harness
accounts and reports but does not bound, and says so in the report. Guessing a
limit would be worse than the unbounded behaviour that preceded this feature.

## Retention priority

| Priority | What | What may happen to it |
|---|---|---|
| **Pinned** | System and governance instructions, loaded skills, the current task | Nothing. If pinned context alone exceeds the budget the run fails and says so. |
| **Recent** | The trailing verbatim window, default 8 messages | Nothing, unless the window itself is the problem |
| **Reducible** | Older conversation and tool results | Pruned, then compacted |

A tool call and the tool result answering it always share a priority. The
recent window is a count of messages, so its boundary can fall between them —
and a reduction that removed one and left the other would produce a request
every OpenAI-compatible endpoint rejects with a 400, mid-run.

## Configuration

Safe behaviour needs no configuration. A config file that says nothing about
context gets a bounded one.

```json
{
  "context": {
    "policy": "bounded",
    "model_limit": 131072,
    "generation_reserve": 16384,
    "safety_reserve": 4096,
    "keep_recent_turns": 8,
    "tool_result_pruning": true,
    "compaction": true
  }
}
```

`policy` is `bounded` (default) or `unbounded` (turns the feature off);
`model_limit` is the model's window (0 or absent = ask the runtime, see
below); the reserves are derived when absent; `keep_recent_turns`
defaults to 8; the booleans default to true. `SIMPLE_HARNESS_CONTEXT_POLICY`,
`SIMPLE_HARNESS_CONTEXT_MODEL_LIMIT` and `SIMPLE_HARNESS_CONTEXT_PROBE_LIMIT`
override from the environment.

## Where the limit comes from

The model context limit is resolved, in order:

1. `--context-limit <n>` on the command line — a deliberate act for this
   run; nothing else is consulted.
2. The runtime, unless `probe_limit` is false: the harness asks what
   window the model is actually served with — `max_model_len` from
   `/v1/models` (vLLM, SGLang), `n_ctx` from `/props` (llama.cpp), or
   `num_ctx` from `/api/show` (Ollama). It deliberately ignores
   architecture maxima such as Ollama's `context_length`, which for a
   model served at 65 536 says 262 144: a limit above the served window
   would make the budget unsafe. Each request is bounded by three
   seconds; a runtime that reports nothing yields "unknown".
3. `context.model_limit` from configuration. When both the runtime and
   the configuration report a figure, the smaller is used and the
   report names both.

The result is announced once per run as a `CONTEXT_LIMIT: <n> (<source>)`
status event (`CONTEXT_LIMIT: unknown (unbounded)` when nothing is known),
and `context show` prints it as `Limit source:`.

Turning it off has to be asked for by name: any other value of `policy`,
including a misspelling, leaves the safe behaviour in place.

On the command line, `--context-limit <n>` sets the model's window for one
run. It is **not** `--limit`, which is the older accounting check ("tell me if
the composition exceeds n", verified after the call, exit 2). One is a budget
planned against before the call; the other is a check applied after it.

## Observability

`simple-harness context show --context-limit <n>` prints the budget and the
lifecycle counters beneath the accounting report it always printed:

```
Model context limit:            131072
Generation reserve:              16384
Safety reserve:                   4096
Active input budget:            110592

Pinned:                           5210
Recent verbatim:                 27100
Reducible:                       19400
Compacted history:                2210
Tool schemas:                     8870
--------------------------------------
Active context:                  60580
Budget utilization:              54.8%

Tool results pruned:                12
Tokens pruned:                   28400
Compactions:                         2
Peak active context:             98330
```

During a run the JSONL sidecar carries `CONTEXT_REDUCED` when a reduction
happens, with what it did, and `CONTEXT_BUDGET_EXCEEDED` when one cannot be
made to fit. A compaction is an inference: it is announced by a
`COMPACTING` status and its own `model_request` event, and its `usage`
is emitted like any other request's, so a measurement counting model
calls counts it.

## What it is not

It is not memory. There is no semantic retrieval, no vector store, no
embedding index, and a compaction summary is lossy working memory rather than
a record of anything.

It is not project state. Where `scope-mcp` is connected it remains the
authoritative source for scope, goals, decisions and status, reachable as an
ordinary MCP tool. A compaction summary must never become the only record of
what a project has done, and the compaction instruction says so to the model
as well as to the reader.

It is not orchestration. FlowRunner still owns workflow execution and resume
state; FlowApps running through Simple Harness get this feature without
implementing anything, because it lives below them in the harness itself.

## Where it lives

`internal/ctxlife` owns the decision and nothing else: budget, priority,
accounting, pruning, compaction, reporting. It holds no history of its own —
each call takes the caller's candidate list and returns a bounded view of it,
which is why the caller's history cannot be damaged by it.

`RunAgent` remains responsible for running the agent. It asks the manager what
to send, immediately before sending it.

## Benchmarking it

`scripts/benchmark-context.sh <base-url> <model> [limit] [turns]` runs the same
long task twice against the same local model and runtime, differing only in
whether `--context-limit` is set. It reports model calls, total input tokens,
the largest single prompt, how many reductions happened, runtime and exit
code, all read out of the harness's own JSONL sidecar.

The figure that matters is the largest single prompt: with the lifecycle it
stays under the budget, and without it, it does not.

Measured 2026-09-18 against `qwen3.6-27b-64k` through Ollama (served window
65 536, RTX 5090), a 16 384-token limit, twelve turns, the task being to read
this repository one file at a time. `calls` counts every model request,
compaction inferences included; `compact` is how many of them were
compactions:

| arm | calls | compact | input tokens | largest prompt | reductions | runtime | exit |
|---|---:|---:|---:|---:|---:|---:|---:|
| bounded | 17 | 4 | 90 903 | 12 315 | 5 | 149.5 s | 1 (max turns) |
| baseline | 11 | 0 | 419 451 | **64 766** | 0 | 59.6 s | 0 |

What the numbers say:

- The bounded arm never left its limit: 12 315 is the largest prompt the
  runtime counted, against a 16 384 limit and an 11 264 active budget. The
  runtime's count exceeds the harness's estimate by about 9 % on Go source
  (the estimator is four characters per token; code tokenizes denser),
  which is what the safety and generation reserves are for — they
  absorbed it. The baseline's largest prompt was 64 766 tokens against a
  65 536-token served window: one more file and the runtime would have
  refused it. That is the failure this addendum is about.
- 78 % fewer input tokens in the bounded arm.
- Compaction overhead: four of seventeen model calls were compaction
  inferences; the bounded arm took 2.5x the baseline's wall time, part of
  it compaction, part of it the extra turns below.
- The bounded arm did not finish the task within twelve turns: with large
  files cut to excerpts it re-read them in ranges, which costs turns. The
  baseline finished in ten. Bounding trades turns for staying inside the
  window; a task that needs whole large files needs a turn budget sized
  for that.
- The first bounded run of the day failed at its second call with
  "cannot fit": one 15k-token file read into the 11k budget. That is the
  measurement that produced step 7 (oversized results are cut to
  excerpts); the table above is the run after it.

A first attempt at twenty-four turns was aborted by the benchmark host:
the GPU stopped responding partway through ("Unable to determine the
device handle for GPU0"), for the second time on this workload in two
days. After a reboot the measurement ran to completion the same evening
(2026-09-18, same model, runtime and limit; GPU telemetry alongside:
peak 566 W of a 575 W limit, 80 °C, no fault):

| arm | calls | compact | input tokens | largest prompt | reductions | runtime | exit |
|---|---:|---:|---:|---:|---:|---:|---:|
| bounded | 19 | 4 | 127 299 | 12 321 | 6 | 163.0 s | **0** |
| baseline | 10 | 0 | 378 493 | **62 526** | 0 | 56.4 s | 0 |

With a turn budget sized for it, the bounded arm finishes the task: exit
0 after fifteen working turns and four compactions, largest prompt
12 321 — the same ceiling as at twelve turns, so more turns did not
grow the context. 66 % fewer input tokens than the baseline, at 2.9x its
wall time. The baseline again ended within 3 010 tokens of the served
window. The twelve-turn exit 1 above was the turn budget, not the
lifecycle.

### DeepSeek Harness as the behavioural reference (§25, optional)

Measured 2026-09-18, the same task and the same workspace through DSH
0.1.5-rc.1, driven over dsh-bridge, and through both arms of this
benchmark — all three against the model DSH is configured for
(`Qwen3.8-Flash-Next-Abliterated-NVFP4` on FreeToken, served window
131 072), twenty-four turns:

| harness | model calls | input tokens | largest prompt | bound | reductions | runtime | task |
|---|---:|---:|---:|---|---|---:|---|
| simple-harness, bounded | 22 | 220 477 | 12 629 | 16 384 (flag) | 5 prunings, 0 compactions | 144.0 s | completed |
| simple-harness, unbounded | 16 | 765 766 | 69 901 | none | 0 | 124.3 s | completed |
| DSH | 18 | 1 066 462 | 97 838 | 131 072 (window) | 8 prunings (61 981 tokens shadowed), 0 summaries | 192.0 s | completed |

DSH writes no token counts to its session log, so its prompt sizes were
read from the runtime: FreeToken's scheduler journal logs every prefill
with its new and cached tokens (`scripts/freetoken-journal-prompts.py`).
The instrument was calibrated first against the two simple-harness arms,
whose own sidecar carries the runtime's usage: 38 calls, 986 243 input
tokens, largest prompt 69 901 — the journal reproduced all three figures
exactly. DSH's step, tool-call and pruning counts are from its own log;
its 19th request, a 239-token session-title call, is left out.

What the reference shows:

- **Same mechanism, different bound.** DSH let the prompt grow to 97 838
  tokens — 75 % of the served window — and then shadowed old tool
  results in one step, down to 35 696, and grew again. No summary
  compaction was needed in eighteen steps. That is this lifecycle's
  pruning stage, triggered relative to the model's window rather than to
  a declared limit. The bounded arm here did the same five times under a
  16 384 limit and also needed no compaction on this model.
- **The bound is what the tokens cost.** All three finished the task.
  DSH spent 4.8x the bounded arm's input tokens, the unbounded harness
  3.5x. DSH's floor is also higher: its first prompt is 16 617 tokens
  (system prompt, tool set, scope-mcp) against 3 730 here — more than
  the bounded arm's whole limit, so DSH could not have run under it.
- **Not like for like on the limit.** DSH takes its bound from the
  configured context window (131 072 in `~/.dsh/settings.yaml`), which
  was left as the operator has it. The comparison is of behaviour at each
  harness's own bound, not of two harnesses at 16 384.
- Wall time: on FreeToken the bounded arm took 1.16x the unbounded
  arm's time, with no compaction inferences; on Ollama with
  `qwen3.6-27b` (above) it took 2.9x, four of its calls being
  compactions. The next section is what that difference was.

### Why the bounded arm was 2.9x slower on Ollama

Investigated 2026-09-19. Ollama's access log for the twenty-four-turn
run holds the latency of every call: fifteen working calls of 1-11 s,
and four of 22.2, 28.7, 23.1 and 29.2 s — 103 s of the arm's 163 s, 63 %.
Those four are the compaction inferences. Without them the arm is 60 s
against the baseline's 56 s: the whole difference.

What those inferences bought was measured on FreeToken, where the same
task at a 10 240 limit reproduces them
(`scripts/benchmark-timing.py <sidecar>`, which the benchmark now prints
per arm):

| run | compactions | their time | share of wall | tokens saved | wall | task |
|---|---:|---:|---:|---:|---:|---|
| before | 2 | 28.5 s | 14 % | ~6 | 200.8 s | completed |
| before | 3 | 51.8 s | 28 % | ~8 | 186.3 s | max turns |
| before | 1 | 34.7 s | 22 % | ~122 | 156.0 s | completed |
| after | 0 | — | — | — | 115.5 s | completed |
| after | 0 | — | — | — | 101.1 s | completed |

The cause: on a file-reading task the deficit sits in large tool results
inside the recent window. Pruning has already turned everything older
into placeholders, so the reducible span is a few hundred tokens of
one-line remarks — and `Fit` compacted it regardless, because compaction
was the next stage. The compaction prompt was 460-830 tokens; the
summary that came back was 464-1 424, the instruction asking for six
headings. Most were refused as no smaller than what they replaced, and
narrowing the window, which costs nothing, then did the work.

Corrected in `internal/ctxlife`:

- Compaction is attempted only when it can pay: the reducible span, less
  the deficit, must leave room for a summary (`MinSummaryRoom`, default
  512 tokens). Otherwise the free reductions go first, and compaction is
  tried after them if the context still does not fit.
- A second defect surfaced while measuring, present before this change:
  after an oversized result was cut to an excerpt, `Fit` did not narrow
  the window again, and failed a run ("the window will not narrow below
  2") that one more free pruning would have fitted. A 10.5k-token file
  read into a 7k budget, exit 2. It narrows again now.

Not done, recommended: the compaction request carries no size target and
no output cap, so one summary ran to 1 424 tokens and 35 s.
`model.ChatRequest` has no per-request output limit; adding one is a
change to the wire contract and was left out of this correction. The
Ollama re-measurement with the correction is owed: the GPU was serving
FreeToken when this was written.

Not measured: resume/continue across invocations, which the harness does
not implement (a new session is a new composition; durable history is
persisted, not replayed).

`scripts/smoketest-context.sh` is the faster check — twelve acceptance
criteria against the built binary, using a stub endpoint, needing no model.
