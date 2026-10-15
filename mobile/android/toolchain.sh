#!/usr/bin/env bash
# Source this file from any working directory.
export WT_LANE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export ANDROID_HOME="$WT_LANE/sdk"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/29.0.14206865"
export JAVA_HOME="$ANDROID_HOME/jdk-21.0.12.1+1"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/gradle-9.3.1/bin:$PATH"
export GRADLE_USER_HOME="$WT_LANE/sdk/gradle-home"
export HOME="$WT_LANE/home"
export TMPDIR="$WT_LANE/tmp"
export GOPATH="$WT_LANE/go"
export GOMODCACHE="$WT_LANE/go-mod"
export GOCACHE="$WT_LANE/go-build"
export GOMAXPROCS=4
# gomobile builds both ABIs concurrently; cap each Go build to two compiler jobs.
export GOFLAGS="-buildvcs=false -p=2"
