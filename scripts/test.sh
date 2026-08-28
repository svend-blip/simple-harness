#!/bin/bash
set -euo pipefail
# scripts/test.sh — Simple Harness test suite contract.
# Exit 0 = all Go tests pass; non-zero otherwise. The exit code is the
# authoritative test verdict (docs/ARCHITECTURE.md §"Distribution shape";
# RUNS-BACKLOG §"Cross-run bound decisions"; SCOPE §39).
# Uses bash, not /bin/sh, because TG5 requires `set -euo pipefail`
# and on Debian-family systems /bin/sh is dash which lacks pipefail.
cd "$(dirname "$0")/.."
go test ./...
