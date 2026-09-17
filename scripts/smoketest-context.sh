#!/usr/bin/env bash
# Bounded Context Lifecycle — end-to-end smoke test against the built binary.
#
# The unit tests prove the pieces behave. This drives the command-line surface
# a user actually touches, one check per acceptance criterion, and reports PASS
# or FAIL for each. It needs no model: scripts/stub-endpoint.py answers the
# requests, which is what makes it runnable anywhere and in CI.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO="$(pwd)"
TMP="$(mktemp -d)"
BIN="$TMP/simple-harness"
WORK="$TMP/work"
mkdir -p "$WORK"
STUB_PID=""
cleanup() { [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT

pass=0
fail=0
check() { # check <criterion> <description> <script>
    local n="$1" desc="$2" script="$3"
    if out="$(bash -c "$script" 2>&1)"; then
        printf '  PASS  %2s. %s\n' "$n" "$desc"
        pass=$((pass + 1))
    else
        printf '  FAIL  %2s. %s\n' "$n" "$desc"
        printf '%s\n' "$out" | sed 's/^/        /' | head -6
        fail=$((fail + 1))
    fi
}

echo "Bounded Context Lifecycle smoke test — $(date '+%Y-%m-%d %H:%M:%S')"
go build -o "$BIN" ./cmd/simple-harness
echo "built $("$BIN" --version)"

python3 scripts/stub-endpoint.py "$WORK/port" &
STUB_PID=$!
for _ in $(seq 1 60); do [ -s "$WORK/port" ] && break; sleep 0.1; done
[ -s "$WORK/port" ] || { echo "the stub endpoint did not start" >&2; exit 1; }
STUB="http://127.0.0.1:$(cat "$WORK/port")"
echo "stub endpoint at $STUB"
echo

printf 'summarise the repository and list its packages\n' >"$WORK/prompt.txt"
head -c 200000 /dev/zero | tr '\0' 'a' >"$WORK/huge.txt"
head -c 6000 /dev/zero | tr '\0' 'b' >"$WORK/big-prompt.txt"

SHOW="$BIN context show --base-url $STUB --model stub --workspace $WORK --prompt-file $WORK/prompt.txt"

check 1 "the complete active context is accounted for" "
    out=\$($SHOW --context-limit 131072)
    grep -q 'Active context:' <<<\"\$out\" &&
    grep -q 'Tool schemas:'   <<<\"\$out\" &&
    grep -q 'Pinned:'         <<<\"\$out\""

check 2 "a safe budget is derived from the model limit" "
    out=\$($SHOW --context-limit 131072)
    grep -q 'Model context limit: *131072' <<<\"\$out\" &&
    grep -q 'Generation reserve:'          <<<\"\$out\" &&
    grep -q 'Safety reserve:'              <<<\"\$out\" &&
    budget=\$(grep 'Active input budget:' <<<\"\$out\" | tr -dc 0-9)
    [ \"\$budget\" -gt 0 ] && [ \"\$budget\" -lt 131072 ]"

check 5 "an unknown limit is reported rather than guessed" "
    out=\$($SHOW)
    grep -q 'unknown' <<<\"\$out\" && grep -q 'not bounded' <<<\"\$out\""

check 7 "the lifecycle counters are observable" "
    out=\$($SHOW --context-limit 131072)
    grep -q 'Tool results pruned:' <<<\"\$out\" &&
    grep -q 'Compactions:'         <<<\"\$out\" &&
    grep -q 'Peak active context:' <<<\"\$out\" &&
    grep -q 'Budget utilization:'  <<<\"\$out\""

check 21 "oversized pinned context fails explicitly, naming the cause" "
    out=\$($BIN run --base-url $STUB --model stub --workspace $WORK \
        --prompt-file $WORK/prompt.txt --system-file $WORK/huge.txt \
        --context-limit 2048 2>&1); rc=\$?
    [ \$rc -eq 2 ] &&
    grep -q 'pinned context' <<<\"\$out\" &&
    grep -q 'exceeds the active budget' <<<\"\$out\""

check 9 "--limit keeps its own meaning and its own exit code" "
    $BIN run --base-url $STUB --model stub --workspace $WORK \
        --prompt-file $WORK/big-prompt.txt --limit 100 >/dev/null 2>&1; rc=\$?
    [ \$rc -eq 2 ]"

check 3 "a bounded run against a real endpoint completes" "
    $BIN run --base-url $STUB --model stub --workspace $WORK \
        --prompt-file $WORK/prompt.txt --context-limit 131072 >/dev/null 2>&1"

check 17 "config: unbounded must be asked for by name" "
    cd $REPO && go test ./internal/config/ -run 'Unbounded|Typo|Nothing' -count=1"

check 24 "every validation test in the addendum's list passes" "
    cd $REPO && go test ./internal/ctxlife/ -count=1"

check 12 "a 40-turn run stays inside its budget; an unbounded one does not" "
    cd $REPO && go test ./internal/loop/ -run 'LongRun|Lifecycle|Durable|Pinned' -count=1"

check 10 "no duplicate context subsystem was added elsewhere" "
    cd $REPO
    ! grep -rn 'compact\|prune\|token budget' --include='*.go' \
        internal/mcp internal/session 2>/dev/null | grep -qv '_test.go'"

check 22 "no semantic memory, vector store or retrieval was added" "
    cd $REPO
    ! grep -rniE 'embedding|vector (db|store)|leann|faiss|chromadb' --include='*.go' \
        internal/ctxlife internal/loop internal/config 2>/dev/null"

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
