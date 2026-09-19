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
cp README.md docs/SCOPE.md "$WORK/" 2>/dev/null || true

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
        --output jsonl "$@" >"$sidecar" 2>"$TMP/$label.err"
    rc=$?
    set -e
    end=$(date +%s.%N)
    python3 scripts/benchmark-report.py "$label" "$sidecar" "$rc" \
        "$(python3 -c "print(f'{$end-$start:.1f}')")" "$LIMIT"
    # Where the arm's wall time went: working turns against compaction
    # inferences, time to first token against generation.
    python3 scripts/benchmark-timing.py "$sidecar" >"$TMP/$label.timing" || true
    if [ "$rc" -ne 0 ]; then
        # A failed arm names its cause: the harness's stderr and the
        # last status it emitted, so the failure is a finding rather
        # than a number.
        printf '  %s stderr: %s\n' "$label" "$(tail -c 400 "$TMP/$label.err" | tr '\n' ' ')"
        printf '  %s last status: %s\n' "$label" \
            "$(grep '"event":"status"' "$sidecar" | tail -1 | cut -c1-300)"
    fi
}

echo "§25 benchmark — $(date '+%Y-%m-%d %H:%M:%S')"
echo "model $MODEL at $BASE, limit $LIMIT, max turns $TURNS"
echo
printf '%-12s%8s%9s%12s%14s%10s%10s%9s\n' \
    arm calls compact "input tok" "max context" reduced runtime exit
# A long prompt against a local model can take longer than the default
# request timeout; both arms get the same generous one.
export SIMPLE_HARNESS_REQUEST_TIMEOUT=900s
run_arm bounded --context-limit "$LIMIT"
# The baseline is the unbounded harness. Since the runtime probe (§5)
# would otherwise bound it at the served window, the lifecycle is
# switched off by name for this arm.
SIMPLE_HARNESS_CONTEXT_POLICY=unbounded run_arm baseline
for arm in bounded baseline; do
    echo
    echo "$arm — where the time went:"
    sed 's/^/  /' "$TMP/$arm.timing"
done
echo
echo "the arms differ only in the context lifecycle (bounded at $LIMIT vs off); everything else is identical"
