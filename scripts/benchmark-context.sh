#!/usr/bin/env bash
# Bounded Context Lifecycle — §25 benchmark against the baseline.
#
# Same local model, same runtime, same context limit, same workspace, same
# long-running task, twice: once with the lifecycle and once without. The
# only difference is --context-limit, which is the feature.
#
#   scripts/benchmark-context.sh <base-url> <model> [context-limit] [max-turns]
#
# Measures what §25 asks for and can be measured from outside the model:
# maximum active context, total input tokens, number of model calls, runtime,
# context-overflow failures, and whether the task completed. The reduction
# counters come from the harness's own telemetry.
set -euo pipefail

cd "$(dirname "$0")/.."
BASE="${1:?usage: benchmark-context.sh <base-url> <model> [limit] [turns]}"
MODEL="${2:?}"
LIMIT="${3:-16384}"
TURNS="${4:-12}"

TMP="$(mktemp -d)"
BIN="$TMP/simple-harness"
trap 'rm -rf "$TMP"' EXIT
go build -o "$BIN" ./cmd/simple-harness

WORK="$TMP/work"
mkdir -p "$WORK"
# A workspace with enough material that reading it fills a context.
cp -r internal "$WORK/internal" 2>/dev/null || true
cp README.md SCOPE.md "$WORK/" 2>/dev/null || true

cat >"$WORK/task.txt" <<'TASK'
Explore this workspace systematically. Read README.md, then SCOPE.md, then at
least six Go source files under internal/, one at a time, using the read_file
tool. After each file, state in one sentence what that file is responsible
for. When you have read at least eight files, write a final summary listing
every file you read and its responsibility.
TASK

run_arm() { # run_arm <label> <extra-flags...>
    local label="$1"; shift
    local sidecar="$TMP/$label.jsonl"
    local start end rc
    start=$(date +%s.%N)
    set +e
    "$BIN" run --base-url "$BASE" --model "$MODEL" --workspace "$WORK" \
        --prompt-file "$WORK/task.txt" --max-turns "$TURNS" \
        --output json "$@" >"$sidecar" 2>"$TMP/$label.err"
    rc=$?
    set -e
    end=$(date +%s.%N)
    python3 scripts/benchmark-report.py "$label" "$sidecar" "$rc" \
        "$(python3 -c "print(f'{$end-$start:.1f}')")" "$LIMIT"
}

echo "§25 benchmark — $(date '+%Y-%m-%d %H:%M:%S')"
echo "model $MODEL at $BASE, limit $LIMIT, max turns $TURNS"
echo
printf '%-12s%10s%12s%14s%10s%10s%9s\n' \
    arm calls "input tok" "max context" reduced runtime exit
run_arm bounded --context-limit "$LIMIT"
run_arm baseline
echo
echo "the arms differ only in --context-limit; everything else is identical"
