# Quality & Integrity Audit — 2026-09-18

Executor: Fable 5.1. Scope: the "Existing Code Quality & Integrity
Audit" addendum. Purpose: establish a trustworthy baseline before
further Bounded Context Lifecycle work.

## Baseline

- Starting commit `5a0f9dd` on `main`, working tree clean.
- `gofmt`, `go vet`, `go build ./...` clean. Go 1.27.
- `go test ./...`: 2 failures in `cmd/simple-harness`
  (`TestMCPLight_GetGovernanceIndex`, `TestRun_Skill_ContentInjectedIntoModelContext`),
  3 further tests flaky (fixed sleep before SIGTERM against a
  non-routable address). `go test -race`: 3 data races in
  `internal/tools/builtins` (shell escalation flag).
- The committed `bin/simple-harness-runtime` was 14 source commits
  behind; 9 subprocess tests measured that stale binary.
- The `cmd` test suite was not hermetic: config discovery walked from
  the package directory up to the developer's real
  `~/.simple-harness/config.json`, connected every `run` test to the
  live mcp-light server and registered its tools into the
  process-global registry.

## Inspection

Subsystems traced end-to-end: CLI/config → model client → composition →
`RunAgent` → builtin and MCP dispatch → tool results → follow-up
request → session persistence; the interactive REPL; the bounded
context lifecycle; MCP http and stdio transports; the permission
pipeline; the workspace path normalization. Static searches:
TODO/FIXME/stub/placeholder markers, swallowed errors, `return nil`
after `err != nil`, goroutine lifecycles, `os.Getwd` uses, config keys
declared vs consumed, documentation claims vs code. Five parallel
read-only audit agents (MCP, builtins, config, tests/docs, context)
produced candidate findings; every finding below was verified by a
failing test before the fix.

## Findings and corrections

Severity per the addendum. All corrected unless marked.

### Critical
1. `path.Normalize`: an absolute path inside the workspace was returned
   without symlink evaluation → `<ws>/link/secret` read outside the
   workspace under READ_ONLY. Fixed (evaluate every branch).
2. `path.Normalize`: a not-yet-existing file under a directory symlink
   pointing outside passed (NotExist skipped evaluation) → `write_file`
   and `apply_patch` created files outside the workspace. Fixed
   (longest existing prefix is evaluated).
3. `grep`: the model's pattern was passed to `rg` positionally →
   `--pre=/bin/sh` executed a program over every file under READ_ONLY.
   Fixed (`-e <pattern> -- <path>`).

### High
4. SSE parser used only `tool_calls[0]` of each chunk → every parallel
   tool call after the first was silently dropped (llama.cpp, Ollama,
   vLLM emit them in one chunk). Fixed; entries without an index are
   identified by position.
5. A 2xx body with no SSE data, or a stream cut before `finish_reason`/
   `[DONE]`, was reported as a completed turn (exit 0, empty/partial
   text). Fixed (ErrParse).
6. Tools opened the raw path argument relative to the process cwd; the
   normalized path was validated and discarded → with `--workspace`
   elsewhere `read_file grep.go` read outside the workspace. Fixed:
   Authorize rewrites path arguments on a copy; Dispatch carries the
   workspace in the context; shell default cwd and skill-tool roots use it.
7. `apply_patch` searched later hunks from their original line numbers
   in the already-modified slice → correct multi-hunk patches rejected,
   or applied to the wrong lines after an earlier deletion. Fixed
   (running offset). Also: `\ No newline at end of file` rejected;
   CRLF files unpatchable. Fixed.
8. Interactive mode advertised the tool registry but ran the single-turn
   loop → a model that answered with a tool call produced an empty
   COMPLETED response. Fixed: REPL runs `RunAgent` (per-prompt
   `--max-turns`), wires MCP servers, reports bounds at the prompt.
9. `--prompt-file -` returned 0 without reading stdin or calling the
   model (a documented no-op pinned by a test). Fixed; test replaced.
10. stdio MCP transport never sent `initialize` → every server built on
    the reference SDK refused with -32602. Fixed (both transports
    perform the handshake and send `notifications/initialized`).
11. MCP `isError: true` results were returned as status ok. Fixed.
12. MCP schema conversion dropped non-plain-string types (`anyOf [T,
    null]`, type arrays) and defaulted `additionalProperties` to false →
    required arguments rejected as `additional_property`. Fixed.
13. READ_ONLY did not constrain MCP tools (mutation set was three
    builtin names); per-server `permission` was never consumed. Fixed:
    a server's tools are registered as mutations unless the server is
    declared `read_only`.
14. The `context` config block was documented and never loaded. Fixed
    (per-field overlay, `SIMPLE_HARNESS_CONTEXT_POLICY` /
    `_MODEL_LIMIT`, rendered by `config show`).
15. MCP `api_key`/`headers` were parsed, redacted and never sent. Fixed.
16. Compaction inserted a system message after the user task → rejected
    by runtimes applying a chat template (FreeToken 400). Fixed: the
    summary is a pinned user-role message, never compacted again.
17. `Fit` was given the raw history each turn → re-pruned and
    re-compacted every turn once over budget (extra inference per turn,
    double-counted stats). Fixed: the fitted view is carried forward.
18. Headless SIGTERM emitted `interrupted` without the contract's
    terminal `completed(exit_code: 6)`. Fixed; the conformance checker
    measured it.

### Medium
19. `session.json` recorded exit_code 1 for a permission violation the
    process exited 4 on, and completed/0 for a `--limit` overflow (exit
    2). Fixed (the record carries the process exit code).
20. `messages.jsonl` held the prompt and one concatenated assistant
    line; the contract promised every message including tool results.
    Fixed via `loop.Config.OnMessage` (assistant with tool_calls, tool
    results, final answer, additive fields).
21. Assistant text streamed alongside tool calls was dropped from the
    history; tool calls without an id produced an invalid follow-up.
    Fixed (kept; synthesized ids).
22. Data race on the shell tool's `escalated` flag; a goroutine parked
    on `ctx.Done()` per call; a 30 s polling goroutine. Fixed (all
    select on the child's exit). Race on run mode's `interrupted` flag.
    Fixed (atomic).
23. MCP init ran before `run` flag parsing with an empty workspace →
    `run --help` exited 2 when a server was down. Fixed (init inside
    `runModeExecute`). No startup timeout; http client had a fixed
    30 s; stdio none. Fixed (30 s listing bound; tool calls bounded by
    `shell_timeout`). stdio 1 MiB line cap; cancelled stdio call left a
    half-dead transport; multi-event SSE bodies unparseable; error
    bodies dropped; child stderr discarded; child inherited
    `SIMPLE_HARNESS_API_KEY`. All fixed.
24. Ledger accumulated across prompts (spurious `--limit` overflow,
    N copies in `/context`); tool schemas never accounted; inline
    `--system` missing from the lifecycle section; `--limit 0`
    overwrote the model limit; narrowing skipped the floor; Reductions
    counted attempts; compaction inferences invisible in events. Fixed
    (`COMPACTING` status + `model_request` + `usage`).
25. `shell cwd` was not path-shaped → cwd outside the workspace under
    WORKSPACE_WRITE. Fixed. `read_file` past EOF returned the last line
    again; `search_files` aborted on one unreadable subdirectory;
    `list_directory` called a symlink-to-dir a file; huge `timeout_ms`
    overflowed. Fixed.
26. Unknown subcommands fell through to interactive mode (exit 0, a
    session dir left behind). Fixed (exit 2).
27. The config loader applied `~/.simple-harness/config.json` a second
    time as project config for projects under HOME. Fixed.
28. `tools` subcommand did not list MCP tools although the contract
    said so. Fixed.

### Low / documentation
29. README/ARCHITECTURE/SCOPE named `config.yaml` (loader reads JSON);
    contract listed six status values as emitted that never were, a
    `--prompt` flag, missing `usage` event, missing `--context-limit`;
    ARCHITECTURE claimed `max_tool_calls`/`max_execution_time` and event
    fields that do not exist; README referenced a missing `docs/RECON.md`
    and wrong package counts; BCL doc showed YAML and omitted narrowing;
    root `SCOPE.md` was a task handoff. All corrected; stale package
    comments (loop "tool-less", perm "stub", mcp "unimplemented")
    replaced.
30. `ModelError` for a connection failure rendered "HTTP 0:" with no
    cause. Fixed.

### Test-suite corrections (each justified in the commit)
- `cmd` package hermetic: `TestMain` isolates HOME and cwd and builds
  the runtime binary from the source under test.
- Loader: user config no longer doubles as project config.
- Stale expectation (3 wire system messages) updated to the joined
  message with order asserted.
- Two e2e stubs omitted `[DONE]` with a rationale about harness state
  the server cannot observe; three subprocess tests dialled a
  non-routable address with a fixed sleep. All now hold the request on
  a local server and signal once `model_request` is on disk.
- A test pinning the `--prompt-file -` no-op was removed.
- MCP: schema-open-by-default and the three-request session exchange
  are the new expectations.

## Tests

- Regression tests added: 45 (`audit_regressions_test.go` in model,
  loop, tools, builtins, path, perm, mcp, ctxlife, cmd; plus config).
- Final: `go test ./...` 555 passing, 0 skipped, 15 packages;
  `go test -race ./...` clean; `go vet`, `gofmt` clean.
- Coverage: cmd 78 %, config 63 %, context 94 %, ctxlife 90 %,
  event 100 %, loop 77 %, mcp 84 %, model 81 %, path 81 %, perm 82 %,
  procgroup 75 %, session 71 %, skill 79 %, tools 92 %, builtins 86 %.
- `scripts/smoketest-context.sh`: 12/12. `scripts/contract-check.sh`
  with a live endpoint: 5/5 (checks (c) and (d) corrected to measure
  stdout and to signal on the `model_request` event).
- Live: headless run against Ollama `qwen3.6-27b-64k` with the live
  mcp-light server — three tool calls in one chunk (`list_directory`,
  `read_file`, `get_governance_index`), correct answer, exit 0;
  interactive prompt dispatching `read_file` through the REPL with the
  full history in `messages.jsonl`.
- harness-allocator: `test_simple_harness_adapter`, `test_terminal`,
  `test_launchspec`, `test_adapter_contract` — 330 passed.

## FlowRunner / Harness Allocator compatibility

Invocation grammar, exit codes, event envelope and session layout are
unchanged. Additive: `usage`/`COMPACTING` were already or are now
documented; `messages.jsonl` lines gain optional `tool_calls` /
`tool_call_id`; `session.json` exit codes are now accurate. Behavioural
change to note: tools of an MCP server not declared `read_only` are
mutations (denied under READ_ONLY) — the ecosystem's mcp-light
declaration is `read_only` and is unaffected. `run` still requires
`--base-url`/`--model` explicitly (contract check (b)).

## Remaining risks

- The live scope-mcp round trip owed by the first pass was run the same
  evening (`scripts/e2e-scope-mcp.sh`, 6/6) and found two defects the
  SDK-modelled stubs had not, both corrected with regression tests —
  see "Addendum: live scope-mcp round trip" below. scope-mcp is a stdio
  server; "not running" was never the obstacle.
- A stdio MCP server inherited the harness's cwd, not `--workspace`, so
  scope-mcp put `.scope-mcp/state.db` wherever the harness was launched
  from. Corrected after the report: the server starts in the workspace,
  and an optional `cwd` on the stdio declaration overrides it (relative
  to the workspace). `scripts/e2e-scope-mcp.sh` criterion 7 launches
  the harness from elsewhere and is red against the code before it.
- MCP http: no re-initialize after a server restart (404 "Session not
  found" ends the session's MCP use); no `DELETE` on close.
- Config: lenient env parsing (`0.7abc` → 0.7), negative
  `max_output_tokens`/`request_timeout` accepted, unknown
  `SIMPLE_HARNESS_*` names ignored silently, duplicate server names
  accepted. Run mode ignores `SIMPLE_HARNESS_BASE_URL`/`_MODEL` by
  contract.
- `request_timeout` (default 30 s) bounds the whole streamed response;
  long generations on a slow local model need it raised. Documented in
  the config keys; not changed.
- Interactive mode carries no conversation history across prompts
  (documented; not a V1 requirement).
- Compaction sends the whole reducible span in one request; a span
  that overflowed the window can overflow the compaction request.
- `max_tool_calls` / `max_execution_time` from SCOPE §3 remain
  unimplemented (documented as extension points).

## Addendum: live scope-mcp round trip

Run against `~/scope-mcp` at `4db943a` (reference TypeScript SDK, stdio)
with a scripted model, after the report above was written.

- **High — no server built on the TypeScript SDK could be listed.**
  `internal/mcp/transport_stdio.go`, `transport_http.go`. Observed:
  `mcp server unreachable: … listing failed: context deadline exceeded`,
  exit 2, with the server demonstrably up. Expected: the listing.
  Root cause: a request without parameters went out as `"params":null`.
  JSON-RPC allows only an object or array there; the SDK validates the
  envelope before dispatch and drops the message unanswered, so the
  harness waited for a reply that was never coming. Python servers
  (mcp-light) tolerate it, and the audit's stubs matched on `"method"`
  only. Correction: the member is omitted when there are no parameters.
  Tests: `TestStdioTransportOmitsAbsentParams`,
  `TestHTTPTransportOmitsAbsentParams`.
- **Medium — the model was shown a type-only schema for MCP tools.**
  `internal/mcp/registry.go`, `internal/loop/loop.go`. Observed on the
  wire for `set_goals`: `{"goals":{"type":"array"}}`. Expected: the
  server's schema — the goal object's fields, the status enum, every
  parameter description. Root cause: the request was rendered from
  `tools.Schema`, which holds only what the validator consumes.
  Correction: the adapter carries the server's `inputSchema`
  (`tools.WireSchemaProvider`) and the loop prefers it; `$schema` is
  dropped and the `int`/`bool` shorthand normalised at every depth, so
  strict endpoints keep accepting it. Validation is unchanged. Tests:
  `TestWireSchemaKeepsWhatTheModelNeeds`,
  `TestToolDefinitionsCarryTheFullSchemaWhenAToolHasOne`. Effect on
  accounting: tool definitions are larger and are counted as sent —
  the ledger reads the same rendering the request uses.

Verified live: listing, five tool rounds with correct call/result
pairing, a server-side `isError` reaching the model as a failure, a
schema violation rejected before the server, and state written by one
harness process read back by the next.

## Readiness

The baseline is technically ready for further Bounded Context
Lifecycle work: the agent loop, message history, request construction,
tool-call/result representation, MCP behaviour, session persistence
and context accounting are validated by the suite, the race detector,
the conformance checker and live runs listed above. The items under
Remaining risks are known and documented, none Critical or High.
