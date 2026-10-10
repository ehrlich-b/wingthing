#!/usr/bin/env bash
# One invocation, with a lock, wall-clock bound, and rotating JSONL receipts.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
exec python3 "$repo/scripts/dogfood/run.py" "$@"
