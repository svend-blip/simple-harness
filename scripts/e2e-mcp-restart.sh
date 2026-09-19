#!/usr/bin/env bash
# An MCP server restarted in the middle of a run: the harness must start a
# new session and carry on. The server is a minimal one on the reference
# Python SDK (the SDK mcp-light is built on), whose streamable-http
# transport answers 404 "Session not found" to a session id from before
# the restart.
#
#   scripts/e2e-mcp-restart.sh <python-with-the-mcp-package> [port]
#   e.g. scripts/e2e-mcp-restart.sh ~/mcp-light/venv/bin/python
#
# The scripted model calls the tool, restarts the server before its second
# answer, and calls the tool twice more. Passes when all three calls
# succeed and the last two are answered by a different process than the
# first. Not part of scripts/test.sh: it needs the mcp Python package.
set -euo pipefail

PY="${1:?usage: e2e-mcp-restart.sh <python-with-the-mcp-package> [port]}"
PORT="${2:-9799}"
"$PY" -c "import mcp.server.fastmcp" 2>/dev/null || { echo "e2e-mcp-restart: $PY cannot import mcp.server.fastmcp" >&2; exit 2; }
cd "$(dirname "$0")/.."
ROOT="$PWD"

TMP="$(mktemp -d)"
MOCK_PID=""
cleanup() {
    [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
    [ -f "$TMP/server.pid" ] && kill "$(cat "$TMP/server.pid")" 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

go build -o "$TMP/simple-harness" ./cmd/simple-harness
mkdir -p "$TMP/ws" "$TMP/home/.simple-harness"

cat >"$TMP/server.py" <<'PY'
import os, sys
from mcp.server.fastmcp import FastMCP
mcp = FastMCP("restartable", host="127.0.0.1", port=int(sys.argv[1]))

@mcp.tool()
def ping() -> str:
    """Answer with the server's process id."""
    return f"pong from pid {os.getpid()}"

mcp.run(transport="streamable-http")
PY

cat >"$TMP/restart.sh" <<SH
#!/usr/bin/env bash
set -euo pipefail
if [ -f "$TMP/server.pid" ]; then kill "\$(cat "$TMP/server.pid")" 2>/dev/null || true; sleep 0.5; fi
nohup "$PY" "$TMP/server.py" "$PORT" >>"$TMP/server.log" 2>&1 &
echo \$! >"$TMP/server.pid"
for _ in \$(seq 1 50); do
    curl -s -m 1 -o /dev/null "http://127.0.0.1:$PORT/mcp" && exit 0
    sleep 0.2
done
echo "server did not come up on port $PORT" >&2
exit 1
SH
chmod +x "$TMP/restart.sh"

printf '{"mcp_servers":[{"name":"restartable","transport":"http","endpoint":"http://127.0.0.1:%s/mcp","permission":"read_only"}]}\n' "$PORT" \
    >"$TMP/home/.simple-harness/config.json"
printf '[{"tool":"ping","args":{}},{"tool":"ping","args":{},"before":"%s"},{"tool":"ping","args":{}},{"text":"done"}]\n' "$TMP/restart.sh" \
    >"$TMP/steps.json"
echo "ping three times" >"$TMP/ws/prompt.md"

"$TMP/restart.sh"
python3 -u "$ROOT/scripts/scripted-model.py" "$TMP/model.port" "$TMP/model.req" "$TMP/steps.json" &
MOCK_PID=$!
for _ in $(seq 1 25); do [ -f "$TMP/model.port" ] && break; sleep 0.2; done

rc=0
(cd "$TMP/ws" && HOME="$TMP/home" "$TMP/simple-harness" run \
    --base-url "http://127.0.0.1:$(cat "$TMP/model.port")/v1" --model scripted \
    --workspace "$TMP/ws" --prompt-file "$TMP/ws/prompt.md" --output jsonl --max-turns 8 \
    >"$TMP/run.jsonl" 2>"$TMP/run.err") || rc=$?
if [ "$rc" -ne 0 ]; then
    echo "FAIL: the harness exited $rc: $(tail -c 300 "$TMP/run.err")" >&2
    exit 1
fi

python3 - "$TMP/run.jsonl" <<'PY'
import json, re, sys
results = [json.loads(l) for l in open(sys.argv[1])]
results = [e for e in results if e.get("event") == "tool_result"]
pids = [re.search(r"pid (\d+)", e.get("content", "")) for e in results]
ok = (len(results) == 3 and all(e.get("tool_result_status") == "ok" for e in results)
      and all(pids) and pids[0].group(1) != pids[1].group(1) == pids[2].group(1))
for e in results:
    print(" ", e.get("call_id"), e.get("tool_result_status"), e.get("content", "")[:90])
print("PASS: three calls, the last two answered by the restarted server" if ok
      else "FAIL: a call after the restart did not reach the new server")
sys.exit(0 if ok else 1)
PY
