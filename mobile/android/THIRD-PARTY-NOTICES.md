# Third-party notices

- Termux `terminal-emulator` and `terminal-view`, revision
  `8629e632fcb95da272221be327db653fb24befe9`: Apache License 2.0, based on
  Terminal Emulator for Android (Jack Palevich and contributors), with Termux
  contributions. Vendored sources/resources retain upstream notices. The
  repository-wide GPL Termux application is excluded. See the copied upstream
  license statement and Apache license text.
- BoringSSL through Swift Crypto's prefixed sources: inherited unchanged from
  `internal/pake/boringssl`; license at `../../internal/pake/BORINGSSL-LICENSE.txt`.
- Go standard library: BSD-style Go license.
- Android NDK libc++ runtime: Apache License 2.0 with LLVM exceptions; see
  `NDK-NOTICE.txt` in the APK's assets for the NDK toolchain notices.
- AndroidX / Jetpack Compose: Apache License 2.0.
- ZXing Android Embedded and ZXing: Apache License 2.0.
- OkHttp and Okio: Apache License 2.0.
- golang.org/x/mobile and x/crypto: BSD-style Go license.

Terminal integration does not instantiate Termux's application or native shell
process code. No Termux application branding or assets are included.
