# Wingthing Android

Built from `145bba47c03fee98df73f897ca6317437083aba7` on
`codex/wt-android-20261011`. Scope is the phone flow in the whole-app spec:
independently pair wings, see attention, attach, type, stop, detach and forget.
No phone login, billing, cloud provisioning, push, or configuration authority.

## Crypto decision: (a) gomobile

The branch has a server and crypto primitives, but no exported Go phone client.
`bridge/client.go` adds the client and binds it as an AAR. It reuses
`internal/pake` (the pinned, symbol-prefixed BoringSSL SPAKE2),
`phonepair.Identity`, `PairKey`, `EmojiFromShared`, and `internal/auth`'s
X25519/HKDF/AES-GCM. There is no Kotlin curve arithmetic or replacement PAKE.
The only native build change excludes Linux's separate pthread/stdlib link flags
on Android and links NDK libc++ dynamically. `build-bridge.sh` includes the matching
NDK `libc++_shared.so` in the AAR: gomobile omits that runtime itself, and an AAR
without it builds but fails to load on Android. The resulting AAR contains arm64-v8a
for phones and x86_64 for the emulator. Go's ephemeral keys stay in memory.

`Wire` sends opaque `phone.box` JSON. Android cannot inspect plaintext protocol
messages in transport. Each request gets a fresh carrier X25519 key with domain
`wt-phone-carrier-v1`. Pairing and device envelopes retain the exact Go/Swift
identities, transcript order, standard padded base64, GCM nonce/ciphertext/tag,
emoji vocabulary, DER ECDSA signature and sequence binding. Returned pins and
emoji are checked before enrollment approval. Either-side approval alone gives
no credential. Protocol failures close access; commands are never replayed.

## Storage and lifecycle

Each pairing gets an independent non-exportable Android Keystore P-256 signing
key. StrongBox is requested when available, with fallback only for
`StrongBoxUnavailableException`. ECDSA signatures are made by the Keystore, not
by Go. A separate Keystore AES-GCM key encrypts the complete saved profile list
(including per-wing credential, pin and alias) with fixed version AAD. AtomicFile
commits it under `noBackupFilesDir`; Android backup is disabled. Forget removes
the profile and signing key. Re-pairing replaces the old key only after successful
storage. Backgrounding closes the connection, clears session content and releases
writer leases; returning requires an explicit reconnect. No credentials, QR
secret, private key or terminal content are logged. Production windows use
FLAG_SECURE; instrumentation screenshots use a separate test activity and fixtures.

## Transport

`PhoneWire` owns LAN → NSD recovery → tailnet → registered HTTPS relay order.
NSD accepts only `_wingthing-phone._tcp.` with the hashed instance name, v=1,
exact WingID and pin. IPv4 private/link-local/loopback/tailnet and .local/.ts.net
HTTP origins are runtime restricted by the Go bridge; credentials, URL query,
fragment and paths are rejected. Android's dynamic QR-supplied LAN origins require
`usesCleartextTraffic`; only encrypted phone boxes use those validated origins.
Redirects, cookies, proxy selection and automatic retries are disabled. Established
commands stay on the selected carrier. Initial handshakes reset to LAN. A malformed
or unauthentic response is a hard stop, not a route failure. The relay uses
accountless `/ws/phone?wing_id=...` and validates request IDs and bounded responses.
Android 17's local-network permission is requested before connection or scanning.

## Terminal license and integration

Vendored only `terminal-emulator` and `terminal-view` Java/resources from
[termux/termux-app revision 8629e632fcb95da272221be327db653fb24befe9](https://github.com/termux/termux-app/tree/8629e632fcb95da272221be327db653fb24befe9).
The upstream [license statement](https://github.com/termux/termux-app/blob/8629e632fcb95da272221be327db653fb24befe9/LICENSE.md)
explicitly identifies these two libraries as Apache 2.0. The full Termux app is
GPLv3 and is not included. See `TERMUX-UPSTREAM-LICENSE.md`,
`LICENSE-APACHE-2.0.txt`, and `THIRD-PARTY-NOTICES.md`.
The remote view uses Termux's maintained ANSI emulator and canvas renderer;
no local shell or PTY is launched. Raw output retains ANSI styling and cursor
movement; the wing's plain screen is used for truncated recovery. Clipboard
escape requests and terminal-generated query replies are ignored. User input
uses explicit Send/Tab/Ctrl+C actions. Scrollback is bounded.

## Toolchain and reproducibility

JDK 21, Android tools, platform 37.0, build-tools 37.0.0, NDK 29 and Gradle 9.3.1
live under the lane's `sdk/`. API 37 is the current stable major target:
[Android 17 release](https://developer.android.com/blog/posts/android-17-is-here),
[AGP 9.1.1 compatibility](https://developer.android.com/build/releases/agp-9-1-0-release-notes),
[local-network permission](https://developer.android.com/privacy-and-security/local-network-permission).
Use `source toolchain.sh`, `./build-bridge.sh`, `./build-release.sh`.
Gradle uses max four workers and a 3 GiB heap. Signing files are new lane-local
files, outside Git, with mode 0600; build scripts read the password from that file.

`internal/phonepair/testpeer` has optional loopback fixture controls for Android
interop only. It always uses a temporary state directory and rejects non-loopback
listeners. It never touches real wing state. Host Go race tests cover crypto and
server transitions; Android tests exercise the actual AAR, Keystore and HTTP boxes.
Screenshot fixtures exercise the same Compose screen functions in both themes,
and are identified as fixtures rather than a real phone acceptance claim.

## Physical acceptance

REQUIRED-UNRUN: a real phone camera scan of `wt pair`, both-side approval and
terminal input/stop on the Mac. Hardware StrongBox, a real NSD LAN address change,
Tailscale across networks and production relay reachability also need physical
acceptance. The host build and simulator tests cannot establish those outcomes.
