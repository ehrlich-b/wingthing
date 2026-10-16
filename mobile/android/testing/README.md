# Android runtime checks

Use JDK 21, Gradle 9.3.1, Android platform/build-tools 37.0 and NDK 29.
Set `ANDROID_HOME` (or `ANDROID_SDK_ROOT`), `ANDROID_NDK_HOME` and `JAVA_HOME`
to installed tools. Existing cache variables and `HOME` are preserved.
`WT_ANDROID_WORKDIR` defaults to ignored `mobile/android/.local`; SDK and cache
defaults are beneath it. The scripts do not install or download a toolchain.
Go modules are pinned in the root `go.mod` and `go.sum`.
`build-bridge.sh` does not run `go get` or change those pins.

From the repository root, run the unsigned build and test gate:

```sh
nice -n 10 make check-android
```

This sources `mobile/android/toolchain.sh` for both the native bridge build and
Gradle, then runs JVM unit tests, debug lint, and both debug APK builds offline.
It needs the installed tools and dependency caches described above.

To also build a signed release APK:

```sh
source mobile/android/toolchain.sh
# Set WT_SIGNING_STORE and WT_SIGNING_PASSWORD before the release build.
nice -n 10 mobile/android/build-bridge.sh
nice -n 10 mobile/android/build-release.sh
nice -n 10 mobile/android/gradlew -p mobile/android --offline --no-daemon \
  :app:assembleDebug :app:assembleDebugAndroidTest
nice -n 10 go build -o "$WT_ANDROID_WORKDIR/testpeer" ./mobile/android/testing/peer
```

Release signing uses `WT_SIGNING_STORE` (an absolute path or a path relative to
`mobile/android`, default `signing/wingthing-upload.jks`) and `WT_SIGNING_PASSWORD`.
Keep signing credentials outside Git. The build helper requires the password
and preserves the configured store.

When KVM is available, create a disposable API 35 Google APIs
x86_64 AVD with the official SDK's `avdmanager`, then launch it with all writable
state under `WT_ANDROID_WORKDIR`. The optional Docker image uses a generic emulator
account; set `EMULATOR_UID` and `KVM_GID` build arguments to match mounted files
and the KVM device when needed.

```sh
export ANDROID_USER_HOME="$WT_ANDROID_WORKDIR/sdk/android-home"
export ANDROID_AVD_HOME="$WT_ANDROID_WORKDIR/sdk/avd"
export ANDROID_EMULATOR_HOME="$ANDROID_USER_HOME"
export ANDROID_ADB_SERVER_PORT=5039
export ANDROID_EMU_CRASH_REPORTING_DATABASE="$WT_ANDROID_WORKDIR/tmp/emulator-crash"
export XDG_RUNTIME_DIR="$WT_ANDROID_WORKDIR/tmp/xdg"
mkdir -p "$ANDROID_USER_HOME" "$ANDROID_AVD_HOME" "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"
nice -n 10 "$ANDROID_HOME/emulator/emulator" -avd wingthing-current \
  -port 5558 -no-window -no-audio -no-boot-anim -gpu swiftshader \
  -memory 1536 -cores 2 -no-snapshot -no-metrics -crash-report-mode disabled
```

Keep `"$WT_ANDROID_WORKDIR/testpeer" -listen 127.0.0.1:38473` running with stdin open in a
separate terminal. It accepts fixture controls only on loopback, uses disposable
state, and imports the current server protocol. It never opens real wing state.
After the emulator returns `1` for `sys.boot_completed`, disable its Wi-Fi/mobile
data and configure the loopback-only test connection:

```sh
adb -P 5039 -s emulator-5558 shell svc wifi disable
adb -P 5039 -s emulator-5558 shell svc data disable
adb -P 5039 -s emulator-5558 reverse tcp:38473 tcp:38473
adb -P 5039 -s emulator-5558 install -r -t app/build/outputs/apk/debug/app-debug.apk
adb -P 5039 -s emulator-5558 install -r -t \
  app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
adb -P 5039 -s emulator-5558 shell am instrument -w -r \
  ai.wingthing.phone.test/androidx.test.runner.AndroidJUnitRunner
```

The last commands run from `mobile/android`. Select only a failed class for a
retry with `-e class ai.wingthing.phone.CameraScreenTest` before the runner name.
Preserve the initial log as well as any retry. First boot can produce system or
System UI ANR dialogs; wait for Android to settle and dismiss those dialogs before
judging camera/UI results. Never count a failed or obscured capture as passing.

The suite contains nine tests, including a real 301-second wait for the server's
five-minute pairing deadline. It exercises the actual AAR, Android Keystore,
HTTP boxes, same-session reconnect, live revoke and rejected revoked reconnect.
Host bridge/race tests also check durable server authority and no input replay.

Screenshot tests use actual Compose screens with fixture content, both themes,
and a separate test activity. Production windows keep `FLAG_SECURE`. Fixture
PNGs are exported to `/sdcard/Download/wingthing-evidence`; pull that directory
into `$WT_ANDROID_WORKDIR/evidence/screenshots/` after the tests. Camera evidence uses an
emulated camera and does not establish a physical QR scan.

The APK targets API 37; these runtime checks use API 35. Physical-phone QR/Mac
session acceptance, API 37 local-network permission, hardware StrongBox and real
LAN/Tailscale/relay routing remain separate and are not established by this suite.
