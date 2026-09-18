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
7. Otherwise fail explicitly, naming what could not be reduced.

Steps 4 to 6 never touch pinned context. The view fitted for one call is
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

Measured 2026-09-17 against `qwen3.6-27b-64k` through Ollama, a 16 384-token
limit, twelve turns, the task being to read this repository one file at a time:

| arm | model calls | input tokens | largest prompt | reductions |
|---|---:|---:|---:|---:|
| bounded | 8 | 62 047 | 10 853 | 2 |
| baseline | 10 | 171 634 | **46 079** | 0 |

The baseline's largest prompt was 2.8x the stated limit and it completed
anyway, because that model's real window is 64k. That is the failure this
addendum is about: an unbounded harness works until the day the numbers line
up differently, and then it does not.

The bounded arm used 64% fewer input tokens and never exceeded its budget. It
also failed at turn 8 on the first measurement, which is how the
window-narrowing reduction came to exist — see §14 step 12 and
`RecentWindowNarrowings`. The re-measurement after that fix is outstanding:
the GPU on the benchmark host stopped responding partway through it.

`scripts/smoketest-context.sh` is the faster check — twelve acceptance
criteria against the built binary, using a stub endpoint, needing no model.
