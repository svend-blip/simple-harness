#!/usr/bin/env bash
# scripts/contract-check.sh — Simple Harness V1 Public Contract
# conformance checker. Implements the 5 observable behaviors documented
# in docs/HARNESS-CONTRACT.md §"## Conformance-check anchors
# (cross-reference)" (line 1153):
#
#   (a) version flag            — exit 0 + stdout begins with "simple-harness "
#   (b) config-error exit 2     — exit 2 + stderr "config error: --base-url is required"
#   (c) unreachable endpoint    — exit 3 + valid JSONL with "started" + "completed(exit_code:3)"
#   (d) SIGTERM exit 6          — interrupt event + completed(exit_code:6); BEST-EFFORT, SKIPs gracefully
#   (e) session layout          — session.json + events.jsonl + messages.jsonl per the contract anchor
#
# Model-free black-box: no model server is started; assertion (c) uses
# the synthetic unreachable URL http://127.0.0.1:1/v1 (TCP-refused at
# the OS); assertion (d) is SKIPped unless CONTRACT_CHECK_LIVE_ENDPOINT
# points at a reachable endpoint.
#
# Exits 0 when every assertion PASSes or PASS+SKIPs (SKIP = "could not
# verify this assertion because of environment constraints, not a
# contract violation"); exits 1 when any assertion FAILs with a stderr
# diagnostic. Stdout is reserved (silent on success); stderr carries
# pass/fail/skip diagnostics + the summary.
#
# State is REPORTED, not cleaned silently. Each run gets a fresh
# scratch state-dir under /tmp/simple-harness-contract-check-<pid>-<ts>/
# so concurrent runs do not share state and retries are idempotent.
# Set CONTRACT_CHECK_CLEAN_SESSIONS=1 to opt-in to removing the scratch
# state-dir at end of run.

set -euo pipefail

# --- Configuration (overridable via env vars for CI) ----------------
: "${SIMPLE_HARNESS_BIN:=/home/svend/simple-harness/bin/simple-harness}"
: "${CONTRACT_CHECK_STATE_DIR:=/tmp/simple-harness-contract-check-$$-$(date +%s)}"
: "${CONTRACT_CHECK_ENDPOINT:=http://127.0.0.1:1/v1}"   # TCP-refused synthetic URL
: "${CONTRACT_CHECK_MODEL:=kimi-k3:cloud}"               # config-valid; never contacted under TCP-refused
: "${CONTRACT_CHECK_WORKSPACE:=/tmp/simple-harness-contract-check-workspace-$$}"
: "${CONTRACT_CHECK_SKIP_SIGTERM:=0}"                    # set 1 to skip assertion (d) entirely
: "${CONTRACT_CHECK_CLEAN_SESSIONS:=0}"                  # set 1 to auto-remove scratch state-dir on exit

# --- Helper: per-assertion result tracking --------------------------
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0

record_pass() { echo "[PASS] $1" >&2; PASS_COUNT=$((PASS_COUNT + 1)); }
record_fail() { echo "[FAIL] $1: $2" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }
record_skip() { echo "[SKIP] $1: $2" >&2; SKIP_COUNT=$((SKIP_COUNT + 1)); }

# --- Per-harness-invocation scratch dir setup (idempotent) -----------
mkdir -p "$CONTRACT_CHECK_STATE_DIR"
mkdir -p "$CONTRACT_CHECK_WORKSPACE"

# --- Assertion (a): version flag ------------------------------------
# Document claim (§probe): --version exits 0 + stdout begins with
# "simple-harness ". Source: cmd/simple-harness/main.go:88 (Version
# literal) + cmd/simple-harness/run.go:252-255 (--version short-circuit).
assert_version_flag() {
  local out ec
  out=$("$SIMPLE_HARNESS_BIN" --version 2>&1) && ec=0 || ec=$?
  if [[ $ec -eq 0 ]] && [[ "$out" == simple-harness\ * ]]; then
    record_pass "(a) version flag"
  else
    record_fail "(a) version flag" "exit=$ec, stdout=$out (expected exit 0 + stdout begins with 'simple-harness ')"
  fi
}

# --- Assertion (b): config-error exit 2 -----------------------------
# Document claim (§prepare): empty --base-url emits "config error:
# --base-url is required" to stderr + exits 2. Source:
# cmd/simple-harness/run.go:259-262. NOTE: --prompt-file is not passed
# here because the --base-url empty check at run.go:259-262 runs BEFORE
# the --prompt-file check at run.go:393-402; the config-error path
# exits 2 deterministically.
assert_config_error_exit_2() {
  local err ec
  err=$("$SIMPLE_HARNESS_BIN" run --base-url "" \
    --model "$CONTRACT_CHECK_MODEL" \
    --workspace "$CONTRACT_CHECK_WORKSPACE" \
    --permission workspace_write \
    --output jsonl 2>&1 >/dev/null) || ec=$?
  ec=${ec:-0}
  if [[ $ec -eq 2 ]] && [[ "$err" == *"config error: --base-url is required"* ]]; then
    record_pass "(b) config-error exit 2"
  else
    record_fail "(b) config-error exit 2" "exit=$ec (expected 2), stderr='$err'"
  fi
}

# --- Assertion (c): unreachable-endpoint exit 3 with valid JSONL -----
# Document claim (§start): synthetic URL http://127.0.0.1:1/v1
# (TCP-refused at the OS — no model server is started) emits started +
# completed(exit_code:3), every JSONL line parseable.
# Source: internal/event/event.go (8 event types + Completed/Interrupted
# methods) + cmd/simple-harness/run.go exit-code mapping.
assert_unreachable_endpoint() {
  local ec jsonl
  jsonl="$CONTRACT_CHECK_STATE_DIR/c.jsonl"
  set +e
  "$SIMPLE_HARNESS_BIN" run \
    --base-url "$CONTRACT_CHECK_ENDPOINT" \
    --model "$CONTRACT_CHECK_MODEL" \
    --workspace "$CONTRACT_CHECK_WORKSPACE" \
    --permission workspace_write \
    --prompt-file "$CONTRACT_CHECK_STATE_DIR/ping-prompt.txt" \
    --output jsonl \
    --state-dir "$CONTRACT_CHECK_STATE_DIR" \
    --max-turns 1 \
    > "$jsonl" 2>&1
  ec=$?
  set -e
  if [[ $ec -ne 3 ]]; then
    record_fail "(c) unreachable-endpoint exit 3" "exit=$ec (expected 3); jsonl first 5 lines: $(head -5 "$jsonl" 2>/dev/null || echo '(none)')"
    return
  fi
  local nonempty parseable has_started has_completed_3
  nonempty=$(test -s "$jsonl" && echo "yes" || echo "no")
  parseable=$(python3 -c "
import json, sys
ok = True
with open('$jsonl') as f:
    for ln_no, ln in enumerate(f, 1):
        ln = ln.strip()
        if not ln: continue
        try:
            json.loads(ln)
        except json.JSONDecodeError as e:
            print(f'line {ln_no}: {e}')
            ok = False
            break
sys.exit(0 if ok else 1)
" 2>&1 && echo "yes" || echo "no")
  has_started=$(grep -q '"event":"started"' "$jsonl" 2>/dev/null && echo "yes" || echo "no")
  has_completed_3=$(grep -q '"event":"completed"' "$jsonl" 2>/dev/null && grep -q '"exit_code":3' "$jsonl" 2>/dev/null && echo "yes" || echo "no")
  if [[ "$nonempty" == "yes" && "$parseable" == "yes" && "$has_started" == "yes" && "$has_completed_3" == "yes" ]]; then
    record_pass "(c) unreachable-endpoint exit 3 with valid JSONL"
  else
    record_fail "(c) unreachable-endpoint exit 3" "nonempty=$nonempty, parseable=$parseable, has_started=$has_started, has_completed_3=$has_completed_3"
  fi
}

# --- Assertion (d): SIGTERM exit 6 with interrupted event (BEST-EFFORT)
# Document claim (§interrupt): harness exits within 5s after SIGTERM +
# sidecar JSONL has "interrupted" event + terminal event has
# exit_code:6. Best-effort — SKIP gracefully when no live endpoint is
# configured (CONTRACT_CHECK_LIVE_ENDPOINT) or the endpoint is not
# reachable. The script does NOT exit 1 on SKIP.
assert_sigterm_exit_6() {
  if [[ "${CONTRACT_CHECK_SKIP_SIGTERM:-0}" == "1" ]]; then
    record_skip "(d) SIGTERM exit 6" "CONTRACT_CHECK_SKIP_SIGTERM=1 set explicitly"
    return
  fi
  local live_endpoint="${CONTRACT_CHECK_LIVE_ENDPOINT:-}"
  if [[ -z "$live_endpoint" ]]; then
    record_skip "(d) SIGTERM exit 6" "CONTRACT_CHECK_LIVE_ENDPOINT not set (no live model endpoint available)"
    return
  fi
  if ! curl -sf --max-time 1 "$live_endpoint" >/dev/null 2>&1; then
    record_skip "(d) SIGTERM exit 6" "live endpoint $live_endpoint unreachable (TCP-refused or timeout)"
    return
  fi
  local launch_jsonl="$CONTRACT_CHECK_STATE_DIR/d.jsonl"
  local long_prompt="$CONTRACT_CHECK_STATE_DIR/long-prompt.txt"
  # Write a long prompt that will keep the harness busy long enough
  # to SIGTERM mid-flight (the harness streams the model response,
  # so even a short model call is observable mid-flight).
  python3 -c "print('ping ' * 200)" > "$long_prompt"
  set +e
  "$SIMPLE_HARNESS_BIN" run \
    --base-url "$live_endpoint" \
    --model "$CONTRACT_CHECK_MODEL" \
    --workspace "$CONTRACT_CHECK_WORKSPACE" \
    --permission workspace_write \
    --output jsonl \
    --state-dir "$CONTRACT_CHECK_STATE_DIR" \
    --prompt-file "$long_prompt" \
    > "$launch_jsonl" 2>&1 &
  local pid=$!
  sleep 2.5
  kill -TERM "$pid" 2>/dev/null || true
  local waited=0
  while kill -0 "$pid" 2>/dev/null && [[ $waited -lt 50 ]]; do
    sleep 0.1
    waited=$((waited + 1))
  done
  wait "$pid" 2>/dev/null
  local ec=$?
  set -e
  local session_dir
  session_dir=$(ls -t "$CONTRACT_CHECK_STATE_DIR" 2>/dev/null | head -1 || true)
  if [[ -z "$session_dir" || ! -d "$CONTRACT_CHECK_STATE_DIR/$session_dir" ]]; then
    record_skip "(d) SIGTERM exit 6" "no session dir created"
    return
  fi
  local events="$CONTRACT_CHECK_STATE_DIR/$session_dir/events.jsonl"
  if [[ ! -f "$events" ]]; then
    record_skip "(d) SIGTERM exit 6" "no events.jsonl sidecar"
    return
  fi
  local has_interrupted exit_6
  has_interrupted=$(grep -q '"event":"interrupted"' "$events" 2>/dev/null && echo "yes" || echo "no")
  exit_6=$(grep -q '"event":"completed"' "$events" 2>/dev/null && grep -q '"exit_code":6' "$events" 2>/dev/null && echo "yes" || echo "no")
  if [[ "$has_interrupted" == "yes" && "$exit_6" == "yes" ]]; then
    record_pass "(d) SIGTERM exit 6 with interrupted event"
  else
    record_fail "(d) SIGTERM exit 6 with interrupted event" "interrupted=$has_interrupted, exit_code:6=$exit_6, harness exit=$ec"
  fi
}

# --- Assertion (e): session layout ----------------------------------
# Document claim (§collect): each session dir has session.json (valid
# JSON carrying top-level session_id + started_at + status + exit_code
# + events_path, plus the nested config sub-object with base_url +
# model + workspace + permission) + events.jsonl (>=1 line, every line
# valid JSON) + messages.jsonl (non-empty). Source:
# internal/session/writer.go + internal/session/session.go (the Session
# and Config structs).
assert_session_layout() {
  local sd="$CONTRACT_CHECK_STATE_DIR"
  local sessions
  sessions=$(find "$sd" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)
  if [[ "$sessions" -lt 1 ]]; then
    record_fail "(e) session layout" "no session dirs under $sd"
    return
  fi
  local all_ok=true
  while IFS= read -r dir; do
    [[ -f "$dir/session.json" ]] || { all_ok=false; echo "  MISSING session.json in $dir" >&2; continue; }
    [[ -f "$dir/events.jsonl" ]] || { all_ok=false; echo "  MISSING events.jsonl in $dir" >&2; continue; }
    [[ -f "$dir/messages.jsonl" ]] || { all_ok=false; echo "  MISSING messages.jsonl in $dir" >&2; continue; }
    # Verify session.json is valid JSON with the required top-level
    # identity-card fields + the nested `config` sub-object per the
    # contract document's anchor table (docs/HARNESS-CONTRACT.md:1166).
    # The top-level fields are session_id + started_at + status +
    # exit_code + events_path + the nested `config` itself; the nested
    # `config` must be a dict containing base_url + model + workspace +
    # permission. Source: internal/session/session.go:37-43 (Config
    # struct) + :49-57 (Session struct) + internal/session/writer.go
    # :94-118 (Writer.Write).
    python3 -c "
import json, sys
required_top = ['session_id', 'started_at', 'status', 'exit_code', 'events_path', 'config']
required_nested = ['base_url', 'model', 'workspace', 'permission']
with open('$dir/session.json') as f:
    cfg = json.load(f)
missing_top = [k for k in required_top if k not in cfg]
if missing_top:
    print(f'  missing top-level {missing_top} in $dir/session.json')
    sys.exit(1)
if not isinstance(cfg.get('config'), dict):
    print(f'  config is not a dict in $dir/session.json')
    sys.exit(1)
missing_nested = [k for k in required_nested if k not in cfg['config']]
if missing_nested:
    print(f'  missing config.{missing_nested} in $dir/session.json')
    sys.exit(1)
" 2>&1 || { all_ok=false; continue; }
    [[ -s "$dir/events.jsonl" ]] || { all_ok=false; echo "  EMPTY events.jsonl in $dir" >&2; continue; }
    python3 -c "
import json, sys
with open('$dir/events.jsonl') as f:
    for ln_no, ln in enumerate(f, 1):
        ln = ln.strip()
        if not ln: continue
        try:
            json.loads(ln)
        except json.JSONDecodeError as e:
            print(f'  invalid JSON in events.jsonl line {ln_no}: {e}')
            sys.exit(1)
" 2>&1 || { all_ok=false; continue; }
    [[ -s "$dir/messages.jsonl" ]] || { all_ok=false; echo "  EMPTY messages.jsonl in $dir" >&2; continue; }
  done < <(find "$sd" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)
  if [[ "$all_ok" == "true" ]]; then
    record_pass "(e) session layout ($sessions session dir(s))"
  else
    record_fail "(e) session layout" "one or more session dirs failed layout checks under $sd"
  fi
}

# --- Pre-run: stage the prompt file for assertion (c) ----------------
echo "contract-check ping" > "$CONTRACT_CHECK_STATE_DIR/ping-prompt.txt"

# --- Main: run all 5 assertions in order ----------------------------
echo "=== scripts/contract-check.sh ===" >&2
echo "Target: $SIMPLE_HARNESS_BIN" >&2
echo "State dir: $CONTRACT_CHECK_STATE_DIR" >&2
echo "Workspace: $CONTRACT_CHECK_WORKSPACE" >&2
echo "" >&2

assert_version_flag
assert_config_error_exit_2
assert_unreachable_endpoint
assert_sigterm_exit_6
assert_session_layout

# --- Report session layout summary ----------------------------------
echo "" >&2
echo "=== Test session layout report ===" >&2
local_sessions=$(find "$CONTRACT_CHECK_STATE_DIR" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)
echo "Test sessions under scratch state-dir: $local_sessions" >&2
if [[ "$local_sessions" -gt 0 ]]; then
  echo "Session dirs:" >&2
  find "$CONTRACT_CHECK_STATE_DIR" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | while IFS= read -r d; do
    echo "  $d" >&2
    echo "    $(ls -1 "$d" | tr '\n' ' ')" >&2
  done
fi

echo "" >&2
echo "=== Summary: PASS=$PASS_COUNT FAIL=$FAIL_COUNT SKIP=$SKIP_COUNT ===" >&2
echo "State REPORTED (not cleaned): $CONTRACT_CHECK_STATE_DIR" >&2
echo "Set CONTRACT_CHECK_CLEAN_SESSIONS=1 to clean scratch state-dir on exit." >&2

# --- Optional cleanup ------------------------------------------------
if [[ "${CONTRACT_CHECK_CLEAN_SESSIONS:-0}" == "1" ]]; then
  rm -rf "$CONTRACT_CHECK_STATE_DIR" "$CONTRACT_CHECK_WORKSPACE" 2>/dev/null || true
  echo "Scratch state-dir cleaned (CONTRACT_CHECK_CLEAN_SESSIONS=1)." >&2
fi

# Exit 0 when every PASS-or-SKIP assertion holds; exit 1 if any FAIL.
if [[ $FAIL_COUNT -gt 0 ]]; then
  exit 1
else
  exit 0
fi
