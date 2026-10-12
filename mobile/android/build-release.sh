#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./toolchain.sh
export WT_SIGNING_STORE="$WT_LANE/signing/wingthing-upload.jks"
export WT_SIGNING_PASSWORD="$(cat "$WT_LANE/signing/password")"
./gradlew --max-workers=4 -Dorg.gradle.jvmargs=-Xmx3g :app:testDebugUnitTest :app:lint :app:assembleRelease
