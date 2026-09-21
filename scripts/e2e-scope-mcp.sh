#!/usr/bin/env bash
# Live scope-mcp round trip (audit §19): the real scope-mcp server on
# stdio, a scripted model, no GPU. scope-mcp is built on the reference
# TypeScript SDK, which is stricter on the wire than the Python servers
# the harness was first proven against.
#
#   scripts/e2e-scope-mcp.sh <path-to-scope-mcp/src/server.js>
#
# Proves, from the JSONL stream and the recorded model requests:
#   1. discovery and listing against the real server
#   2. the model is shown the server's full schema (item shapes, enums)
#   3. calls and results across five tool rounds, correctly paired
#   4. a server-side error (isError) reaches the model as a tool failure
#   5. a schema violation is rejected by the harness before the server
#   6. state written in one harness process is read back by the next
#   7. the server runs in the workspace even though the harness is
#      launched from elsewhere (scope-mcp keeps its state under its cwd)
#
# Not part of scripts/test.sh: it needs node and a scope-mcp checkout.
set -euo pipefail

SERVER="${1:?usage: e2e-scope-mcp.sh <path-to-scope-mcp/src/server.js>}"
[ -f "$SERVER" ] || { echo "e2e-scope-mcp: no server at $SERVER" >&2; exit 2; }
cd "$(dirname "$0")/.."
ROOT="$PWD"

# Runs on Windows too (Git Bash, as in CI): python may be `python`, a built
# binary needs its .exe, the harness finds its user config through
# USERPROFILE rather than HOME, and a path handed to a native program inside
# a file or a variable must be a Windows path - only arguments are translated.
PY="$(command -v python3 || command -v python)"
EXE="$(go env GOEXE)"
TMP="$(mktemp -d)"
if command -v cygpath >/dev/null 2>&1; then
    TMP="$(cygpath -m "$TMP")"
    SERVER="$(cygpath -m "$SERVER")"
fi
MOCK_PID=""
cleanup() {
    [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

BIN="$TMP/simple-harness$EXE"
go build -o "$BIN" ./cmd/simple-harness

WS="$TMP/ws"
# The declaration lives in the user config: the project config is found
# upward from the harness's cwd, and the harness is launched from
# $TMP/elsewhere on purpose.
mkdir -p "$WS" "$TMP/home/.simple-harness" "$TMP/elsewhere"
"$PY" - "$SERVER" >"$TMP/home/.simple-harness/config.json" <<'PY'
import json, sys
print(json.dumps({"mcp_servers": [{"name": "scope-mcp", "transport": "stdio",
      "command": ["node", sys.argv[1]], "permission": "workspace_write"}]}))
PY
echo "do the scope work" >"$WS/prompt.md"

run() { # run <label> <script.json>
    local label="$1" script="$2" rc=0
    "$PY" -u "$ROOT/scripts/scripted-model.py" "$TMP/$label.port" "$TMP/$label.req" "$script" &
    MOCK_PID=$!
    for _ in $(seq 1 25); do [ -f "$TMP/$label.port" ] && break; sleep 0.2; done
    (cd "$TMP/elsewhere" && HOME="$TMP/home" USERPROFILE="$TMP/home" "$BIN" run \
        --base-url "http://127.0.0.1:$(cat "$TMP/$label.port")/v1" --model scripted \
        --workspace "$WS" --permission workspace_write \
        --prompt-file "$WS/prompt.md" --output jsonl --max-turns 10 \
        >"$TMP/$label.jsonl" 2>"$TMP/$label.err") || rc=$?
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
    MOCK_PID=""
    if [ "$rc" -ne 0 ]; then
        echo "FAIL: $label exited $rc: $(tail -c 300 "$TMP/$label.err")" >&2
        exit 1
    fi
}

cat >"$TMP/s1.json" <<'JSON'
[ {"tool":"init_project","args":{"objective":"Round-trip proof"}},
  {"tool":"set_goals","args":{"goals":[{"id":"g1","title":"first"},{"id":"g2","title":"second","status":"pending"}]}},
  {"tool":"next_goal","args":{"goal_id":"does-not-exist"}},
  {"tool":"set_goals","args":{"goals":"not-an-array"}},
  {"tool":"status","args":{}},
  {"text":"done"} ]
JSON
cat >"$TMP/s2.json" <<'JSON'
[ {"tool":"status","args":{}}, {"text":"resumed"} ]
JSON

run r1 "$TMP/s1.json"
run r2 "$TMP/s2.json"
"$PY" "$ROOT/scripts/e2e-scope-mcp-check.py" "$TMP"
