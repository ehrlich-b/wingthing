#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./toolchain.sh
: "${WT_SIGNING_PASSWORD:?Set WT_SIGNING_PASSWORD for release signing}"
./gradlew --max-workers=4 -Dorg.gradle.jvmargs=-Xmx3g :app:testDebugUnitTest :app:lint :app:assembleRelease
