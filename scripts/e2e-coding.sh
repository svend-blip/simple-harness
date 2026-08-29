#!/bin/bash
set -euo pipefail
# scripts/e2e-coding.sh — Simple Harness end-to-end coding agent
# acceptance runner (SCOPE §40). The acceptance runner validates its
# arguments in THIS handoff (handoff 039); the full body — temp-
# workspace setup + simple-harness run invocation + JSONL transcript
# capture + fixture-diff capture + session-id capture + 3-attempt retry
# logic + deterministic assertion sequence — lands in handoff 040 once
# the live model endpoint is reachable.
#
# Usage: scripts/e2e-coding.sh BASE_URL MODEL
#   BASE_URL  base URL of an OpenAI-compatible endpoint (required)
#   MODEL     model name to use (required)
#
# Exit codes (SCOPE §28):
#   0  acceptance passed (handoff 040 will gate this)
#   1  usage error (no / wrong args) — THIS handoff
#   2  configuration error (handoff 040 — endpoint unreachable)
#   3  model/API failure (handoff 040 — model returned an error)
#   6  interrupted (SIGINT/SIGTERM)
#
# The runner is NOT part of scripts/test.sh (it needs a live endpoint
# per GOAL §3). It is invoked manually at acceptance time.

if [ $# -ne 2 ]; then
    cat <<'EOF' >&2
usage: scripts/e2e-coding.sh BASE_URL MODEL
  BASE_URL  base URL of an OpenAI-compatible endpoint (required)
  MODEL     model name to use (required)
EOF
    exit 1
fi

BASE_URL="$1"
MODEL="$2"

# Handoff 040 will populate this block with:
#   1. Copy example-project/ to a temp workspace (t.TempDir equivalent)
#   2. Run simple-harness run --permission workspace_write --output jsonl
#      against the temp workspace
#   3. Capture the JSONL transcript + the fixture diff + the session id
#   4. Assert from events + exit code + workspace state: tests ran, a
#      patch/write landed, tests re-ran, final exit 0
#   5. Retry up to 3 attempts; 0/3 success is a FAILED criterion (not a
#      rewritten one) per GOAL §2

# Placeholder for handoff 040 — the body lands there. Exit 0 for now
# once args are validated; handoff 040 will replace this placeholder
# with the live acceptance logic that asserts the actual model behavior.
echo "scripts/e2e-coding.sh: args validated (BASE_URL=$BASE_URL, MODEL=$MODEL)" >&2
echo "scripts/e2e-coding.sh: handoff 039 stub; live acceptance body lands in handoff 040" >&2
exit 0