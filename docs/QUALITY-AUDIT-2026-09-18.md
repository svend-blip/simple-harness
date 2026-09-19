# Quality & Integrity Audit — 2026-09-18

Executor: Fable 5.1. Scope: the "Existing Code Quality & Integrity
Audit" addendum. Purpose: establish a trustworthy baseline before
further Bounded Context Lifecycle work.

Updated 2026-09-19 with what was found after the first report — by the
live scope-mcp round trip it still owed, and by the Bounded Context
Lifecycle measurements made on top of the audited baseline. Those
findings are in the three addenda before "Readiness"; "Tests" and
"Remaining risks" carry the current figures. Head at this update:
`276f908`.

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

- Regression tests added: 45 in the first pass
  (`audit_regressions_test.go` in model, loop, tools, builtins, path,
  perm, mcp, ctxlife, cmd; plus config), 21 more with the three addenda
  (mcp 8, ctxlife 7, model 2, config 2, loop 1, cmd 1): 66.
- First report: `go test ./...` 555 passing, 0 skipped, 15 packages.
  After the third addendum: 584 passing, 1 skipped — `TestLive_SizedCompaction`,
  which needs a model endpoint and runs when
  `SIMPLE_HARNESS_LIVE_BASE_URL` and `_MODEL` name one (run against
  Ollama and FreeToken, passing). `go test -race ./...` clean;
  `go vet`, `gofmt` clean.
- Coverage: cmd 78 %, config 63 %, context 94 %, ctxlife 90 %,
  event 100 %, loop 77 %, mcp 84 %, model 81 %, path 81 %, perm 82 %,
  procgroup 75 %, session 71 %, skill 79 %, tools 92 %, builtins 86 %.
- `scripts/e2e-scope-mcp.sh` against the real scope-mcp: 7/7.
- `scripts/e2e-mcp-restart.sh` (a real server restarted mid-run): pass.
  `scripts/e2e-mcp.sh` against the live mcp-light: exit 0.
- `scripts/smoketest-context.sh`: 12/12 at the first report, 14/14 now
  (two criteria came with the runtime probe). `scripts/contract-check.sh`
  with a live endpoint: 5/5 (checks (c) and (d) corrected to measure
  stdout and to signal on the `model_request` event).
- Live: headless run against Ollama `qwen3.6-27b-64k` with the live
  mcp-light server — three tool calls in one chunk (`list_directory`,
  `read_file`, `get_governance_index`), correct answer, exit 0;
  interactive prompt dispatching `read_file` through the REPL with the
  full history in `messages.jsonl`.
- harness-allocator: `test_simple_harness_adapter`, `test_terminal`,
  `test_launchspec`, `test_adapter_contract` — 330 passed.
  Re-run at `276f908`, after the two addenda: 330 passed, and
  `scripts/contract-check.sh` and `scripts/test.sh`, which drive the
  rebuilt runtime binary, exit 0.

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

- Owed by the first report and since settled: the live scope-mcp round
  trip (first addendum below) and the stdio server's working directory
  (second addendum).
- MCP http: no `DELETE` on close. (Re-initialize after a server
  restart, the other half of this item, is corrected — third addendum.)
  After a restart the tool listing is not fetched again, and a stdio
  server whose child exits is not restarted.
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
- The compaction default `reasoning_effort: "none"` falls back when an
  endpoint refuses it with 400/422. The fallback is proven against a
  stub answering as FreeToken answers a value it does not know; no
  endpoint available here refuses `none`, so it has no live proof. An
  endpoint that refuses with another status, or that accepts `none` and
  mishandles it, is not covered.
- Neither Ollama nor FreeToken reports reasoning tokens in its usage
  block, so `reasoning_tokens: 0` in the sidecar and in
  `scripts/benchmark-timing.py` means "not reported", not "none spent".
  The harness passes on what it is given; the figure can mislead. Not
  changed.
- `MinSummaryRoom` (512) and `MaxSummaryTokens` (1 024) are constants
  from measurements on two models and one kind of task; they are fields
  on the manager, not configuration keys.
- The GPU fault that aborted two benchmark runs on 2026-09-17/18 did not
  recur after the machine was rebooted — two runs of the same Ollama
  workload and every FreeToken and DeepSeek Harness run since, peak
  566 W and 80 °C — and its cause was not established.
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

## Addendum 2: found while measuring the Bounded Context Lifecycle

The lifecycle was closed out on the audited baseline and then measured:
twenty-four turns against Ollama, DeepSeek Harness as the behavioural
reference, and the question of why the bounded arm was 2.9x slower than
the unbounded one. Figures and method are in
`docs/BOUNDED-CONTEXT-LIFECYCLE.md`; what follows is what the
measurements found wrong, in the terms of this report.

- **Medium — a stdio MCP server ran in the wrong directory.**
  `cmd/simple-harness/mcp_init.go`, `internal/mcp/transport_stdio.go`,
  `internal/config`. Observed: scope-mcp, which keeps its state under
  its cwd, wrote `.scope-mcp/state.db` wherever the harness was launched
  from; a second run from another directory saw an empty project.
  Expected: state with the project. Root cause: the child inherited the
  harness's cwd; `--workspace` was never applied to it. Correction: the
  child starts in the workspace; an optional `cwd` on the stdio
  declaration overrides it (relative to the workspace; a configuration
  error on an http declaration). Tests:
  `TestStdioTransportStartsTheChildInTheGivenDirectory`,
  `TestMCP_StdioCwd_IsLoadedRenderedAndStdioOnly`, `TestMCPServerDir`;
  `e2e-scope-mcp.sh` criterion 7, red against the code before it.
  Commit `80044d5`.
- **Medium — compaction inferences that could not pay.**
  `internal/ctxlife/manager.go`. Observed: on a file-reading task, four
  compaction calls of 22-29 s were 103 s of a 163 s run (Ollama access
  log); reproduced on FreeToken, where three runs spent 14-28 % of their
  wall time on compactions that saved ~6, ~8 and ~122 tokens. Expected:
  an inference spent only where it can reduce the context. Root cause:
  compaction ran whenever pruning had not reached the budget, without
  regard to the reducible span — which on this kind of task is a few
  hundred tokens of remarks and placeholders, while the deficit sits in
  the recent window's tool results; the summaries came back no smaller
  than the span and were mostly refused. Correction: compaction is
  attempted only when the span, less the deficit, leaves room for a
  summary (`MinSummaryRoom`); otherwise the free reductions go first and
  compaction is tried after them. Same task and limit on FreeToken:
  156-201 s before, 101-116 s after. Ollama, twenty-four turns: the
  bounded arm went from 2.9x the baseline's time to 0.82x. Tests:
  `TestCompactionIsNotAttemptedWhenItCannotCoverTheDeficit`,
  `TestCompactionNeedsRoomForASummary`,
  `TestCompactionStillRunsOnceCheaperReductionsHaveMadeItWorthwhile`.
  Commit `470fecc`.
- **Medium — a run failed that one more free reduction would have
  fitted.** `internal/ctxlife/manager.go`. Observed: a 10.5k-token file
  read into a 7k budget ended the run with exit 2, "the window will not
  narrow below 2". Expected: the run continues. Root cause: narrowing
  was tried while the oversized result was whole and could not succeed,
  because that result sits inside the window's floor; after the result
  was cut to an excerpt, narrowing was not tried again. The defect
  predates the day's other change — the test fails identically against
  the previous `manager.go` — and was exposed by model variation, not
  caused by it. Correction: narrow again after a truncation. Test:
  `TestNarrowingIsRetriedAfterAnOversizedResultIsCut`. Commit `470fecc`.
- **Medium — the compaction request had no size target, no cap of its
  own, and reasoned at length.** `internal/ctxlife/compactor.go`,
  `internal/model/client.go`. Observed: summaries of 464-1 424 tokens,
  one taking 35 s; on a reasoning model 1 500-2 300 output tokens of
  reasoning ahead of a 500-token summary. Expected: a bounded, small
  request. Correction: the manager hands a `SizedCompactor` the room it
  has (at most `MaxSummaryTokens`), the instruction states it, and the
  request carries its own `max_tokens` (`ChatRequest.MaxTokens`) and
  `reasoning_effort` (`ChatRequest.ReasoningEffort`). A summary cut off
  at the cap is refused. The compaction asks for `none` unless
  `context.compaction_reasoning_effort` says otherwise, with a fallback
  to the model's own effort when the endpoint refuses the value;
  `inherit` restores the previous behaviour. Same span: 39.5 s to 8.6 s
  on Ollama, 34.1 s to 16.7 s on FreeToken. Tests:
  `TestARequestsOwnOutputCapIsSentWhenItIsTheSmaller`,
  `TestARequestsOwnReasoningEffortOverridesTheConfigured`,
  `TestTheCompactorIsToldHowLargeTheSummaryMayBe`,
  `TestModelCompactorStatesTheTargetAndCapsTheOutput`,
  `TestCompactionReasoningDefaultsToNoneAndFallsBackWhenRefused`,
  `TestCompactionReasoningEffort_FileAndEnv`. Commits `bfd751e`,
  `276f908`.
- **Caught before it landed.** The first version of the output cap,
  twice the target, passed every unit test and returned no summary at
  all against a real reasoning model: the cap was spent on reasoning.
  The stub it was tested against had no reasoning to spend. The
  committed version carries a reasoning allowance, dropped only when the
  request asks for no reasoning, and the live test that found this is in
  the repository.

One existing test was changed, deliberately:
`TestModelCompactorStatesTheTargetAndCapsTheOutput` pinned an empty
`ReasoningEffort` as "the model's own", which was the default until
`276f908` made it `none` on the repository owner's instruction. It pins
`inherit` to that behaviour now; nothing it asserted was weakened.

Behaviour changes a caller may notice, all from the two addenda: MCP
tool definitions are the server's full schemas, so they are larger on
the wire and in the accounting; a stdio MCP server runs in the workspace,
and a relative path inside its `command` resolves there (no stdio
declaration exists in the ecosystem's configurations today); a
compaction request asks for `reasoning_effort: "none"` by default.

## Addendum 3: an MCP server restarted during a run

Listed under Remaining risks in the first report; corrected 2026-09-19
on the repository owner's instruction.

- **High — a restarted MCP server ended the session's MCP use.**
  `internal/mcp/transport_http.go`. Observed, against a real server on
  the reference Python SDK restarted between two tool calls: every call
  after the restart failed with `http 404 … Session not found`, for the
  rest of the run. Expected: a new session and the call answered. Root
  cause: the `initialize` handshake ran exactly once per transport
  (`sync.Once`), so the session id from before the restart was sent
  forever; the same `sync.Once` cached a failed `initialize`, so a
  server still starting at the first call stayed unreachable after it
  was up. Correction: a `404` under a session id starts a new session
  and repeats the refused request once; concurrent calls share the one
  renewal; a failed `initialize` is attempted again on the next call.
  Tests: `TestHTTPTransportReinitializesAfterTheServerForgetsTheSession`,
  `TestHTTPTransportGivesUpOnAServerThatNeverKeepsASession`,
  `TestHTTPTransportInitializesAgainAfterAFailedInitialize`,
  `TestHTTPTransportConcurrentCallsShareOneNewSession` (run under the
  race detector). Live: `scripts/e2e-mcp-restart.sh` — the binary before
  the change fails both calls after the restart, the binary after it
  gets both answered by the new server process.

  The first report classed this Medium by leaving it among the risks.
  High is the honest class: it is "incorrect MCP behaviour" that takes
  out every MCP tool for the remainder of a run, and long runs are what
  the harness is for.

An existing test's comments were corrected, not its assertions:
`TestMCP_TransportHTTP_SessionPreflight` part 3 said a failed initialize
is cached and never retried. It asserts only that the error surfaces on
both calls while the server stays broken, which still holds.

## Readiness

The first report declared the baseline ready for further Bounded Context
Lifecycle work: the agent loop, message history, request construction,
tool-call/result representation, MCP behaviour, session persistence and
context accounting validated by the suite, the race detector, the
conformance checker and the live runs listed above.

That work has since been done on it, and measuring it is what produced
the second addendum — four defects the suite did not hold, three of them
in the lifecycle itself. All are corrected with regression coverage, and
the full validation passes. The one High finding the first report had
left among its risks — a restarted MCP server — is corrected in the third
addendum. No Critical or High finding is open. The items under Remaining risks are known and documented, none
Critical or High.

What the two addenda say about the first pass is worth keeping: both
sets of defects were found by running the harness against the real thing
— a server built on a different SDK, a reasoning model, a second
runtime — after stubs modelled on the author's understanding had passed.
