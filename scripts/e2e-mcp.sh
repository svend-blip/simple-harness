#!/bin/bash
set -euo pipefail
# scripts/e2e-mcp.sh — Simple Harness end-to-end MCP acceptance runner
# (SCOPE §40 + amendment §44). Run 020 / handoff 065 — WORK 2 runner
# body. Drives the harness loop against the live mcp-light endpoint
# at $MCP_ENDPOINT with a MOCK model that emits a tool_call for an
# allowlisted read-only mcp-light tool. The acceptance surface is the
# JSONL event stream (tool_call → tool_result with real server data),
# per SCOPE §40 — NOT terminal scraping, NOT a separate test pin.
# The MCP half is REAL (the harness's cmdMcpInit calls mcp.NewHTTPTransport
# against $MCP_ENDPOINT); the model half is DETERMINISTIC (a Python
# stdlib http.server in the workspace, no external dependencies). This
# closes §44's event assertion without inheriting the H2 model-
# availability blocker per GOAL §2.
#
# Usage: scripts/e2e-mcp.sh MCP_ENDPOINT
#   MCP_ENDPOINT  base URL of the mcp-light streamable-http endpoint (required)
#
# Exit codes (SCOPE §28):
#   0  acceptance passed (tool_call → tool_result cycle observed with
#                          real server data in the result content)
#   1  usage error (no / wrong args)
#   2  configuration error (endpoint unreachable / config invalid)
#   3  model/API failure (mock model returned an error / harness model
#                         client failed)
#   6  interrupted (SIGINT/SIGTERM)
#
# The runner is NOT part of scripts/test.sh (it needs a live endpoint
# per GOAL §3). It is invoked manually at acceptance time.
#
# Environment escape hatches (matching scripts/e2e-coding.sh:44-50 +
# scripts/e2e-review.sh:47-58 precedent):
#   WORKSPACE_DIR_OVERRIDE  when set, the runner uses the supplied dir
#                            as the workspace AND skips the cleanup trap
#                            (the WORK 3 / handoff 066 binding pin sets
#                            this to inspect the post-run workspace).
#   WORKSPACE_KEEP          when set (without WORKSPACE_DIR_OVERRIDE),
#                            the runner disables the cleanup trap but
#                            still creates + owns the mktemp dir.

if [ $# -ne 1 ]; then
    cat <<'EOF' >&2
usage: scripts/e2e-mcp.sh MCP_ENDPOINT
  MCP_ENDPOINT  base URL of the mcp-light streamable-http endpoint (required)
EOF
    exit 1
fi

MCP_ENDPOINT="$1"

# Treat empty / whitespace-only values as missing — a bare `--` separator
# or accidental whitespace argument must surface as a usage error.
if [ -z "${MCP_ENDPOINT// }" ] || [ "$MCP_ENDPOINT" = "--" ]; then
    cat <<'EOF' >&2
usage: scripts/e2e-mcp.sh MCP_ENDPOINT
  MCP_ENDPOINT  base URL of the mcp-light streamable-http endpoint (required)
EOF
    exit 1
fi

# ---------------------------------------------------------------------------
# (1) Live mcp-light pre-check — issue the MCP `initialize` JSON-RPC 2.0
# request and verify the response carries `"serverInfo":{"name":"mcp-light"}`
# (the canonical server identity per the live server). On any failure exit
# 2 (SCOPE §28 configuration error) with a stderr message naming the
# unreachable endpoint + the curl error.
# ---------------------------------------------------------------------------
PROBE_BODY='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e-mcp-probe","version":"0"}}}'
if ! curl -sf --max-time 5 \
        -X POST \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d "$PROBE_BODY" \
        "$MCP_ENDPOINT" -o /tmp/e2e-mcp-init.$$.out 2>/tmp/e2e-mcp-init.$$.err; then
    echo "scripts/e2e-mcp.sh: mcp-light unreachable at $MCP_ENDPOINT: $(cat /tmp/e2e-mcp-init.$$.err)" >&2
    rm -f /tmp/e2e-mcp-init.$$.out /tmp/e2e-mcp-init.$$.err
    exit 2
fi
if ! grep -q '"name":"mcp-light"' /tmp/e2e-mcp-init.$$.out; then
    echo "scripts/e2e-mcp.sh: mcp-light identity missing at $MCP_ENDPOINT (serverInfo.name != mcp-light)" >&2
    rm -f /tmp/e2e-mcp-init.$$.out /tmp/e2e-mcp-init.$$.err
    exit 2
fi
rm -f /tmp/e2e-mcp-init.$$.out /tmp/e2e-mcp-init.$$.err
echo "scripts/e2e-mcp.sh: mcp-light reachable at $MCP_ENDPOINT" >&2

# ---------------------------------------------------------------------------
# (2) Temp workspace setup — the project's .simple-harness/config.json is
# searched upward from cwd at config.Load() per internal/config/config.go
# findProjectConfig. The harness invocation runs with cwd=$WORKSPACE so
# the workspace's .simple-harness/config.json is picked up.
# ---------------------------------------------------------------------------
if [ -n "${WORKSPACE_KEEP:-}" ]; then
    trap -- EXIT
elif [ -n "${WORKSPACE_DIR_OVERRIDE:-}" ]; then
    trap -- EXIT
else
    trap 'cleanup_workspace' EXIT
fi
cleanup_workspace() {
    if [ -n "${MOCK_PID:-}" ]; then
        kill "$MOCK_PID" 2>/dev/null || true
        wait "$MOCK_PID" 2>/dev/null || true
    fi
    if [ -z "${WORKSPACE_DIR_OVERRIDE:-}" ] && [ -z "${WORKSPACE_KEEP:-}" ]; then
        rm -rf "$WORKSPACE"
    fi
}
if [ -n "${WORKSPACE_DIR_OVERRIDE:-}" ]; then
    WORKSPACE="$WORKSPACE_DIR_OVERRIDE"
    mkdir -p "$WORKSPACE"
else
    WORKSPACE=$(mktemp -d -t e2e-mcp.XXXXXX)
fi
mkdir -p "$WORKSPACE/.simple-harness"
echo "scripts/e2e-mcp.sh: WORKSPACE=$WORKSPACE" >&2

# ---------------------------------------------------------------------------
# (3) Mock model — Python stdlib http.server on a kernel-assigned port.
# First request emits a tool_call for get_governance_index (bare name
# per HARNESS-CONTRACT.md §"Collision naming" — get_governance_index
# collides with no harness builtin so it registers BARE, NOT under the
# mcp-light__get_governance_index prefix; amendment 4 to Run 020 GOAL
# corrected the 065 handoff's prefixed-name premise which assumed the
# prefix was always applied); second request emits an assistant
# content delta ("Patch applied.") so the loop reaches the single-turn
# happy path. Third+ request returns HTTP 500 (defensive — the mock
# model should never be called more than twice given --max-turns 4).
# ---------------------------------------------------------------------------
cat > "$WORKSPACE/mock_model.py" <<'MOCK_EOF'
#!/usr/bin/env python3
import http.server
import json
import sys
import threading

PORT_FILE = sys.argv[1] if len(sys.argv) > 1 else "/tmp/mock_port"
n_requests_lock = threading.Lock()
n_requests = 0


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def do_POST(self):
        global n_requests
        if self.path != "/v1/chat/completions":
            self.send_error(404, "unexpected path")
            return
        with n_requests_lock:
            n_requests += 1
            this_request = n_requests
        if this_request == 1:
            payload = (
                'data: {"choices":[{"delta":{"tool_calls":['
                '{"index":0,"id":"call_e2e_mcp_1","function":'
                '{"name":"get_governance_index","arguments":"{}"}}'
                ']}}]}\n\n'
            )
        elif this_request == 2:
            payload = (
                'data: {"choices":[{"delta":{"content":"Patch applied."}}]}\n\n'
            )
        else:
            self.send_error(500, "mock model received unexpected extra request")
            return
        payload += "data: [DONE]\n\n"
        body = payload.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main():
    srv = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    with open(PORT_FILE, "w") as fp:
        fp.write(str(srv.server_address[1]))
    srv.serve_forever()


if __name__ == "__main__":
    main()
MOCK_EOF

nohup python3 -u "$WORKSPACE/mock_model.py" "$WORKSPACE/mock_port" \
    > "$WORKSPACE/mock.out" 2> "$WORKSPACE/mock.err" &
MOCK_PID=$!

# Wait up to 2 seconds for the mock model to write its port file.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [ -f "$WORKSPACE/mock_port" ]; then
        break
    fi
    sleep 0.2
done
if [ ! -f "$WORKSPACE/mock_port" ]; then
    echo "scripts/e2e-mcp.sh: mock model failed to start (no port file written)" >&2
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
    exit 3
fi
MOCK_PORT=$(cat "$WORKSPACE/mock_port")
echo "scripts/e2e-mcp.sh: mock model listening on 127.0.0.1:$MOCK_PORT (pid=$MOCK_PID)" >&2

# ---------------------------------------------------------------------------
# (4) Pinned config file — only the mcp_servers declaration. The model
# block is supplied via --base-url, which overrides the config's
# model.base_url per the harness's flag-precedence contract.
# ---------------------------------------------------------------------------
cat > "$WORKSPACE/.simple-harness/config.json" <<EOF_CONFIG
{
  "mcp_servers": [
    {
      "name": "mcp-light",
      "transport": "http",
      "endpoint": "$MCP_ENDPOINT",
      "permission": "read_only",
      "allowlist": ["get_governance_index"]
    }
  ]
}
EOF_CONFIG

# ---------------------------------------------------------------------------
# (5) Prompt file — small static instruction matching the precedent at
# scripts/e2e-coding.sh:54 + scripts/e2e-review.sh:69.
# ---------------------------------------------------------------------------
printf '%s' 'Call the mcp-light governance-index tool once.' > "$WORKSPACE/prompt.md"

# ---------------------------------------------------------------------------
# (6) 3-attempt retry loop. Each attempt re-writes the pinned config
# (defends against any state leakage), invokes the harness, and asserts
# the runner's happy-path evidence (JSONL tool_call + tool_result cycle
# + real server data + exit 0). 0/3 attempts passing is a FAILED
# criterion per GOAL §2 (matches the 018 rule).
# ---------------------------------------------------------------------------
for attempt in 1 2 3; do
    # Re-write the pinned config at the top of each attempt.
    cat > "$WORKSPACE/.simple-harness/config.json" <<EOF_ATTEMPT
{
  "mcp_servers": [
    {
      "name": "mcp-light",
      "transport": "http",
      "endpoint": "$MCP_ENDPOINT",
      "permission": "read_only",
      "allowlist": ["get_governance_index"]
    }
  ]
}
EOF_ATTEMPT
    rm -f "$WORKSPACE/run.$attempt.jsonl" "$WORKSPACE/run.$attempt.err"

    set +e
    (
        cd "$WORKSPACE" && simple-harness run \
            --base-url "http://127.0.0.1:$MOCK_PORT/v1" \
            --model "e2e-mcp-mock" \
            --workspace "$WORKSPACE" \
            --permission "read_only" \
            --prompt-file "$WORKSPACE/prompt.md" \
            --output jsonl \
            --max-turns 4 \
            > "$WORKSPACE/run.$attempt.jsonl" 2> "$WORKSPACE/run.$attempt.err"
    )
    harness_exit=$?
    set -e

    FAIL_REASON=""
    SESSION_ID=""

    # (A) Startup listing succeeded — the harness's stderr must NOT contain
    # the "declared but unreachable" structured-startup-error message per
    # SCOPE §43 + Out-§11 replacement.
    if grep -qF 'simple-harness: mcp server' "$WORKSPACE/run.$attempt.err"; then
        FAIL_REASON="(A) mcp-light unreachable at session start"
    # (B) JSONL carries a tool_call event (compact JSON form per the V1 emitter).
    elif ! grep -q '"event":"tool_call"' "$WORKSPACE/run.$attempt.jsonl"; then
        FAIL_REASON="(B) JSONL missing tool_call event"
    # (C) tool_call event names the server-qualified tool
    # mcp-light__get_governance_index per HARNESS-CONTRACT.md's
    # collision-avoidance form. The 066 amendment-4 corrected the
    # tool_name in the assertion to `get_governance_index` (bare) per
    # the V1 contract — get_governance_index collides with no harness
    # builtin so it registers BARE; the prefix form applies ONLY on
    # collision.
    else
        if command -v jq >/dev/null 2>&1; then
            TOOL_NAME=$(jq -r 'select(.event == "tool_call") | .tool' "$WORKSPACE/run.$attempt.jsonl" | head -1)
        else
            TOOL_NAME=$(python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "tool_call":
        print(ev.get("tool", ""))
        break
' < "$WORKSPACE/run.$attempt.jsonl")
        fi
        if [ "$TOOL_NAME" != "get_governance_index" ]; then
            FAIL_REASON="(C) tool_call event tool field mismatch (got '$TOOL_NAME', want 'get_governance_index')"
        # (D) JSONL carries a tool_result event WITH REAL SERVER DATA — the
        # marker substring "11_SCOPE.md" is verified live in the assertion
        # sequence's pre-flight per handoff 065's binding contract.
        elif ! grep -q '"event":"tool_result"' "$WORKSPACE/run.$attempt.jsonl"; then
            FAIL_REASON="(D) JSONL missing tool_result event"
        else
            if command -v jq >/dev/null 2>&1; then
                RESULT_CONTENT=$(jq -r 'select(.event == "tool_result") | .content' "$WORKSPACE/run.$attempt.jsonl" | head -1)
            else
                RESULT_CONTENT=$(python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "tool_result":
        print(ev.get("content", "") or "")
        break
' < "$WORKSPACE/run.$attempt.jsonl")
            fi
            if [ -z "$RESULT_CONTENT" ] || ! echo "$RESULT_CONTENT" | grep -qF '11_SCOPE.md'; then
                FAIL_REASON="(D) tool_result event missing real server data marker (11_SCOPE.md not found)"
            # (E) tool_call and tool_result share the same call_id — the
            # call_id correlation is the binding evidence per the canonical
            # mock model pattern at main_test.go:2582-2591.
            else
                if command -v jq >/dev/null 2>&1; then
                    CALL_ID=$(jq -r 'select(.event == "tool_call" and .tool == "get_governance_index") | .call_id' "$WORKSPACE/run.$attempt.jsonl" | head -1)
                    RESULT_CALL_ID=$(jq -r 'select(.event == "tool_result") | .call_id' "$WORKSPACE/run.$attempt.jsonl" | head -1)
                else
                    CALL_ID=$(python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "tool_call" and ev.get("tool") == "get_governance_index":
        print(ev.get("call_id", "") or "")
        break
' < "$WORKSPACE/run.$attempt.jsonl")
                    RESULT_CALL_ID=$(python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "tool_result":
        print(ev.get("call_id", "") or "")
        break
' < "$WORKSPACE/run.$attempt.jsonl")
                fi
                if [ -z "$CALL_ID" ] || [ "$CALL_ID" != "$RESULT_CALL_ID" ]; then
                    FAIL_REASON="(E) tool_call / tool_result call_id mismatch (call='$CALL_ID' result='$RESULT_CALL_ID')"
                # (F) Session id extraction from started event.
                else
                    if command -v jq >/dev/null 2>&1; then
                        SESSION_ID=$(jq -r 'select(.event == "started") | .session_id' "$WORKSPACE/run.$attempt.jsonl" | head -1)
                    else
                        SESSION_ID=$(python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        ev = json.loads(line)
    except Exception:
        continue
    if ev.get("event") == "started":
        print(ev.get("session_id", "") or "")
        break
' < "$WORKSPACE/run.$attempt.jsonl")
                    fi
                    if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
                        FAIL_REASON="(F) session id not extractable from JSONL"
                    # (G) Harness exit code 0 per SCOPE §28.
                    elif [ "$harness_exit" -ne 0 ]; then
                        FAIL_REASON="(G) harness exited non-zero (harness_exit=$harness_exit, see run.$attempt.err)"
                    fi
                fi
            fi
        fi
    fi

    if [ -z "$FAIL_REASON" ]; then
        echo "attempt $attempt: PASS — session_id=$SESSION_ID" >&2
        echo "session_id=$SESSION_ID" >&2
        exit 0
    fi
    echo "attempt $attempt: FAIL — $FAIL_REASON" >&2
done

# ---------------------------------------------------------------------------
# (7) Final disposition — 0/3 attempts passing is a FAILED criterion per
# GOAL §2 + the 018 rule.
# ---------------------------------------------------------------------------
echo "0/3 attempts passed; FAILED criterion per GOAL §2" >&2
exit 1