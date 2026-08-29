#!/bin/sh
set -eu

BINARY=${1:-./wt}

if [ ! -x "$BINARY" ]; then
    echo "release contract: executable not found: $BINARY" >&2
    exit 1
fi

"$BINARY" --version >/dev/null
"$BINARY" mcp connect --help >/dev/null

SERVE_HELP=$("$BINARY" serve --help)
ROOST_HELP=$("$BINARY" roost start --help)
printf '%s\n' "$SERVE_HELP" | grep -q -- '--https'
printf '%s\n' "$ROOST_HELP" | grep -q -- '--https'
"$BINARY" local-cert status --help >/dev/null

echo "release contract: ok ($BINARY)"
