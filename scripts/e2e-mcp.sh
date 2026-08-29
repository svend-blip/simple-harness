#!/bin/bash
set -euo pipefail
# scripts/e2e-mcp.sh — Simple Harness end-to-end MCP acceptance runner
# (SCOPE §40 + amendment §44). The runner validates its arguments in
# THIS handoff (handoff 064); the full body — temp-workspace setup +
# pinned mcp_servers config + simple-harness run invocation + JSONL
# transcript capture + assertion sequence — lands in WORK 2.
#
# Usage: scripts/e2e-mcp.sh MCP_ENDPOINT
#   MCP_ENDPOINT  base URL of the mcp-light streamable-http endpoint (required)
#
# Exit codes (SCOPE §28):
#   0  acceptance passed (WORK 2 will gate this)
#   1  usage error (no / wrong args) — THIS handoff
#   2  configuration error (WORK 2 — endpoint unreachable / config invalid)
#   3  model/API failure (WORK 2 — model returned an error)
#   6  interrupted (SIGINT/SIGTERM)
#
# The runner is NOT part of scripts/test.sh (it needs a live endpoint
# per GOAL §3). It is invoked manually at acceptance time.

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

# Stub body — argument validation passed. handoff 065 lands the runner body
# (temp-workspace setup + pinned mcp_servers config in config.json +
# simple-harness run invocation + JSONL capture + assertion sequence).
# The script exits 0 here because arg validation succeeded (TG1's
# "validates arguments" half is satisfied); the stub stderr message
# keeps the handoff-064-delegated state unambiguous for the reviewer.
echo "scripts/e2e-mcp.sh: handoff 065 lands the runner body — argument validation passed (MCP_ENDPOINT=$1)" >&2
exit 0