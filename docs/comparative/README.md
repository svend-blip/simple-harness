# docs/comparative/ — Run 013 SCOPE §38 measurement evidence

This directory contains the raw evidence for the Pi / Whip / Simple
Harness comparative validation (SCOPE §38; Run 013 / handoff 048).
The formal comparison document lands in `docs/COMPARATIVE-VALIDATION.md`
at handoff 049; the files in this directory are the source of truth
that the document references.

Per the GOAL §3 fence (`docs/comparative/ (new, evidence only)`), all
files in this directory are RAW evidence: transcripts, timing captures,
session listings, sha256sum dumps, config dumps, state-change reports.
No source code, no documentation, no flag/event surface changes are
landed here.

## Index

| File / Dir | Purpose | Maps to dimension(s) |
|------------|---------|----------------------|
| `bounded-task-prompt.txt` | The exact prompt used for all 3 tools | n/a (shared input) |
| `bounded-task-fixture/calculator.py` | Copy of the Run 011 fixture used in scratch workspaces | dimension 10 (final repo state) |
| `bounded-task-fixture/test_calculator.py` | Copy of the fixture's test | dimension 11 (test result) |
| `pi/version.txt` | `0.84.1` — Pi package version | (reference pin) |
| `pi/config-dump.txt` | Pi's loaded config (provider + model) | dimension 2 (initial context overhead) |
| `pi/run.stdout.json` | Pi session log (JSONL-format) | dimensions 3, 4, 5 |
| `pi/session.jsonl` | Same as above (Pi's native log format) | dimensions 3, 4, 5 |
| `pi/session-transcript.txt` | Human-readable rendering of the Pi session log | dimensions 3, 4, 5 |
| `pi/startup-timing.txt` | time_to_first_byte + total_runtime | dimension 1 |
| `pi/interrupt-test.txt` | SIGINT response evidence | dimension 7 |
| `pi/pgrep-after-exit.txt` | process cleanup evidence | dimension 9 |
| `pi/run.stderr.txt` | stderr capture from Pi run | n/a (sanity) |
| `pi/sha256-before.txt` | workspace state before Pi run | dimension 10 |
| `pi/sha256-after.txt` | workspace state after Pi run | dimension 10 |
| `whip/version.txt` | `whip v0.4.0` | (reference pin) |
| `whip/config-dump.txt` | Whip's loaded config | dimension 2 |
| `whip/bench-output.txt` | `whip -bench` init-only mode output (empty) | dimension 1, 12 |
| `whip/session-transcript.txt` | Session inspection of `~/.whip/sessions.db` | dimension 8 |
| `whip/startup-timing.txt` | bench-mode init time + TUI-mode measurement | dimension 1 |
| `whip/interrupt-test.txt` | SIGINT response evidence | dimension 7 |
| `whip/pgrep-after-exit.txt` | process cleanup evidence | dimension 9 |
| `whip/sha256-before.txt` | workspace state before Whip run | dimension 10 |
| `whip/sha256-after.txt` | workspace state after Whip run | dimension 10 |
| `simple-harness/version.txt` | `simple-harness 0.1.0-dev` + HEAD pin | (reference pin) |
| `simple-harness/config-dump.txt` | Resolved config from `simple-harness config show` | dimension 2 |
| `simple-harness/run.stdout.jsonl` | The V1 protocol JSONL event stream | dimensions 1, 3, 4, 5 |
| `simple-harness/run.stderr.txt` | stderr capture (time + bash exit code) | n/a (sanity) |
| `simple-harness/startup-timing.txt` | Per-event timing analysis from the JSONL | dimension 1, 5 |
| `simple-harness/interrupt-test.txt` | SIGINT response evidence | dimension 7 |
| `simple-harness/pgrep-after-exit.txt` | process cleanup evidence | dimension 9 |
| `simple-harness/sha256-before.txt` | workspace state before SH run | dimension 10 |
| `simple-harness/sha256-after.txt` | workspace state after SH run | dimension 10 |
| `state-reports/whip-and-pi-BEFORE.txt` | `ls -la` capture of `~/.whip/` + `~/.pi/` before runs | dimension 8 |
| `state-reports/whip-and-pi-AFTER.txt` | `ls -la` capture of `~/.whip/` + `~/.pi/` after runs | dimension 8 |
| `state-reports/sh-BEFORE.txt` | `ls -la` capture of `~/.simple-harness/` before run | dimension 8 |
| `state-reports/sh-AFTER.txt` | `ls -la` capture of `~/.simple-harness/` after run | dimension 8 |
| `cross-tool/table.md` | The preliminary 12-dimension comparison table | (all 12 dimensions) |
| `cross-tool/observations.md` | Informal observations to seed handoff 049's prose | (analysis notes) |

## Bounded task

All three tools were given the same prompt against the same fixture:

```
Inspect calculator.py and explain what is wrong with it. Do not modify any files.
```

The fixture is the Run 011 planted defect:
```python
def add(a, b):
    # BUG: should be `return a + b`. Planted for the e2e slice.
    return a - b
```

with the canonical test:
```python
def test_add():
    assert add(2, 3) == 5
```

The scratch workspaces are at `/tmp/run013-scratch-{pi,whip,sh}/`. They
are OUT-OF-FENCE (outside the governed repo per the GOAL concurrent-flow
notice + the dispatch prompt's "Pi/Whip runs use scratch workspaces
outside every governed repository" binding).

## What is NOT here (and why)

- `docs/COMPARATIVE-VALIDATION.md` — FROZEN until handoff 049
- Source code changes — NONE permitted at this handoff; the
  comparison motivates simplifications, the simplifications land
  in handoff 050 conditionally
- Run-001 documents (`docs/RECON.md`, `docs/ADR-001-implementation-language.md`,
  `docs/ARCHITECTURE.md`) — FROZEN per GOAL §3 fence
- README.md — FROZEN per GOAL §3 fence
- `bin/simple-harness`, `internal/`, `cmd/`, `scripts/test.sh`,
  `go.mod`, `go.sum`, `.gitignore`, `share/`, `simple-harness` — FROZEN
- `example-project/` — FROZEN in-repo (the bounded task copies the
  fixture into `/tmp/run013-scratch-*/` for use there)

## Reproducibility

To re-run the bounded task against the three tools from this evidence:

```bash
# Setup scratch workspaces (OUT-OF-FENCE)
for tool in pi whip sh; do
    mkdir -p /tmp/run013-scratch-$tool
    cp example-project/calculator.py /tmp/run013-scratch-$tool/
    cp example-project/test_calculator.py /tmp/run013-scratch-$tool/
done

# Pi
cd /tmp/run013-scratch-pi && pi --print --mode json \
    "Inspect calculator.py and explain what is wrong with it. Do not modify any files."

# Whip (TUI-only; the headless capture from `script -q -c 'whip -cautious' /dev/null`
# is what produced the bench + interrupt evidence; the chat content is NOT scriptable)
cd /tmp/run013-scratch-whip && script -q -c 'whip -cautious' /dev/null

# Simple Harness
cd /tmp/run013-scratch-sh && simple-harness run \
    --base-url http://127.0.0.1:11434/v1 \
    --model kimi-k3:cloud \
    --workspace /tmp/run013-scratch-sh/ \
    --permission read_only \
    --output jsonl \
    --prompt-file docs/comparative/bounded-task-prompt.txt \
    --max-turns 8
```

(The Pi invocation above uses `--print --mode json` for headless capture;
the actual measurement pass used the same flags. The exact flags used
are documented per-file in the relevant evidence file.)

## Reference pins (measured TODAY)

| Tool | Pin | Value |
|------|-----|-------|
| Pi   | package version | `0.84.1` |
| Whip | `-version` output | `whip v0.4.0` |
| Simple Harness | git HEAD | `2c0be605903778d8870ecb6c4e2508a4d462cc46` |

All three pins were verified at session start per the dispatch prompt's
binding "Reference pins re-verified TODAY by the supervisor: pi 0.84.1
and whip v0.4.0 unchanged — TG3's bound pins are valid".