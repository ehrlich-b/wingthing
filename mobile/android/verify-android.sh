#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./toolchain.sh
# Start sdk/bin/testpeer -listen 127.0.0.1:38473 with stdin open first.
adb reverse tcp:38473 tcp:38473
adb shell rm -rf /sdcard/Download/wingthing-evidence
./gradlew --max-workers=4 -Dorg.gradle.jvmargs=-Xmx3g :app:connectedDebugAndroidTest
mkdir -p "$WT_LANE/evidence/screenshots"
adb pull /sdcard/Download/wingthing-evidence/. "$WT_LANE/evidence/screenshots/"
