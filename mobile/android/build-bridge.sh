#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
source ./toolchain.sh
./preflight.py
cd ../..
mkdir -p mobile/android/app/libs
gomobile bind -target=android/arm64,android/amd64 -androidapi 26 -o mobile/android/app/libs/wingthing.aar ./mobile/android/bridge
# Go's Android C++ linker uses the shared NDK runtime. gomobile does not copy
# it into the AAR, so include the runtime from the same NDK used for this build.
python3 - <<'PY'
import os
from pathlib import Path
from zipfile import ZipFile
sysroot = Path(os.environ["ANDROID_NDK_HOME"]) / "toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib"
with ZipFile("mobile/android/app/libs/wingthing.aar", "a") as aar:
    for abi, triple in (("arm64-v8a", "aarch64-linux-android"), ("x86_64", "x86_64-linux-android")):
        aar.write(sysroot / triple / "libc++_shared.so", f"jni/{abi}/libc++_shared.so")
PY
