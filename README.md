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

## Quick start

```bash
# interactive (endpoint/model/permission come from the config hierarchy:
# ~/.simple-harness/config.yaml, .simple-harness/config.yaml, env)
bin/simple-harness --workspace ~/project

# headless (machine-readable events on stdout, deterministic exit codes)
bin/simple-harness run \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace ~/project \
  --permission workspace_write \
  --prompt-file task.md \
  --max-turns 8 \
  --output jsonl
```

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
  `--max-turns`. Malformed model tool-calls are untrusted input —
  structured rejection, never a crash.
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
  a real status model, documented exit codes (0 ok, 2 config, 3 model/API,
  4 permission, 6 interrupted), tested SIGINT/SIGTERM semantics, and
  child-process cleanup via process groups. No terminal scraping needed —
  see `docs/HARNESS-CONTRACT.md` (the frozen V1 public contract) and
  `scripts/contract-check.sh` (model-free conformance checker).
- **MCP client (V2):** configuration-pinned servers via the
  `mcp_servers` config key — the harness connects to configured
  Model Context Protocol servers at session start and exposes their
  tools alongside the builtins. See `docs/examples/mcp-light.json`
  for a reference config and `docs/HARNESS-CONTRACT.md` for the
  MCP client section.
- **Model-invoked skills:** the `list_skills` + `load_skill` builtin
  tools let a model discover and load skills at runtime — the
  `--skill` flag and `/skill` slash command remain for human-initiated
  loading, but a model can now enumerate available skills and pull
  one into its context mid-session.
- **Sessions:** stable identity per execution, inspectable history
  (`session.json` + `messages.jsonl` + `events.jsonl` under
  `--state-dir`), `sessions list` / `sessions show`.
- **Skills:** reusable instruction packages from
  `~/.simple-harness/skills/` and `.simple-harness/skills/` (`--skill`,
  `/skill`); `share/skills/cold-start/SKILL.md` is the shipped reference.
- **Context observability:** `context show` / `context doctor` (and
  `/context` interactively) — per-category token accounting with honest
  estimates and diagnostics for oversized contributors.

## Development

```bash
./scripts/test.sh           # full suite (12 packages, mocked models)
./scripts/contract-check.sh # black-box V1 contract conformance
./scripts/e2e-coding.sh URL MODEL   # live coding-agent acceptance
./scripts/e2e-review.sh URL MODEL   # live read-only reviewer acceptance
```

Built with Go (see `docs/ADR-001-implementation-language.md` for the
Python-vs-Go decision record). Architecture: `docs/ARCHITECTURE.md`.
Reference study of Pi and Whip: `docs/RECON.md` and
`docs/COMPARATIVE-VALIDATION.md`. Concurrency stance (sequential V1,
extension points documented): `docs/ADR-002-concurrency.md`.

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
Criterion 30: see [docs/EVIDENCE-criterion-30.md](docs/EVIDENCE-criterion-30.md) — honest FAIL per Run 023 / handoff 074.

## Requirements

Go (the runtime binary at `bin/simple-harness-runtime` is pre-built and
committed; no Go toolchain needed to run it). See
`docs/ADR-001-implementation-language.md` for the Python-vs-Go decision
record. Optional: `jq` + `python3` for the test suite (`./scripts/test.sh`)
and contract checker (`./scripts/contract-check.sh`).

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

Run `./scripts/test.sh` (13 packages, mocked models) and
`./scripts/contract-check.sh` (model-free V1 contract conformance).
Both scripts are part of the committed repository.

## Configuration

Configuration is read from the hierarchy:
`~/.simple-harness/config.yaml` → `.simple-harness/config.yaml` →
environment variables. The `mcp_servers` config key (V2) lists
configuration-pinned MCP servers the harness connects to at session
start — see `docs/examples/mcp-light.json` for a reference config and
`docs/HARNESS-CONTRACT.md` for the MCP client section. The `api_key`
field is redacted in `config show` output per SCOPE §30.

## Running

Interactive: `bin/simple-harness --workspace ~/project`. Headless:
`bin/simple-harness run --base-url URL --model NAME --workspace DIR
--permission workspace_write --prompt-file task.md --max-turns 8
--output jsonl`. The wrapper `bin/simple-harness` `exec`s the
committed runtime binary so signals reach the harness process
directly. See the Quick start section above for examples.

## Testing

Run `./scripts/test.sh` for the full test suite (13 packages, mocked
models) and `./scripts/contract-check.sh` for the model-free V1
contract conformance checker. Live acceptance runners
(`./scripts/e2e-coding.sh URL MODEL` and
`./scripts/e2e-review.sh URL MODEL`) require a reachable OpenAI-
compatible endpoint.
