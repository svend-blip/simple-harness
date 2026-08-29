#!/bin/bash
set -euo pipefail
# scripts/e2e-review.sh — Simple Harness end-to-end read-only reviewer
# acceptance runner (SCOPE §41). The acceptance runner validates its
# arguments in THIS handoff (handoff 047); the full body — temp-
# workspace setup + simple-harness run invocation + JSONL transcript
# capture + workspace-fingerprint capture + session-id capture +
# 3-attempt retry logic + deterministic assertion sequence — lands
# in handoff 047 once the live model endpoint is reachable.
#
# Usage: scripts/e2e-review.sh BASE_URL MODEL
#   BASE_URL  base URL of an OpenAI-compatible endpoint (required)
#   MODEL     model name to use (required)
#
# Exit codes (SCOPE §28):
#   0  acceptance passed
#   1  usage error (no / wrong args)
#   2  configuration error (endpoint unreachable)
#   3  model/API failure (model returned an error)
#   6  interrupted (SIGINT/SIGTERM)
#
# The runner is NOT part of scripts/test.sh (it needs a live endpoint
# per GOAL §3). It is invoked manually at acceptance time.

if [ $# -ne 2 ]; then
    cat <<'EOF' >&2
usage: scripts/e2e-review.sh BASE_URL MODEL
  BASE_URL  base URL of an OpenAI-compatible endpoint (required)
  MODEL     model name to use (required)
EOF
    exit 1
fi

BASE_URL="$1"
MODEL="$2"

# Run 012 / handoff 047 — live review acceptance runner body.
# (i) Temp workspace setup: copy the fixture into a temp workspace via
# mktemp -d + cp -r. The pristine source remains at
# <project_root>/example-project/ for the after-fingerprint comparison.
# WORKSPACE_DIR_OVERRIDE is the binding-pin anchor (when set, the
# binding pin owns the dir's lifecycle + can pre-compute the absolute
# path the model's tool-call needs); when unset, the runner creates
# + owns a fresh mktemp dir via the cleanup trap. WORKSPACE_KEEP, when
# set, disables the cleanup trap so the binding pin can inspect the
# post-run workspace + transcripts after the script exits.
if [ -n "${WORKSPACE_KEEP:-}" ]; then
    trap -- EXIT
elif [ -n "${WORKSPACE_DIR_OVERRIDE:-}" ]; then
    trap -- EXIT
else
    trap 'rm -rf "$WORKSPACE"' EXIT
fi
if [ -n "${WORKSPACE_DIR_OVERRIDE:-}" ]; then
    WORKSPACE="$WORKSPACE_DIR_OVERRIDE"
    mkdir -p "$WORKSPACE"
else
    WORKSPACE=$(mktemp -d -t e2e-review.XXXXXX)
fi
rm -rf "$WORKSPACE"/*
cp -r example-project/. "$WORKSPACE/"

# (ii) Prompt file: small static instruction matching the Run 012 GOAL
# §1 role-neutrality contract. The model inspects the fixture, attempts
# mutations (which the perm layer rejects), and returns a review text
# without actually modifying any files. The "Do not modify any files"
# clause is informational only — the harness's perm layer is the actual
# enforcement (Run 005 READ_ONLY semantics).
printf '%s' 'Review the defect in calculator.py. Do not modify any files — the workspace is read-only.' > "$WORKSPACE/prompt.md"

# (iii) Before-fingerprint capture: directory-level SHA-256 of the
# fixture contents (NOT mtime). The runner-owned files (fingerprint.* +
# prompt.md + run.* artifacts + finding.txt) are excluded from the find
# so the fingerprint reflects only the fixture files; this is required
# because the runner writes fingerprint.after + run.* artifacts into
# the same workspace, and including them in the find would cause the
# before/after fingerprints to always differ (a false-positive mutation
# signal). The content-based pipeline matches GOAL §2's "computed before
# and after from file content, not mtimes" + GOAL §3's binding contract.
# The fingerprint.before file is written to /tmp (NOT to $WORKSPACE)
# because the runner's per-attempt `rm -rf $WORKSPACE/*` cleans the
# workspace between attempts; storing the file outside the workspace
# keeps the pre-run capture intact for the reviewer's audit.
FP_BEFORE_FILE=/tmp/e2e-review-fp-before.$$
BEFORE_FP=$(find "$WORKSPACE" -type f \
    -not -name 'fingerprint.before' \
    -not -name 'fingerprint.after' \
    -not -name 'prompt.md' \
    -not -name 'run.*.jsonl' \
    -not -name 'run.*.err' \
    -not -name 'finding.txt' \
    | sort | xargs sha256sum | sha256sum | awk '{print $1}')
printf '%s' "$BEFORE_FP" > "$FP_BEFORE_FILE"

# (v) 3-attempt retry loop. Each attempt re-copies the fixture into the
# workspace + invokes simple-harness run against it + asserts the
# runner's happy-path evidence (fingerprint unchanged + rejection
# evidence + review text + session id). 0/3 success is a FAILED
# criterion per GOAL §2.
for attempt in 1 2 3; do
    rm -rf "$WORKSPACE"/*
    cp -r example-project/. "$WORKSPACE/"
    printf '%s' 'Review the defect in calculator.py. Do not modify any files — the workspace is read-only.' > "$WORKSPACE/prompt.md"
    rm -f "$WORKSPACE/run.$attempt.jsonl" "$WORKSPACE/run.$attempt.err"
    set +e
    simple-harness run \
        --base-url "$BASE_URL" \
        --model "$MODEL" \
        --workspace "$WORKSPACE" \
        --permission read_only \
        --output jsonl \
        --prompt-file "$WORKSPACE/prompt.md" \
        --max-turns 8 \
        > "$WORKSPACE/run.$attempt.jsonl" 2> "$WORKSPACE/run.$attempt.err"
    run_exit=$?
    set -e

    FAIL_REASON=""
    REJECTION_FORM=""
    SESSION_ID=""

    # (A) Content-based fingerprint assertion: workspace file contents
    # must be UNCHANGED (zero mutation = SCOPE §41). Compute the
    # after-fingerprint with the same exclusion pattern as the before
    # fingerprint so the runner's own artifacts do not pollute the
    # comparison.
    AFTER_FP=$(find "$WORKSPACE" -type f \
        -not -name 'fingerprint.before' \
        -not -name 'fingerprint.after' \
        -not -name 'prompt.md' \
        -not -name 'run.*.jsonl' \
        -not -name 'run.*.err' \
        -not -name 'finding.txt' \
        | sort | xargs sha256sum | sha256sum | awk '{print $1}')
    # Mirror the before-fingerprint file into the workspace for the
    # reviewer's per-attempt audit (the rm -rf at the start of each
    # attempt cleans it from the workspace, but the canonical copy
    # stays in /tmp at $FP_BEFORE_FILE).
    printf '%s' "$AFTER_FP" > "$WORKSPACE/fingerprint.after"
    if [ "$AFTER_FP" != "$BEFORE_FP" ]; then
        FAIL_REASON="(A) workspace fingerprint changed — SCOPE §41 violation (before=$BEFORE_FP after=$AFTER_FP, run_exit=$run_exit)"
    else
        # (B) Rejection-evidence clause: Form 1 (rejected tool-call)
        # OR Form 2 (text statement). The harness emits tool_call
        # events for the attempted mutation; permission denials
        # surface as status:FAILED + completed(exit_code: 4) WITHOUT
        # a matching tool_result event (per handoff 042's
        # implementer-chosen semantic at internal/loop/loop.go:707).
        # Form 1 detection therefore requires the tool_call event
        # for a mutation tool AND a terminal rejection signal
        # (status:FAILED or completed(exit_code: 4)).
        #
        # The handoff's prescribed grep (tool_call + tool_result +
        # status=error + permission_denied) is the FIRST check; the
        # actual-harness-observable check (tool_call + status:FAILED
        # + completed exit_code:4) is the SECOND check. Either form
        # counts as Form 1 evidence.
        if grep -q '"event":"tool_call"' "$WORKSPACE/run.$attempt.jsonl" \
            && grep -q '"event":"tool_result"' "$WORKSPACE/run.$attempt.jsonl" \
            && grep -qE '"tool":"apply_patch"|"tool":"write_file"|"tool":"shell"' "$WORKSPACE/run.$attempt.jsonl" \
            && grep -q '"tool_result_status":"error"' "$WORKSPACE/run.$attempt.jsonl" \
            && grep -q 'permission_denied' "$WORKSPACE/run.$attempt.jsonl"; then
            REJECTION_FORM="rejected_tool_call"
        elif grep -qE '"tool":"apply_patch"|"tool":"write_file"|"tool":"shell"' "$WORKSPACE/run.$attempt.jsonl" \
            && { grep -q '"status":"FAILED"' "$WORKSPACE/run.$attempt.jsonl" \
                || grep -q '"exit_code":4' "$WORKSPACE/run.$attempt.jsonl"; }; then
            REJECTION_FORM="rejected_tool_call"
        # (B Form 2) Text-statement: assistant_stream event carries an
        # explicit "cannot modify" / "read-only" / "no write access" /
        # "permission denied" statement. Per the dispatch prompt's
        # known-model-behavior note ("kimi answers in text and may
        # never ATTEMPT a mutation"), Form 2 is the EXPECTED outcome
        # against the live kimi-k3:cloud model — the model returns a
        # review text rather than emitting apply_patch tool-calls.
        #
        # The grep concatenates ALL assistant_stream deltas into a
        # single text stream first (the model emits text as many small
        # per-token delta events; line-by-line grep would miss phrases
        # that span multiple events — verified against the live
        # kimi-k3:cloud response which splits "read-only" across two
        # separate assistant_stream events). jq falls back to a python3
        # pipeline if not available (the runner is stdlib-only POSIX
        # bash; jq is the recommended choice).
        elif grep -q '"event":"assistant_stream"' "$WORKSPACE/run.$attempt.jsonl"; then
            COMBINED_TEXT=$(jq -r 'select(.event == "assistant_stream") | .delta // ""' "$WORKSPACE/run.$attempt.jsonl" 2>/dev/null | tr -d '\n' || true)
            if [ -z "$COMBINED_TEXT" ]; then
                COMBINED_TEXT=$(python3 -c '
import json, sys
out = []
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "assistant_stream":
        out.append(ev.get("delta", "") or ev.get("content", "") or "")
sys.stdout.write("".join(out))
' < "$WORKSPACE/run.$attempt.jsonl" 2>/dev/null || true)
            fi
            if echo "$COMBINED_TEXT" | grep -qiE 'cannot modify|read[- ]only|no write access|permission denied'; then
                REJECTION_FORM="text_statement"
            else
                FAIL_REASON="(B) no rejection-evidence form found in JSONL (Form 2 text-statement phrases not detected in concatenated assistant_stream; run_exit=$run_exit)"
            fi
        else
            FAIL_REASON="(B) no rejection-evidence form found in JSONL (Form 1 + Form 2 both absent; run_exit=$run_exit)"
        fi

        # (C) Review-text clause: at least one assistant_stream event
        # with non-empty content. The "final review text" is the
        # LAST assistant_stream event's content; the binding pin may
        # assert on the presence of assistant_stream events +
        # non-empty content as the binding surface.
        if [ -z "$FAIL_REASON" ]; then
            if ! grep -q '"event":"assistant_stream"' "$WORKSPACE/run.$attempt.jsonl"; then
                FAIL_REASON="(C) no assistant_stream event in JSONL"
            elif ! grep -q '"event":"assistant_stream".*"content":"[^"]' "$WORKSPACE/run.$attempt.jsonl" \
                && ! grep -q '"event":"assistant_stream".*"delta":"[^"]' "$WORKSPACE/run.$attempt.jsonl"; then
                FAIL_REASON="(C) assistant_stream events present but no non-empty content/delta"
            fi
        fi

        # (D) Session-id extraction from the started event.
        if [ -z "$FAIL_REASON" ]; then
            SESSION_ID=$(jq -r 'select(.event == "started") | .session_id' "$WORKSPACE/run.$attempt.jsonl" | head -1 || true)
            if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
                FAIL_REASON="(D) session id not extractable from JSONL"
            fi
        fi
    fi

    # (vii) READ_ONLY-shell FINDING documentation. If the JSONL
    # shows the harness's shell builtin being rejected under
    # READ_ONLY (Run 005 semantics), the runner captures the
    # FINDING for the Human in a finding.txt file. The runner does
    # NOT loosen Run 005's semantics — the rejection IS the
    # deterministic-boundary evidence per GOAL §5 reviewer duty 2;
    # the finding is documentation, not a workaround.
    if grep -q '"tool":"shell"' "$WORKSPACE/run.$attempt.jsonl" \
        && { grep -q '"status":"FAILED"' "$WORKSPACE/run.$attempt.jsonl" \
            || grep -q '"exit_code":4' "$WORKSPACE/run.$attempt.jsonl" \
            || grep -q 'permission_denied' "$WORKSPACE/run.$attempt.jsonl"; }; then
        printf 'READ_ONLY-shell finding: shell builtin refused under READ_ONLY permission per Run 005 semantics. This is the deterministic-boundary evidence per SCOPE §41 + GOAL §5 reviewer duty 2. Per the dispatch prompt binding, this is a FINDING for the Human — evidence for a future read-only-command-allowlist decision. The runner does NOT loosen Run 005 semantics.\n' > "$WORKSPACE/finding.txt"
    fi

    if [ -z "$FAIL_REASON" ]; then
        echo "attempt $attempt: PASS — session_id=$SESSION_ID, rejection_form=$REJECTION_FORM" >&2
        exit 0
    fi
    echo "attempt $attempt: FAIL — $FAIL_REASON" >&2
done

# (vi) Final disposition: 0/3 attempts passing is a FAILED criterion per GOAL §2.
echo "0/3 attempts passed; FAILED criterion per GOAL §2" >&2
exit 1
