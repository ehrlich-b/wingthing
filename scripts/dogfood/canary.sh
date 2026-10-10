#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
exec nice -n 15 python3 "$repo/scripts/dogfood/canary.py" "$@"
