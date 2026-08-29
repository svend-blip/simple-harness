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
# interactive
bin/simple-harness \
  --base-url http://127.0.0.1:8080/v1 \
  --model qwen \
  --workspace ~/project \
  --permission workspace_write

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

## What it provides

- **Agent loop** with tool dispatch: model request → stream → validate →
  authorize → execute → record → append → next request, bounded by
  `--max-turns`. Malformed model tool-calls are untrusted input —
  structured rejection, never a crash.
- **Seven builtin tools:** `read_file`, `write_file`, `apply_patch`,
  `list_directory`, `search_files`, `grep`, `shell` — each with explicit
  schema, validation, structured results, and observable start/completion.
- **Deterministic permission modes** enforced in code at the execution
  boundary: `read_only`, `workspace_write`, `full_access`. No silent
  escalation; the effective mode is externally visible (`config show`).
- **Headless contract:** versioned JSONL events (`protocol_version: "1"`),
  a real status model, documented exit codes (0 ok, 2 config, 3 model/API,
  4 permission, 6 interrupted), tested SIGINT/SIGTERM semantics, and
  child-process cleanup via process groups. No terminal scraping needed —
  see `docs/HARNESS-CONTRACT.md` (the frozen V1 public contract) and
  `scripts/contract-check.sh` (model-free conformance checker).
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

V1 complete: 17 governed runs, SCOPE validation green
(38/40 criteria measured; see the flow workspace's validation report).
V2 wave in progress per the 2026-08-29 scope amendment: MCP client with
configuration-pinned servers (§43), mcp-light reference integration
(§44), and model-invoked skills (§45).
