# simple-harness

A small, deterministic, terminal-first execution kernel for **one AI
role** — written in Go, distributed as a single binary. It runs one
externally assigned role against one externally resolved
OpenAI-compatible model endpoint, with a fully observable model/tool
loop. It deliberately does **not** orchestrate workflows, select
harnesses, or manage model lifecycles — those belong to the systems
above it.

```text
one task → one role → one workspace → one session
        → one model endpoint → model/tool loop → observable result
```

## Use in the DPMtF ecosystem

simple-harness is fully standalone — but it is also one of the coding
harnesses the **DPMtF** framework can drive.

**DPMtF (Deterministic Process Management to Finalisation)** is an
open-source framework for coordinating multiple AI agents, models, and
coding tools through a controlled process from defined intent to
verified completion. Its principle: **the right model, the right
harness, the right role** — model selection and harness selection are
kept separate from the workflow itself, so premium resources are spent
only where they add value.

> A powerful cloud agent may benefit from a feature-rich coding harness,
> while a local model may perform better through a lighter harness with
> less overhead. DPMtF therefore treats the model and the harness as
> separate parts of the execution configuration rather than forcing
> every agent through the same toolchain.

That lighter harness is what simple-harness is. In the ecosystem,
[Harness Allocator](https://github.com/svend-blip/harness-allocator)
resolves a role to simple-harness and builds the invocation,
[Model Allocator](https://github.com/svend-blip/model-allocator)
supplies the resolved model endpoint, and
[DPMtF-WebUI](https://github.com/svend-blip/DPMtF-WebUI) dispatches
governed flow steps through it — the composition proven live by the
9000 test flows. The quickstart in DPMtF-WebUI's SETUP.md clones this
repo as a sibling, and the committed runtime binary means a fresh clone
works with nothing to build.

None of that is required to use it: without DPMtF, simple-harness runs
exactly as described below.

**The DPMtF ecosystem:** [DPMtF-WebUI](https://github.com/svend-blip/DPMtF-WebUI) · [model-allocator](https://github.com/svend-blip/model-allocator) · [harness-allocator](https://github.com/svend-blip/harness-allocator) · [mcp-light](https://github.com/svend-blip/mcp-light) · [DPMtF-LightWorker](https://github.com/svend-blip/DPMtF-LightWorker) · [simple-harness](https://github.com/svend-blip/simple-harness)

## Quick start

```bash
# interactive (endpoint/model come from the config hierarchy:
# ~/.simple-harness/config.json, .simple-harness/config.json, env;
# each prompt runs the agent loop, tool calls included)
bin/simple-harness --workspace ~/project --permission workspace_write

# headless (machine-readable events on stdout, deterministic exit codes)
bin/simple-harness run \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace ~/project \
  --permission workspace_write \
  --prompt-file task.md \
  --max-turns 8 \
  --output jsonl

# the prompt can also come from stdin
cat task.md | bin/simple-harness run --base-url ... --model ... --prompt-file -
```

Run mode takes the endpoint and model from its flags only — the
public contract exits 2 when either is empty — so an external
controller never inherits an endpoint from a config file it did not
write. Interactive mode reads them from the config hierarchy.

`bin/simple-harness` is a POSIX wrapper that `exec`s the committed
runtime binary (`bin/simple-harness-runtime`), so signals reach the
harness process directly.

On start, interactive mode prints its identity card and drops you at the
prompt:

```text
session_id: 01a04f7a-2c0e-7a27-99f4-0d07a06ef30d
model:      qwen
endpoint:   http://127.0.0.1:8080
workspace:  ~/project
permission: READ_ONLY
events:     ~/.simple-harness/sessions/01a04f7a-2c0e-7a27-99f4-0d07a06ef30d/events.jsonl
(type /help for built-in commands, /exit to quit, Ctrl+D to exit, Ctrl+C cancels the active request — second Ctrl+C terminates)

simple-harness>
```

## What it provides

- **Agent loop** with tool dispatch: model request → stream → validate →
  authorize → execute → record → append → next request, bounded by
  `--max-turns` (run mode and, per prompt, interactive mode). Malformed
  model tool-calls are untrusted input — structured rejection, never a
  crash. Each interactive prompt is a fresh composition: the REPL does
  not carry conversation history from one prompt to the next.
- **Nine builtin tools:** `read_file`, `write_file`, `apply_patch`,
  `list_directory`, `search_files`, `grep`, `shell`, `list_skills`,
  `load_skill` — each with explicit schema, validation, structured
  results, and observable start/completion. `list_skills` and
  `load_skill` enable model-invoked skill discovery and loading at
  runtime (the model can enumerate available skills and load one into
  its context mid-session).
- **Deterministic permission modes** enforced in code at the execution
  boundary: `read_only`, `workspace_write`, `full_access`. No silent
  escalation; the effective mode is externally visible (`config show`).
- **Headless contract:** versioned JSONL events (`protocol_version: "1"`),
  a real status model, documented exit codes (0 ok, 1 max-turns / generic
  failure, 2 config, 3 model/API, 4 permission, 6 interrupted), tested
  SIGINT/SIGTERM semantics, and
  child-process cleanup via process groups. No terminal scraping needed —
  see `docs/HARNESS-CONTRACT.md` (the frozen V1 public contract) and
  `scripts/contract-check.sh` (model-free conformance checker).
- **One system message on the wire:** the composition keeps one system
  message per slot (base prompt, external governance, each skill), but
  `toWireMessages` joins the leading run into a single system message.
  Cloud endpoints accept several; a local runtime applying the model's
  chat template does not (FreeToken/Qwen: HTTP 400 "System message must be
  at the beginning", which the harness surfaces only as exit 3). Measured
  2026-09-03; see `internal/model/client.go`.
- **Output ceiling:** `SIMPLE_HARNESS_MAX_OUTPUT_TOKENS` (default 8192) is
  sent as `max_tokens`. A tool call larger than the ceiling is truncated
  mid-JSON and the run ends with exit 1 and no error text — the session's
  last `usage` event then shows `completion_tokens` equal to the ceiling.
  Size it for whole-file writes (32768 for coding roles) and tell the
  model to write each file with its own call.
- **Reasoning controls:** `reasoning_effort`, `enable_thinking` and
  `thinking_budget` (config / `SIMPLE_HARNESS_*`) are sent per request
  when set; `usage` events carry `reasoning_tokens`.
- **MCP client (V2):** configuration-pinned servers via the
  `mcp_servers` config key — the harness connects to configured
  Model Context Protocol servers at session start (run mode,
  interactive mode and `tools`) and exposes their tools alongside the
  builtins. A server's tools count as mutations for the permission
  policy unless the server is declared `read_only`; `api_key` and
  `headers` are sent on every request. See
  `docs/examples/mcp-light.json` for a reference config and
  `docs/HARNESS-CONTRACT.md` for the MCP client section.
- **Model-invoked skills:** the `list_skills` + `load_skill` builtin
  tools let a model discover and load skills at runtime — the
  `--skill` flag and `/skill` slash command remain for human-initiated
  loading, but a model can now enumerate available skills and pull
  one into its context mid-session.
- **Sessions:** stable identity per execution, inspectable history
  (`session.json` + `messages.jsonl` + `events.jsonl` under
  `--state-dir`), `sessions list` / `sessions show`. `messages.jsonl`
  is the execution history: the prompt, every assistant message with
  its tool calls, every tool result, the final answer.
- **Skills:** reusable instruction packages from
  `~/.simple-harness/skills/` and `.simple-harness/skills/` (`--skill`,
  `/skill`); `share/skills/cold-start/SKILL.md` is the shipped reference.
- **Context observability:** `context show` / `context doctor` (and
  `/context` interactively) — per-category token accounting with honest
  estimates and diagnostics for oversized contributors.

## Development

```bash
./scripts/test.sh           # full suite (every package, mocked models)
./scripts/contract-check.sh # black-box V1 contract conformance
./scripts/e2e-coding.sh URL MODEL   # live coding-agent acceptance
./scripts/e2e-review.sh URL MODEL   # live read-only reviewer acceptance
```

Built with Go (see `docs/ADR-001-implementation-language.md` for the
Python-vs-Go decision record). Architecture: `docs/ARCHITECTURE.md`.
Comparative validation against Pi and Whip:
`docs/COMPARATIVE-VALIDATION.md`. Concurrency stance (sequential V1,
extension points documented): `docs/ADR-002-concurrency.md`.

## Bounded context

The harness keeps what it sends to the model inside a budget derived from the
model's context limit, so a long session does not grow until the runtime
refuses it. Older tool results are replaced with placeholders, then older
conversation is compacted into a pinned working summary; instructions,
skills and the current task are never touched to make room. The recent
verbatim window is narrowed, down to a floor of two messages, and a single
tool result larger than the budget allows is cut to an excerpt, only when
the alternative is failing a run that could continue — and if the budget
still cannot be met the run fails and says why.

Durable history is not the active context. Nothing is deleted — what changes
is the view that goes on the wire.

It needs no configuration: the harness asks the runtime what window it
serves the model with (llama.cpp, vLLM, SGLang and Ollama all say);
`--context-limit <n>` or `context.model_limit` tells
it the model's window. See [docs/BOUNDED-CONTEXT-LIFECYCLE.md](docs/BOUNDED-CONTEXT-LIFECYCLE.md).

## Boundaries

The governing scope (`docs/SCOPE.md`) draws hard lines: no multi-agent
orchestration, no harness selection, no model allocation or lifecycle,
no plugin marketplace, no semantic memory. Simple Harness executes one
role well; everything else lives above it.

## Status

V1 complete: 18 governed runs, SCOPE validation green
(38/40 criteria measured; see the flow workspace's validation report).
V2 wave in progress per the 2026-08-29 scope amendment: MCP client with
configuration-pinned servers (§43), mcp-light reference integration
(§44), and model-invoked skills (§45).
Criterion 30: see [docs/EVIDENCE-criterion-30.md](docs/EVIDENCE-criterion-30.md) — PASS per Run 023 / handoff 078.

## Requirements

Go (the runtime binary at `bin/simple-harness-runtime` is pre-built and
committed; no Go toolchain needed to run it). See
`docs/ADR-001-implementation-language.md` for the Python-vs-Go decision
record. Optional: `jq` + `python3` for the test suite (`./scripts/test.sh`)
and contract checker (`./scripts/contract-check.sh`).

### Ecosystem dependencies and installation order

**This repository is row 1.** It depends on nothing else here. Everything it gets from the rest — governance, retrieval, project state — arrives through the MCP servers declared in row 6.

The same table is in the README of each of the six repositories; it was
written from the code on 2026-09-19. Install top to bottom: each row
needs only rows above it.

| # | Repository | Needs | Serves | Needed by |
|---|---|---|---|---|
| 0 | a model runtime (Ollama, FreeToken, llama.cpp, a cloud endpoint) | — | an OpenAI-compatible `/v1` endpoint | every harness |
| 1 | [simple-harness](https://github.com/svend-blip/simple-harness) | Go 1.27 to build; row 0 to run | the `simple-harness` command on `PATH` | FlowRunner steps that name it, DPMtF-WebUI roles launched through harness-allocator |
| 2 | [scope-mcp](https://github.com/svend-blip/scope-mcp) | Node >= 22.5 (built-in `node:sqlite`) | a stdio MCP server; state in `<workspace>/.scope-mcp/state.db` | any harness that declares it (row 6) |
| 3 | [knowledge-service](https://github.com/svend-blip/knowledge-service) | Python >= 3.11; provider `leann`: a CUDA GPU with ~2.5 GB free VRAM; provider `portable`: CPU only | `http://127.0.0.1:9140/v1` — retrieval over LEANN indexes | mcp-light (four `knowledge_*` tools), DPMtF-WebUI (service mode) |
| 4 | [DPMtF-WebUI](https://github.com/svend-blip/DPMtF-WebUI) | Python 3.10+, tmux, git; [model-allocator](https://github.com/svend-blip/model-allocator) and [harness-allocator](https://github.com/svend-blip/harness-allocator) beside it; a harness on `PATH` (row 1); row 3 optional | `:9130`, the governance templates, BridgeV002, `DPMtF-WebUI/databases/dpmtf.db` | mcp-light (reads its files and database) |
| 5 | [mcp-light](https://github.com/svend-blip/mcp-light) | Python 3.8+, `mcp[cli]`; read access to the DPMtF-WebUI checkout and database (row 4) and to `model-allocator/allocator.db`; row 3 for the knowledge tools | `http://127.0.0.1:9135/mcp` — read-only MCP context server | any harness that declares it (row 6) |
| 6 | harness wiring | rows 1, 2, 5 | `~/.simple-harness/config.json` with an `mcp_servers` entry per server | every simple-harness run on the machine, whoever launched it |
| 7 | [FlowRunner](https://github.com/svend-blip/FlowRunner) | Go 1.27 to build; at run time, every harness a FlowApp's steps name, on `PATH` (row 1 for `simple-harness`) | the `flowrunner` command and desktop app | — |

Rows 1, 2 and 3 depend on nothing else in the table and can be installed
in any order. knowledge-service's one-time `import-registry` step reads
the DPMtF-WebUI database, so run that step after row 4; the service itself
does not need DPMtF-WebUI at run time.

#### Row 6: what wires a harness to the servers

Neither FlowRunner nor DPMtF-WebUI tells simple-harness which MCP servers
exist. simple-harness reads its own configuration —
`~/.simple-harness/config.json`, then the nearest
`.simple-harness/config.json` at or above its working directory, the
later file replacing the earlier one's `mcp_servers` whole — and that is
the entire wiring:

```json
{
  "mcp_servers": [
    { "name": "mcp-light", "transport": "http",
      "endpoint": "http://127.0.0.1:9135/mcp", "permission": "read_only",
      "allowlist": ["get_governance_index", "get_governance_file",
                    "knowledge_search", "knowledge_scopes",
                    "knowledge_learning", "knowledge_retrievals"] },
    { "name": "scope-mcp", "transport": "stdio",
      "command": ["node", "/abs/path/to/scope-mcp/src/server.js"],
      "permission": "workspace_write" }
  ]
}
```

Without the `knowledge_*` names in the allowlist an agent has no
retrieval. Without the scope-mcp entry it has no durable project state:
no simple-harness configuration declares scope-mcp unless you add it. A
stdio server is started in the workspace, so scope-mcp keeps its state
with the project.

#### How LEANN retrieval reaches an agent

```text
model in a harness
  -> the harness's MCP client              (mcp_servers, row 6)
  -> mcp-light          :9135/mcp          knowledge_search / _scopes / _learning / _retrievals
  -> knowledge-service  :9140/v1           /v1/search, /v1/scopes, /v1/learning, /v1/retrievals
  -> LEANN (hnsw, CUDA)  or the portable CPU provider
```

LEANN lives in knowledge-service and nowhere else. mcp-light is its only
MCP face. scope-mcp has no retrieval of any kind, and simple-harness has
none of its own: it is a generic MCP client that also fills in `run_id`,
`handoff_id` and `flow_key` on MCP calls from `SIMPLE_HARNESS_RUN_ID`,
`SIMPLE_HARNESS_HANDOFF_ID` and `SIMPLE_HARNESS_FLOW_KEY`, so that a
retrieval can be attributed to the run that made it.

#### What is and is not automatic

- FlowRunner has no default harness: every step of a FlowApp names one,
  and an empty `harness:` fails validation. A step that names
  `simple-harness` gets whatever `simple-harness` resolves to on `PATH`
  at dispatch — so a rebuilt simple-harness is used by the next run with
  no change to FlowRunner, and a stale copy on `PATH` (or a bundled
  `simple-harness.exe` beside a packaged FlowRunner) is used just as
  faithfully.
- FlowRunner passes `HOME` through, so a simple-harness step loads the
  machine's `~/.simple-harness/config.json` and sees the servers declared
  there. On a machine without that file the same FlowApp runs with no MCP
  server and no retrieval, and nothing reports the difference.
- FlowRunner sets the three position variables. DPMtF-WebUI's BridgeV002
  launch does not, so retrievals made from its simple-harness panes are
  logged without a run.
- A FlowApp's `knowledge:` block reaches the harness as `KNOWLEDGE_PROVIDERS`
  and `KNOWLEDGE_<NAME>_URL`. simple-harness does not read them; for a
  simple-harness step, retrieval comes through mcp-light or not at all.


## Installation

### Install manually

Download or clone the repository. The runtime binary at
`bin/simple-harness-runtime` is committed alongside the wrapper
`bin/simple-harness` — no build step is required to run the harness.
Optionally rebuild from source with
`go build -o bin/simple-harness-runtime ./cmd/simple-harness`.

### Install using an Agent

Point a tool-capable model at the repository and have it invoke
`bin/simple-harness run` with the appropriate flags. The harness is
self-contained and works out of the box.

### Verify installation

Run `./scripts/test.sh` (every package, mocked models) and
`./scripts/contract-check.sh` (model-free V1 contract conformance).
Both scripts are part of the committed repository.

## Configuration

Configuration is JSON, read from the hierarchy:
`~/.simple-harness/config.json` → `.simple-harness/config.json`
(searched upward from the working directory) → `SIMPLE_HARNESS_*`
environment variables. Keys: `model` (`base_url`, `model`, `api_key`,
`temperature`, `max_output_tokens`, `request_timeout`,
`reasoning_effort`, `enable_thinking`, `thinking_budget`),
`shell_timeout`, `context` (`policy`, `model_limit`,
`generation_reserve`, `safety_reserve`, `keep_recent_turns`,
`tool_result_pruning`, `compaction`, `probe_limit`,
`compaction_reasoning_effort`; `policy`, `model_limit`, `probe_limit` and
`compaction_reasoning_effort` also as `SIMPLE_HARNESS_CONTEXT_POLICY` /
`_CONTEXT_MODEL_LIMIT` / `_CONTEXT_PROBE_LIMIT` /
`_CONTEXT_COMPACTION_REASONING_EFFORT`)
and `mcp_servers`. The `mcp_servers` config key (V2) lists
configuration-pinned MCP servers the harness connects to at session
start — see `docs/examples/mcp-light.json` for a reference config and
`docs/HARNESS-CONTRACT.md` for the MCP client section. The `api_key`
field is redacted in `config show` output per SCOPE §30.

## MCP position defaults

The harness fills three attribution arguments on MCP tool calls when
the model omits them, from its own environment:

- `SIMPLE_HARNESS_RUN_ID` → the `run_id` argument
- `SIMPLE_HARNESS_HANDOFF_ID` → the `handoff_id` argument
- `SIMPLE_HARNESS_FLOW_KEY` → the `flow_key` argument

The rule: an argument is filled only when the tool's schema declares
that property, the call has no value for it (absent or the empty
string), and the matching environment variable is non-empty. A value
the model supplied always wins. The fills apply to MCP tools only;
builtin tools see exactly what the model sent. `simple-harness config
show` renders the current values under a top-level `position` object
(empty string when unset).

Why: a retrieval that cannot be attributed to a run cannot be audited.

## Running

Interactive: `bin/simple-harness --workspace ~/project`. Headless:
`bin/simple-harness run --base-url URL --model NAME --workspace DIR
--permission workspace_write --prompt-file task.md --max-turns 8
--output jsonl`. The wrapper `bin/simple-harness` `exec`s the
committed runtime binary so signals reach the harness process
directly. See the Quick start section above for examples.

## Testing

Run `./scripts/test.sh` for the full test suite (every package, mocked
models) and `./scripts/contract-check.sh` for the model-free V1
contract conformance checker. Live acceptance runners
(`./scripts/e2e-coding.sh URL MODEL` and
`./scripts/e2e-review.sh URL MODEL`) require a reachable OpenAI-
compatible endpoint.
