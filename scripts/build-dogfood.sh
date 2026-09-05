#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."
if [ "$#" -ne 0 ]; then
  echo "usage: scripts/build-dogfood.sh (builds darwin/arm64 and linux/amd64 locally)" >&2
  exit 2
fi
if [ ! -f web/dist/index.html ] || [ ! -d web/dist/assets ]; then
  echo "Build the real web assets with make web first." >&2
  exit 1
fi

source_digest() {
  { git ls-files -z --cached --others --exclude-standard | LC_ALL=C sort -zu | xargs -0 shasum -a 256
    find web/dist -type f -exec shasum -a 256 {} + | LC_ALL=C sort
  } | shasum -a 256 | cut -d ' ' -f 1
}

commit=$(git rev-parse HEAD)
source=$(source_digest)
version="dogfood-${commit:0:12}-${source:0:12}"
output="dist/dogfood/$version"
mkdir -p "$output"

for platform in darwin/arm64 linux/amd64; do
  os=${platform%/*}
  arch=${platform#*/}
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" make build VERSION="$version" BINARY="$output/wt-$os-$arch"
done
if [ "$source" != "$(source_digest)" ]; then
  echo "Source changed during build; do not deploy these artifacts. Rebuild." >&2
  exit 1
fi

mac_sha=$(shasum -a 256 "$output/wt-darwin-arm64" | cut -d ' ' -f 1)
linux_sha=$(shasum -a 256 "$output/wt-linux-amd64" | cut -d ' ' -f 1)
jq -n --arg version "$version" --arg commit "$commit" --arg source "$source" \
  --arg mac "$mac_sha" --arg linux "$linux_sha" \
  '{version:$version, commit:$commit, source_sha256:$source,
    artifacts:{"darwin/arm64":$mac,"linux/amd64":$linux}}' > "$output/manifest.json"
printf '%s\n' "$output/manifest.json"
