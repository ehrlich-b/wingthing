# Forge emulator

`/dev/kvm` exists but the lane user is not a member of its group. Sudo requires
a password. Run the SDK emulator in this small container with the device passed
through, user 1000 and KVM group 992. This needs no host package or group change.
Only the lane SDK is mounted; host networking exposes adb and the isolated peer.
The Dockerfile installs runtime libraries inside the disposable test image.

```sh
docker build -f testing/Emulator.Dockerfile -t wingthing-android-emulator-local .
```

Set `ANDROID_AVD_HOME`, `ANDROID_USER_HOME`, `ANDROID_EMULATOR_HOME` and
`ANDROID_SDK_ROOT` to lane SDK directories when running it. The tested emulator
flags are `-avd wingthing-api35 -no-window -no-audio -no-boot-anim -gpu swiftshader
-memory 1536 -cores 2 -no-snapshot -no-metrics`.

With the lane SDK already installed, from the repository root:

```sh
source mobile/android/toolchain.sh
go build -o "$WT_LANE/sdk/bin/testpeer" ./internal/phonepair/testpeer
```

In one terminal, keep `"$WT_LANE/sdk/bin/testpeer" -listen 127.0.0.1:38473`
running with stdin open. It uses disposable state and accepts fixture controls
only on loopback. In another terminal, after the emulator finishes booting:

```sh
cd mobile/android
source toolchain.sh
adb shell getprop sys.boot_completed  # must return 1
adb shell input keyevent 82
adb shell settings put global window_animation_scale 0
adb shell settings put global transition_animation_scale 0
adb shell settings put global animator_duration_scale 0
./verify-android.sh
```

The suite contains nine instrumented tests, including a real 301-second wait
for the wing's five-minute deadline. Allow the first boot to settle: a System UI
ANR can obscure screenshots even when Compose assertions pass. The screenshot
tests reject ANR dialogs, and the camera test requires its prompt to be visible.
The final camera captures also verify Android 15+ system-bar padding. Fixtures
use a separate test activity; production windows retain FLAG_SECURE.
Instrumentation exports only these fixture PNGs into
`/sdcard/Download/wingthing-evidence`, so Gradle's test-app uninstall cannot delete
them. `verify-android.sh` clears that fixture directory before running and pulls
it into the lane's `evidence/screenshots/` afterward.

Fresh Android 17 Google APIs images can spend several minutes in package
verification on this memory-constrained host. For this disposable emulator only,
`adb root` allowed setting first-boot/OTA package optimization to `skip`, then
restarting the framework. App install optimization is `verify`; actual app code,
Keystore operations, native crypto, Android permissions and instrumentation still
run on the real Android runtime. No phone or wing configuration is changed.

The screenshot tests use known fixture data in the actual Compose screens. Camera
evidence uses the actual ZXing capture activity with an emulated camera. It does
not establish a real camera scan: that remains REQUIRED-UNRUN.

The API 37.0 revision 6 image with emulator 37.2.12 also aborts in
`mapper.ranchu` during color-buffer readback (`hasReadColorBufferDma` assertion).
Three-button navigation was tried, but the framework still restarted and installs
lost the package service. This is captured in lane evidence. UI screenshots and
protocol gates therefore use the KVM x86_64 Android 15/API 35 image instead; the
APK still compiles against 37.0 and targets API 37. Android 17 local-network
permission acceptance remains part of the physical-phone acceptance checks.
