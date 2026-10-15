#!/usr/bin/env bash
# Source this file from any working directory; configured tools and caches win.
export WT_ANDROID_WORKDIR="${WT_ANDROID_WORKDIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/.local}"
export ANDROID_HOME="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$WT_ANDROID_WORKDIR/sdk}}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/29.0.14206865}"
export JAVA_HOME="${JAVA_HOME:-$ANDROID_HOME/jdk-21.0.12.1+1}"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/gradle-9.3.1/bin:$PATH"
export GRADLE_USER_HOME="${GRADLE_USER_HOME:-$WT_ANDROID_WORKDIR/gradle-home}"
export TMPDIR="${TMPDIR:-$WT_ANDROID_WORKDIR/tmp}"
export GOPATH="${GOPATH:-$WT_ANDROID_WORKDIR/go}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-$WT_ANDROID_WORKDIR/go-build}"
export GOMAXPROCS="${GOMAXPROCS:-4}"
# gomobile builds both ABIs concurrently; cap each Go build to two compiler jobs.
export GOFLAGS="${GOFLAGS:--buildvcs=false -p=2}"
mkdir -p "$TMPDIR"
