# Android debug qualification

Use Linux x86-64, Go 1.26.9 or newer, JDK 21, Android SDK platform 37,
build tools 37.0.0, and NDK 29.0.14206865. Set `WT_ANDROID_WORKDIR` to a
writable local directory and `JAVA_HOME` to the JDK. The default SDK is
`$WT_ANDROID_WORKDIR/sdk`; `ANDROID_HOME` and `ANDROID_NDK_HOME` can override
it. On forge2, when the local SDK and explicit Android SDK settings are absent,
`toolchain.sh` uses `/work/cache/android` for the SDK, JDK, and Gradle cache.
Set `WINGTHING_ANDROID_HOME` to select another shared cache with the same
layout. A local SDK or explicit SDK settings take precedence.
No release key is needed for `make check-android`.

Run `mobile/android/bootstrap-gomobile.sh` to install gomobile and gobind in
`$WT_ANDROID_WORKDIR/bin`. It pins the module revision and checks its Go module
and go.mod hashes; Go verifies dependencies through its module checksum system.
An existing `GOMODCACHE` can be used as an offline cache: set `GOPROXY=off`
and `GOSUMDB=off` when the module and all dependencies are already verified and
cached. The bootstrap serves the download cache as a local file proxy in this
mode. For an offline build, Gradle's dependency and wrapper caches must also be populated in
`GRADLE_USER_HOME`; the wrapper pins Gradle 9.3.1 and its SHA-256 in
`gradle/wrapper/gradle-wrapper.properties`.

Install the SDK packages into the local SDK using Android's sdkmanager, after
reviewing and accepting its licenses:

```
source mobile/android/toolchain.sh
sdkmanager --sdk_root="$ANDROID_HOME" 'platforms;android-37.0' 'build-tools;37.0.0' 'ndk;29.0.14206865'
```

Android's SDK manager verifies package archive checksums from Google's
repository metadata. For Linux, command line tools 23.0 are available at
`https://dl.google.com/android/repository/commandlinetools-linux-16111833_latest.zip`
(SHA-1 `e025545c62a8e64c7559119566a569fb1dec5f60`, from Google's repository
metadata). Verify the archive before unpacking it into a local SDK. Check your
JDK distributor's checksum before unpacking a JDK. The preflight reports missing
or incompatible components; it does not download software or accept licenses.
Run `make check-android` after populating the offline Gradle cache. Release
signing is optional; see [README.md](README.md) for unsigned builds and the
separate `sign-release.sh` step.

One Linux x86-64 JDK pin is Temurin 21.0.13+9:
`https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jdk_x64_linux_hotspot_21.0.13_9.tar.gz`
(SHA-256 `d7f5d5cf865a2902b70ff549b97586a7d3f234e016f041ab447ea740c7d66d1c`).
For the corresponding Google Linux SDK archives, the repository metadata lists
SHA-1 `ed8ebf7f8822a4de5686d427f237d2fa30ff7410` for platform 37.0 revision 2,
`70954e99f4c3d9d46ee70fa32624672fe7cd6ebe` for build tools 37.0.0, and
`87e2bb7e9be5d6a1c6cdf5ec40dd4e0c6d07c30b` for NDK 29.0.14206865.

Offline runtime artifacts from Google's Maven repository:

- `https://dl.google.com/dl/android/maven2/androidx/lifecycle/lifecycle-common-java8/2.10.0/lifecycle-common-java8-2.10.0.jar` — SHA-256 `497dd84de9f2fd46324f25f9a3af032d3a23d087e87af515c4b19435f6d6a651`.
- `https://dl.google.com/dl/android/maven2/androidx/concurrent/concurrent-futures/1.1.0/concurrent-futures-1.1.0.jar` — SHA-256 `0ce067c514a0d1049d1bebdf709e344ed3266fe9744275682937cdcb13334e9e`.
- `https://dl.google.com/dl/android/maven2/androidx/collection/collection-ktx/1.5.0/collection-ktx-1.5.0.jar` — SHA-256 `939b4a8697640e77d83e4e8b4c4a9d18f8130198c5961194af8dd9db9dc7c353`.
