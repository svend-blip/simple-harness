# SCOPE §38 Pi/Whip/Simple Harness — informal observations

These are informal observations from the Run 013 / handoff 048 measurement
pass to seed handoff 049's prose. They are NOT the formal comparison
(which lands in `docs/COMPARATIVE-VALIDATION.md`); these are the
implementer's seat-of-the-pants notes on what the measurements revealed.

## What was surprising

### 1. The "text-only tool-call" hypothesis is partially wrong

The dispatch prompt for Run 013 stated:
> Simple Harness measurements use the same live endpoint as Runs 018 + 012
> (http://127.0.0.1:11434/v1, kimi-k3:cloud) and its known text-only
> tool-call behavior is itself a citable measurement, not a defect to hide

But the Run 013 measurement showed that kimi-k3:cloud DOES emit tool calls
against the OpenAI-compatible tool schema. The model emitted a `tool_call`
event for a tool named `system` (with arguments `{"command": "find . -name
\"calculator.py\" ..."}`), which Simple Harness correctly rejected because
no `system` tool is registered. This is NOT text-only behavior.

The Runs 018 + 012 evidence of "text-only" must have been specific to the
prompts they used. With the bounded task prompt "Inspect calculator.py and
explain what is wrong with it. Do not modify any files.", kimi-k3:cloud
chose to call a `find` tool instead of answering in text — and the tool it
called (`system`, presumably meant to be a bash/shell exec) does not exist
in Simple Harness's tool registry.

This is a real finding for the comparison: **the Simple Harness tool
registry has fewer tool names than kimi-k3:cloud was trained to expect**.
The model emits plausible-looking tool calls that SH can't dispatch. Pi's
tool registry (with a `read` tool) handled the same model output cleanly
because `read` is a name kimi-k3:cloud knows.

### 2. Whip's TUI is genuinely hard to script

The handoff said Whip's CLI surface is `-bench` + `-cautious` + `-m` +
`-p` + `-resume` + `-rod` + `-version`. There is no `-prompt` flag. The
TUI reads user input from the controlling TTY. Attempting to drive Whip
from `script -q -c 'whip -cautious' /dev/null < /tmp/whip-input.fifo`
produced a 16,697-byte transcript of pure ANSI escape sequences with no
extractable chat content.

The TUI typewriter animation animates each character as it's typed,
which means even with a fifo feeding input character-by-character, the
chat box echo doesn't match what a real human sees. The chat history
area (where model responses would appear) is rendered with
cursor-positioning escape codes that `script(1)` captures but tools
like `grep` can't parse.

For the comparative analysis this is itself a measurement: **Whip is a
TUI-first tool that does not have a headless task-execution surface**.
This is a meaningful design difference from Pi (which has `--print -p
--mode json`) and Simple Harness (which has `run --output jsonl`).

### 3. Pi's startup is faster than Simple Harness's

Pi's time-to-first-byte was 648 ms. Simple Harness's was 1329 ms. The
681 ms difference is the HTTP round-trip to `http://127.0.0.1:11434/v1`
+ kimi-k3:cloud's first-token latency. Pi does not have this network
round-trip — it routes the model call directly (in this measurement, to
deepseek-v4-pro over the openai-completions API; the latency in Pi's
case is dominated by the model's TTFT, not the HTTP request).

Whip's `-bench` mode (init only) is 14 ms, but that's not a fair
comparison — bench mode does no model work.

### 4. The "context overhead" dimension is not the right question

The handoff asks "bytes of context the tool carries before the user's
first message". For Simple Harness, the answer is approximately zero
because the run-mode does not load any project context (no skill, no
project files). For Pi, the answer is the Pi system prompt + the project
context it auto-loads (in this measurement, ~13 KB of input tokens =
~50 KB of token bytes, dominated by Pi's coding-assistant system prompt
+ the calculator.py + test_calculator.py content Pi auto-loaded as
project context).

The interesting comparison is NOT "how many bytes of context" but
"how much of that context is project-relevant". Simple Harness's
"carry zero context by default" is simpler but loses the auto-loaded
project context that Pi and Whip rely on. This is a design tradeoff,
not a measurement hole.

### 5. Pi and Whip both leave state in ~/.{pi,whip}/

Per the dispatch prompt's binding, state written to `~/.whip/` and
`~/.pi/` is REPORTED, not cleaned. The state changes observed:

- `~/.pi/agent/sessions/`: added new dir `--tmp-run013-scratch-pi--`
  containing 5 JSONL session files. The Pi measurement pass created
  one session that the model completed (deepseek-v4-pro answered the
  bounded task correctly).
- `~/.whip/`: no new session row in sessions.db (Whip TUI never
  completed a turn in headless capture). However, `~/.whip/browser/`
  gained a new subdirectory (`dedicated-profile/`) and `~/.whip/trusted.json`
  was updated to include `/tmp/run013-scratch-whip` as a trusted path.
- `~/.whip/whip.log`: appended config.load + mcp.ready entries for each
  Whip invocation (about 14 new lines from the measurement pass).
- `~/.simple-harness/sessions/`: added 4 new session dirs (one canonical
  + three from interrupt tests / re-runs). Each session is a self-contained
  JSONL stream + session.json.

The state changes are small in bytes but reveal different design
philosophies: Pi keeps raw event logs (human-readable JSONL), Whip
keeps a SQLite sessions.db (queryable but opaque), Simple Harness keeps
both event.jsonl + session.json (structured + replayable).

## What the comparison motivates (deletion-shaped simplifications)

This is for handoff 049 to formalize. Preliminary candidates based on
what the measurements revealed:

1. **Drop Simple Harness's `SkillConfig` field if it exists unused.**
   Pi and Whip do not load skill configs at startup; Simple Harness's
   run-mode does not use skills either (the bounded task did not pass
   `--skill`). If the SkillConfig code path is unused, it can go.
   (TO BE VERIFIED in handoff 049 by reading the Simple Harness source.)

2. **Consider whether SH's tool registry should be expanded to include
   the tool names kimi-k3:cloud is trained to emit (`read`, `write`,
   `bash`, `system`/`shell`).** This is a FEATURE addition, not a
   deletion, so it is FORBIDDEN per SCOPE §38. NOT a candidate.

3. **Drop the legacy `sessions.db-shm` and `sessions.db-wal` SQLite WAL
   files from `~/.whip/` if they accumulate across runs without value.**
   This is a Whip behavior, not a Simple Harness behavior, so it is
   NOT in Simple Harness's scope.

## Notes for handoff 049

- The 12-dimension table is in `cross-tool/table.md` (preliminary;
  re-format for the formal document).
- The reference pins need to be captured verbatim in the formal
  document: `0.84.1`, `whip v0.4.0`, `2c0be60`.
- The "Simplification:" lines must be DELETION-SHAPED ONLY per
  GOAL §2 deliverable 4 + SCOPE §38 ("feature additions to match
  Pi/Whip are forbidden").
- The session state-change reports show Simple Harness adds durable
  session dirs per run; if the planning loop considers this "extra
  Simple Harness complexity" it can propose deleting the
  per-session-messages.jsonl if it's never read.