#!/bin/sh
# Run from the standalone clone root. Keep the existing Make gates intact.
set -eu
test -f mobile/ios/WingthingCore/Package.swift || { echo 'Run from the clone root.' >&2; exit 2; }
task_root=$PWD
mkdir -p "$task_root/.scratch/tmp" "$task_root/.scratch/ios/build" \
    "$task_root/.scratch/ios/cache" "$task_root/.scratch/ios/config" \
    "$task_root/.scratch/ios/security" "$task_root/.scratch/ios/clang" \
    "$task_root/.scratch/ios/modules" "$task_root/.scratch/ios/xdg-cache" \
    "$task_root/.scratch/ios/xcode-home" "$task_root/.scratch/ios/source-packages" \
    "$task_root/.scratch/ios/sdk-stat" "$task_root/.scratch/go-build" \
    "$task_root/.scratch/gomod" "$task_root/.scratch/npm"
export TMPDIR="$task_root/.scratch/tmp"
export GOCACHE="$task_root/.scratch/go-build" GOMODCACHE="$task_root/.scratch/gomod"
export npm_config_cache="$task_root/.scratch/npm" npm_config_offline=true
export GOFLAGS=-p=2 GOMAXPROCS=2 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
export IOS_SCRATCH="$task_root/.scratch/ios/build" IOS_CACHE="$task_root/.scratch/ios/cache"
export IOS_CONFIG="$task_root/.scratch/ios/config" IOS_SECURITY="$task_root/.scratch/ios/security"
export CLANG_MODULE_CACHE_PATH="$task_root/.scratch/ios/clang"
export SWIFTPM_MODULECACHE_OVERRIDE="$task_root/.scratch/ios/modules"
export XDG_CACHE_HOME="$task_root/.scratch/ios/xdg-cache"
case "${1:-core}" in
    core) make -j1 -C mobile/ios check ;;
    xcode)
        : "${DEVELOPER_DIR:?Set DEVELOPER_DIR to the already admitted full Xcode Contents/Developer.}"
        export CFFIXED_USER_HOME="$task_root/.scratch/ios/xcode-home"
        export IOS_DERIVED_DATA="$task_root/.scratch/ios/simulator"
        export IOS_DEVICE_DERIVED_DATA="$task_root/.scratch/ios/device-unsigned"
        export XCODEBUILD_FLAGS="-jobs 2 -disableAutomaticPackageResolution -skipPackageUpdates -clonedSourcePackagesDirPath $task_root/.scratch/ios/source-packages -packageCachePath $IOS_CACHE OTHER_SWIFT_FLAGS=-j2 MODULE_CACHE_DIR=$CLANG_MODULE_CACHE_PATH SWIFT_MODULE_CACHE_PATH=$SWIFTPM_MODULECACHE_OVERRIDE SDK_STAT_CACHE_DIR=$task_root/.scratch/ios/sdk-stat"
        # Xcode's automatic failure bundle can ignore TMPDIR. Give each build
        # an explicit fresh destination, including failures during resolution.
        result_root="$task_root/.scratch/ios/results/$(date +%Y%m%dT%H%M%S)"
        mkdir -p "$result_root"
        build_flags=$XCODEBUILD_FLAGS
        status=0
        XCODEBUILD_FLAGS="$build_flags -resultBundlePath $result_root/simulator.xcresult" \
            make -j1 -C mobile/ios simulator-build || status=$?
        XCODEBUILD_FLAGS="$build_flags -resultBundlePath $result_root/device-unsigned.xcresult" \
            make -j1 -C mobile/ios device-unsigned-check || status=$?
        exit "$status"
        ;;
    release) make -j1 -C mobile/ios live-preview-release-check ;;
    *) echo 'Usage: sh mobile/ios/scripts/offline_validation.sh core|xcode|release' >&2; exit 2 ;;
esac
