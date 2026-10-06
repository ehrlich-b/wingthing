#!/bin/sh
set -eu

# This workflow publishes stable assets; preview releases use a separate channel.
if [ "$#" -ne 1 ] || ! printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo 'release tag must be vMAJOR.MINOR.PATCH; prerelease tags are unsupported' >&2
    exit 1
fi
