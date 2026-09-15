# SCOPE — simple-harness: MCP position defaults, so a retrieval is attributable to its run

Treat this file as the complete project scope for this workspace
(`/home/svend/simple-harness`). Re-initialise scope-mcp (`init_project`
with `reset: true`), ask only what is genuinely necessary, then build. The
tree is clean at `e5629bd`; three tests in `cmd/simple-harness`
(`TestRun_Skill_ContentInjectedIntoModelContext`,
`TestMCPLight_GetGovernanceIndex`,
`TestRun_SIGTERM_Headless_EmitsInterruptedAndExits6`) fail in a full
`go test ./...` on that commit and are **not yours to fix**: the first
fails on the untouched tree, the other two pass when run alone.

## 1. Purpose

The knowledge service logs every retrieval with the caller's position —
`run_id`, `handoff_id`, `flow_key` — so an operator can ask what a given
run retrieved. Measured on 2026-09-15 against 641 live rows: every row
written through this harness carries an empty `run_id` and `handoff_id`,
because those fields are tool-call arguments and the model simply omits
them. Telling the model to pass them (DPMtF now does, in the exported
role context) helps but cannot be relied on: the harness knows its own
position and the model's memory of it does not.

Give the harness the last word: when an MCP tool declares an argument
named `run_id`, `handoff_id` or `flow_key` and the model left it out, the
harness fills it from its environment before the call leaves the process.

## 2. Deliverable

### 2.1 Position defaults (`internal/mcp/registry.go`, or a new file in the same package)

- Three environment variables, read once per process (`os.Getenv`) and
  documented as the harness's position: `SIMPLE_HARNESS_RUN_ID`,
  `SIMPLE_HARNESS_HANDOFF_ID`, `SIMPLE_HARNESS_FLOW_KEY`.
- A pure function
  `applyPositionDefaults(schema tools.Schema, args map[string]any, env func(string) string) map[string]any`
  that returns a copy of `args` in which, for each of the three names, the
  value is filled **only when all of** (a) the schema's `Properties`
  declares that property, (b) the caller's `args` has no entry for it or
  its entry is an empty string, and (c) the environment variable is
  non-empty. A value the model supplied is never replaced, a property the
  tool does not declare is never added, and `args` itself is not mutated.
  `nil` args with a declared property and a set variable yields a new map.
- `mcpAdapter.Execute` calls it on `call.Arguments` before
  `transport.Call`, so the MCP server sees the filled arguments. Builtin
  tools are untouched: this lives in the MCP adapter only.
- The filled values never appear in a log line; the adapter's existing
  error messages are unchanged.

### 2.2 Config visibility (`internal/config/config.go`, `simple-harness config show`)

The rendered configuration gains a `position` object with the three
values as the process sees them (empty string when unset), beside the
existing fields, so an operator can see what the harness will send. No
new config-file key: the position comes from the environment only.

### 2.3 README

A short section `## MCP position defaults`: the three variables, the
exact rule (declared property + absent or empty argument + non-empty
variable), that a model-supplied value always wins, and the one sentence
of why — a retrieval that cannot be attributed to a run cannot be
audited.

### 2.4 Tests, named exactly (in `internal/mcp/` and `internal/config/`)

- `TestApplyPositionDefaultsFillsOnlyDeclaredAndAbsentArguments`
  (declared + absent → filled; declared + model value → untouched;
  declared + empty string → filled; undeclared → not added; unset
  variable → not added; the input map is not mutated)
- `TestAdapterExecuteSendsTheFilledArguments` (a stub transport records
  what it received; with `SIMPLE_HARNESS_RUN_ID` set and a schema that
  declares `run_id`, the transport sees it; with the variable unset it
  does not)
- `TestBuiltinToolsAreUnaffectedByPositionDefaults`
- `TestConfigShowRendersThePosition`

Every existing test that passes on `e5629bd` still passes.

## 2.5 Not yours

Three untracked backup binaries predate this task
(`bin/simple-harness-runtime.bak-pre-*`). Leave them exactly as they are:
do not rename, move, delete or commit them. TG4's fence ignores `bin/`
for that reason (amended by the reviewer 2026-09-15 after the criterion
flagged them).

## 3. Constraints

Work only here; never modify DPMtF, mcp-light, knowledge-service or
FlowRunner; no commits; no network; no services or models touched; en-US;
`gofmt` every changed file (the tree is formatted with Go 1.27 — leave it
formatted); no new dependency; the three failures named in the header stay
as they are and are not counted against you.

## 4. Definition of Done

```testgoals
id: TG1
what: the four named tests exist and pass, and the packages they live in are green
run: cd /home/svend/simple-harness && for t in TestApplyPositionDefaultsFillsOnlyDeclaredAndAbsentArguments TestAdapterExecuteSendsTheFilledArguments TestBuiltinToolsAreUnaffectedByPositionDefaults TestConfigShowRendersThePosition; do grep -rq "func $t" internal cmd || exit 1; done && go test ./internal/mcp ./internal/config ./internal/tools/... -count=1 > /dev/null 2>&1
expect: exit 0

id: TG2
what: the rule is wired into the adapter and documented, and the variables are named nowhere else
run: cd /home/svend/simple-harness && grep -rq "func applyPositionDefaults" internal/mcp && grep -rq "applyPositionDefaults" internal/mcp/registry.go && grep -q "SIMPLE_HARNESS_RUN_ID" README.md && grep -q "SIMPLE_HARNESS_HANDOFF_ID" README.md && grep -q "SIMPLE_HARNESS_FLOW_KEY" README.md && test "$(grep -rl 'SIMPLE_HARNESS_RUN_ID' --include=*.go internal cmd | wc -l)" -le 3
expect: exit 0

id: TG3
what: the tree is gofmt-clean and builds, and the whole suite is no worse than the baseline's three known failures
run: cd /home/svend/simple-harness && test -z "$(gofmt -l internal)" && go build ./... && test "$(go test ./... -count=1 2>&1 | grep -c '^--- FAIL')" -le 3
expect: exit 0

id: TG4
what: FENCE — only the deliverable paths changed
run: cd /home/svend/simple-harness && test -n "$(git status --porcelain)" && test -z "$(git status --porcelain | awk '{print $2}' | grep -v -E '^bin/' | grep -v -E '^(internal/mcp/|internal/config/|cmd/simple-harness/|README.md|SCOPE.md)')"
expect: exit 0

id: TG5
what: LIVE (reviewer only) — a real chain run's retrieval rows carry its run id
run: test -f /tmp/claude-1000/-home-svend-DPMtF-WebUI/e20394ae-27d0-4204-804f-5d6a2f5da054/scratchpad/position-live/ok
expect: exit 0
```

## 5. Initial Execution Instruction

`init_project` with `reset: true`; goals for 2.1–2.4 in order; checkpoint
after each goal; ask now, in one message, only what is genuinely ambiguous;
implement; run TG1–TG4 and `gofmt -l`; record coverage; `complete_project`;
report `git status` and the pasted output of TG1–TG4. TG5 is the
reviewer's, on a real FlowRunner run; do not attempt it.
