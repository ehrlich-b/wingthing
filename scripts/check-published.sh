#!/bin/sh
set -eu

# A tag is not a release. This answers only from what users can download:
# the GitHub release assets and the tag the wingthing.ai mirror selects.
RELEASE_BASE="${WT_RELEASE_BASE:-https://github.com/ehrlich-b/wingthing/releases/download}"
LATEST_URL="${WT_LATEST_URL:-https://wingthing.ai/dl/latest.json}"
ASSETS="SHA256SUMS wt-linux-amd64 wt-linux-arm64 wt-darwin-amd64 wt-darwin-arm64"

if [ "$#" -ne 1 ] || ! printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo 'usage: check-published.sh vMAJOR.MINOR.PATCH' >&2
    exit 2
fi
TAG="$1"

# One byte is enough to prove an asset answers; a ranged GET returns 206.
status() {
    curl -sSL -r 0-0 --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' "$1" 2>/dev/null || true
}

MISSING=""
for asset in $ASSETS; do
    code=$(status "${RELEASE_BASE%/}/${TAG}/${asset}")
    case "$code" in
        200|206) ;;
        *) MISSING="${MISSING}${MISSING:+, }${asset} returned ${code:-no response}" ;;
    esac
done

LATEST=$(curl -fsSL --connect-timeout 10 --max-time 30 --max-filesize 1048576 "$LATEST_URL" 2>/dev/null |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1) || LATEST=""
if [ "$LATEST" != "$TAG" ]; then
    MISSING="${MISSING}${MISSING:+, }latest.json names ${LATEST:-nothing}"
fi

if [ -n "$MISSING" ]; then
    echo "NOT PUBLISHED ${TAG}: ${MISSING}"
    exit 1
fi
echo "PUBLISHED ${TAG}"
