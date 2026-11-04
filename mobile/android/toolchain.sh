#!/usr/bin/env bash
# Source this file from any working directory; configured tools and caches win.
export WT_ANDROID_WORKDIR="${WT_ANDROID_WORKDIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/.local}"
if [[ ! -d "$WT_ANDROID_WORKDIR/sdk" && -z "${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}" && ( -n "${WINGTHING_ANDROID_HOME:-}" || "$(hostname)" == forge2-vm ) ]]; then
    _wt_shared_android="${WINGTHING_ANDROID_HOME:-/work/cache/android}"
fi
export ANDROID_HOME="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-${_wt_shared_android:+$_wt_shared_android/sdk}}}"
export ANDROID_HOME="${ANDROID_HOME:-$WT_ANDROID_WORKDIR/sdk}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/29.0.14206865}"
export JAVA_HOME="${JAVA_HOME:-${_wt_shared_android:+$_wt_shared_android/sdk/jdk-21.0.13+9}}"
export JAVA_HOME="${JAVA_HOME:-$ANDROID_HOME/jdk-21.0.12.1+1}"
export PATH="$WT_ANDROID_WORKDIR/bin:${_wt_shared_android:+$_wt_shared_android/bin:}$JAVA_HOME/bin:$ANDROID_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/gradle-9.3.1/bin:$PATH"
export GRADLE_USER_HOME="${GRADLE_USER_HOME:-${_wt_shared_android:+$_wt_shared_android/gradle-home}}"
export GRADLE_USER_HOME="${GRADLE_USER_HOME:-$WT_ANDROID_WORKDIR/gradle-home}"
unset _wt_shared_android
export TMPDIR="${TMPDIR:-$WT_ANDROID_WORKDIR/tmp}"
export GOPATH="${GOPATH:-$WT_ANDROID_WORKDIR/go}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-$WT_ANDROID_WORKDIR/go-build}"
export GOMAXPROCS="${GOMAXPROCS:-4}"
# gomobile builds both ABIs concurrently; cap each Go build to two compiler jobs.
export GOFLAGS="${GOFLAGS:--buildvcs=false -p=2}"
mkdir -p "$TMPDIR"
