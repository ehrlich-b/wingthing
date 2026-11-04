#!/usr/bin/env bash
# Install the exact Go mobile tools into the configured local work directory.
set -euo pipefail
cd "$(dirname "$0")"
source ./toolchain.sh
version=v0.0.0-20261011034528-197770444867
# The Go module download verifies the archive; also compare to the committed pin.
info=$(go mod download -json "golang.org/x/mobile@$version")
python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["Sum"] == "h1:rav73cdd7OQUPgt1aIFbZQTiPNnjfueKXvfOiLCY0Ss=" and d["GoModSum"] == "h1:HR/iBzuQmVTl/us733QPwrqHp43a8m9ZLgyKXD3ST6U=", "unexpected mobile module checksum"' <<<"$info"
mkdir -p "$WT_ANDROID_WORKDIR/bin"
# A versioned go install asks the proxy for deprecation metadata even when
# every module is cached. Serve the existing download cache as a file proxy
# when the caller explicitly requests offline operation.
if [[ "${GOPROXY:-}" == off ]]; then
  GOPROXY=$(python3 -c 'from pathlib import Path; import sys; print(Path(sys.argv[1]).resolve().as_uri())' "$GOMODCACHE/cache/download")
  export GOPROXY
fi
GOBIN="$WT_ANDROID_WORKDIR/bin" go install "golang.org/x/mobile/cmd/gomobile@$version" "golang.org/x/mobile/cmd/gobind@$version"
go version -m "$WT_ANDROID_WORKDIR/bin/gomobile" "$WT_ANDROID_WORKDIR/bin/gobind"
