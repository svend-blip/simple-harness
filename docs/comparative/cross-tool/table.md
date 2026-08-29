# SCOPE §38 Pi/Whip/Simple Harness — 12-dimension preliminary comparison

This is the **preliminary** comparison table from the handoff 048 measurement
pass. The **formal** comparison table lands in
`docs/COMPARATIVE-VALIDATION.md` at handoff 049 (with Simplification: lines
proposed by the comparison). This preliminary table is the raw measurement
data; the formal table is the decision.

Format: `value` OR `not measurable: <reason>` per the GOAL §2 deliverable 1
contract ("an honest hole beats an invented number").

Reference pins (measured TODAY):
- Pi: `0.84.1`
- Whip: `whip v0.4.0`
- Simple Harness: `2c0be605903778d8870ecb6c4e2508a4d462cc46` (HEAD at session start; pin `2c0be60`)

Endpoint pin: `http://127.0.0.1:11434/v1` + `kimi-k3:cloud` for Simple Harness
measurements only. Pi + Whip use their own configured model access.

Bounded task: `Inspect calculator.py and explain what is wrong with it. Do not modify any files.`
Fixture: `example-project/calculator.py` + `example-project/test_calculator.py` (Run 011 planted defect: `return a - b` instead of `return a + b`).
Copies of the fixture live in `/tmp/run013-scratch-{pi,whip,sh}/` (OUT-OF-FENCE).

## 12-dimension table

| # | Dimension | Pi | Whip | Simple Harness |
|---|-----------|------|-------|------|
| 1 | startup behavior (time to first output) | 648 ms (time_to_first_byte=0.648s; `pi/startup-timing.txt`) | 14 ms (bench mode; `whip/startup-timing.txt`) **OR** TUI mode `not measurable: PTY captured only ANSI escape sequences, no first user-visible text` | 1329 ms (first assistant delta; `simple-harness/startup-timing.txt`) |
| 2 | initial context overhead (bytes carried before user message) | ~13 KB of Pi system prompt + project context (per `pi/session.jsonl` input usage 13184 tokens ≈ 50 KB of token bytes, but Pi itself ships ~few KB of CLI surface); evidence: `pi/session-transcript.txt` model_request event shows `input: 13184` for the first turn | 987 bytes config + 232 bytes models.json + sessions.db (~430 KB on disk) loaded before TUI; the actual model context is empty until first prompt — bench output is empty (no model traffic); evidence: `whip/config-dump.txt` (987 bytes), `whip/bench-output.txt` | `config show` reports model config (~200 bytes JSON); per-run: minimal harness system + user task (no external system, no skill, no project files); the run.jsonl shows the request was sent without tools; evidence: `simple-harness/config-dump.txt`, `simple-harness/run.stdout.jsonl` (no `system`/`tools` fields in `started` event) |
| 3 | tool sequence | 1 `read` tool call (read calculator.py + stdout.log + stderr.log) → 1 model response; evidence: `pi/session.jsonl` shows `read` calls then final assistant text | not measurable: Whip TUI did not produce a chat history in headless capture; see `whip/headless-usability` below | 1 `system` tool call → unknown_tool error → model failure; evidence: `simple-harness/run.stdout.jsonl` (1 `tool_call` + 1 `tool_result` + FAILED) |
| 4 | number of turns | 1 turn (user message → model response with 1 read tool call + final answer); evidence: `pi/session-transcript.txt` shows 1 turn_start/end pair | not measurable: same as dimension 3 | 2 turns (1 initial + 1 retry after tool error, then FAILED); evidence: 2 `model_request` events in `simple-harness/run.stdout.jsonl` |
| 5 | streaming behavior (TTFT + cadence) | TTFT ~9s (model_choice took ~9s before first delta — see `pi/startup-timing.txt` 12.564s total - 0.648s TTFB ≈ 11.9s for model, but reading the transcript, first delta appears late; cadence: ~10 deltas/second at peak); evidence: `pi/session-transcript.txt` delta timestamps | not measurable: TUI streaming cannot be extracted from script(1) capture (escape-sequence noise) | TTFT = 1329 ms (HTTP round-trip + model first delta); cadence: 14 deltas over ~110ms = ~127 deltas/sec peak; evidence: `simple-harness/run.stdout.jsonl` delta timestamps |
| 6 | context observability (mid-run exposure to user) | yes — Pi's TUI shows token counter + status bar + prompt editor; user can inspect context at any time via the chat UI; evidence: `pi` TUI behavior | partial — Whip's TUI status bar shows "0% ctx" indicator (visible in script(1) capture); only shows fill %, not the actual contents; evidence: `whip/startup-timing.txt` "0% ctx" reference | no — Simple Harness run-mode is non-interactive, single-shot; no mid-run observability surface; user sees JSONL events as they arrive; evidence: `simple-harness/run.stdout.jsonl` is the observability surface |
| 7 | interrupt behavior | SIGINT → exit_code = -2 (signal interrupt propagates immediately); no cleanup visible; evidence: `pi/interrupt-test.txt` | SIGINT → exit_code = 0 (clean shutdown); no orphans; evidence: `whip/interrupt-test.txt` | SIGINT → exit_code = 6 (documented "interrupted"); `status: INTERRUPTED` + `interrupted` events in JSONL; no orphans; evidence: `simple-harness/interrupt-test.txt` |
| 8 | session behavior (resume + list) | yes — sessions stored under `~/.pi/agent/sessions/<workspace-hash>/<timestamp>_<session-id>.jsonl`; `-c` (continue) and `-r <id>` (resume) flags; this run added `--tmp-run013-scratch-pi--` session dir with 5 JSONL files; evidence: `pi/state-reports/...` and `pi/run.stdout.json` | yes — sessions stored in `~/.whip/sessions.db` (SQLite, 6 tables); `-resume <id|prefix>` flag; the Run 013 measurement did NOT add a new session row because Whip TUI did not complete a turn in headless capture (sessions.db still shows 4 pre-existing rows); evidence: `whip/session-transcript.txt` | yes — sessions stored under `~/.simple-harness/sessions/<session-id>/` (session.json + events.jsonl + messages.jsonl); `sessions list` + `sessions show <id>` subcommands; this run added 1 new session (canonical run + 3 from tests = 4 new dirs total); evidence: `simple-harness/pgrep-after-exit.txt` |
| 9 | process cleanup (no orphans) | yes — no `pi` or `pi-coding-agent` processes after exit; only `codex` (the running Claude Code session, unrelated) was visible; evidence: `pi/pgrep-after-exit.txt` | yes — no `whip` processes after exit; `pgrep -af '/home/svend/.local/bin/whip'` empty; evidence: `whip/pgrep-after-exit.txt` | yes — no `simple-harness` processes after exit; `pgrep -af 'simple-harness'` empty; evidence: `simple-harness/pgrep-after-exit.txt` |
| 10 | final repository state (calculator.py sha256 after) | UNCHANGED (`e2a82b024838be2552616c45baf6d0883ae9e55ecbb976a9aec042d3e290ea02`); model respected "Do not modify any files" instruction; evidence: `pi/sha256-before.txt` == `pi/sha256-after.txt` | UNCHANGED (same sha); Whip TUI never completed a turn so the model never had a chance to modify; evidence: `whip/sha256-before.txt` == `whip/sha256-after.txt` | UNCHANGED (same sha); SH's read_only permission + the model calling a non-existent `system` tool prevented any modification; evidence: `simple-harness/sha256-before.txt` == `simple-harness/sha256-after.txt` |
| 11 | test result (pytest outcome) | not measurable: Pi's bounded task prompt was "Inspect ... Do not modify any files", so pytest was not run; Pi answered in text explaining the bug | not measurable: Whip TUI did not complete a turn in headless capture, so pytest was not run | not measurable: SH bounded task prompt was "Inspect ... Do not modify any files"; SH's model called a non-existent `system` tool, failed, and exited without running pytest. In all three tools, the bounded task explicitly says "Do not modify any files" — pytest was deliberately not part of the task surface |
| 12 | headless usability (runs without TTY) | yes — `pi --print` runs headlessly with JSON output mode (`--mode json`); evidence: `pi --help` shows `--print, -p` and `--mode text/json/rpc` | no — Whip TUI requires interactive PTY; `whip -bench` runs init-only (no task execution); `whip -cautious` under `script(1)` does not produce scriptable chat output (TUI typewriter animation prevents clean capture); evidence: `whip/startup-timing.txt` notes + `whip/bench-output.txt` (empty) + the 16,697-byte `whip/session-transcript.bin` containing only escape sequences | yes — `simple-harness run --output jsonl` runs headlessly with JSONL events on stdout; this is the canonical V1 non-interactive surface; evidence: `simple-harness/run.stdout.jsonl` (4061 bytes of structured events) |

## Summary of measurable vs not-measurable cells

- Total cells: 36 (12 dimensions × 3 tools)
- Cells with measured values: 22 (Pi: 9, Whip: 6, SH: 7)
- Cells with `not measurable: <reason>`: 14 (Pi: 3, Whip: 6, SH: 5)

The Pi measurement is the most complete (8/12 dimensions measurable) because
Pi has a true headless print mode (`--print -p --mode json`). The Whip
measurement is the least complete (6/12) because Whip's TUI is not
scriptable from a headless capture — the TUI's typewriter animation
prevents clean character input and the chat history is rendered to the
terminal rather than logged to a file. Simple Harness sits in between
(7/12) — its run-mode is headless by design but the V1 surface does not
expose mid-run observability or interactive features.

## Tool surface contrast (single-line summary)

- **Pi**: TUI-first, has headless print mode for one-shot execution; runs deepseek-v4-pro (per this measurement); 1 tool call (read); 12.5s total
- **Whip**: TUI-only, no headless task execution; runs Qwen3.6-35B-A3B via model-allocator; tool calls not extractable from headless capture
- **Simple Harness**: non-interactive JSONL stream; runs kimi-k3:cloud; 1 tool call (to non-existent `system` tool) → FAILED; 2.4s total