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

# Run 018 / handoff 044 — live acceptance runner body.
# (i) Temp workspace setup: copy the fixture into a temp workspace via
# mktemp -d + cp -r. The pristine source remains at <project_root>/example-project/
# for the diff. WORKSPACE_DIR_OVERRIDE is the binding-pin anchor (when
# set, the binding pin owns the dir's lifecycle + can pre-compute the
# absolute path the model's tool-call must patch); when unset, the
# runner creates + owns a fresh mktemp dir via the cleanup trap.
trap 'rm -rf "$WORKSPACE"' EXIT
if [ -n "${WORKSPACE_DIR_OVERRIDE:-}" ]; then
    WORKSPACE="$WORKSPACE_DIR_OVERRIDE"
    mkdir -p "$WORKSPACE"
else
    WORKSPACE=$(mktemp -d -t e2e-coding.XXXXXX)
fi
cp -r example-project/. "$WORKSPACE/"

# (ii) Prompt file: small static instruction matching the Run 011 fixture defect.
printf '%s' 'Find and fix the defect. Run the tests afterward.' > "$WORKSPACE/prompt.md"

# (iv) 3-attempt retry loop. Each attempt re-copies the fixture into the
# workspace + invokes simple-harness run against it + asserts the runner's
# happy-path evidence (diff + pytest + tool_call/tool_result events +
# session id). 0/3 success is a FAILED criterion per GOAL §2.
for attempt in 1 2 3; do
    cp -r example-project/. "$WORKSPACE/"
    rm -f "$WORKSPACE/run.$attempt.jsonl" "$WORKSPACE/run.$attempt.err"
    set +e
    simple-harness run \
        --base-url "$BASE_URL" \
        --model "$MODEL" \
        --workspace "$WORKSPACE" \
        --permission workspace_write \
        --output jsonl \
        --prompt-file "$WORKSPACE/prompt.md" \
        --max-turns 8 \
        > "$WORKSPACE/run.$attempt.jsonl" 2> "$WORKSPACE/run.$attempt.err"
    run_exit=$?
    set -e

    FAIL_REASON=""
    # (A) Content-based diff assertion: workspace file must differ from pristine.
    # diff returns 0 if files are IDENTICAL, 1 if they DIFFER. The script
    # wants the workspace to have been patched, so a same-content outcome
    # is a FAIL: files identical -> "did not change".
    if diff -q example-project/calculator.py "$WORKSPACE/calculator.py" >/dev/null 2>&1; then
        FAIL_REASON="(A) calculator.py did not change (run_exit=$run_exit)"
    # (B) Pytest passes post-patch.
    elif ! python3 -m pytest "$WORKSPACE" -q >/dev/null 2>&1; then
        FAIL_REASON="(B) pytest failed post-patch"
    # (C) JSONL carries tool_call + tool_result events. The emitter
    # emits compact JSON ("event":"tool_call") with no space after
    # the colon, so the grep pattern matches the exact compact form.
    elif ! grep -q '"event":"tool_call"' "$WORKSPACE/run.$attempt.jsonl"; then
        FAIL_REASON="(C) JSONL missing tool_call event"
    elif ! grep -q '"event":"tool_result"' "$WORKSPACE/run.$attempt.jsonl"; then
        FAIL_REASON="(C) JSONL missing tool_result event"
    # (D) Session id extraction from started event.
    elif ! SESSION_ID=$(jq -r 'select(.event == "started") | .session_id' "$WORKSPACE/run.$attempt.jsonl" | head -1) || [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
        FAIL_REASON="(D) session id not extractable from JSONL"
    fi

    if [ -z "$FAIL_REASON" ]; then
        echo "attempt $attempt: PASS — session_id=$SESSION_ID" >&2
        exit 0
    fi
    echo "attempt $attempt: FAIL — $FAIL_REASON" >&2
done

# (v) Final disposition: 0/3 attempts passing is a FAILED criterion per GOAL §2.
echo "0/3 attempts passed; FAILED criterion per GOAL §2" >&2
exit 1