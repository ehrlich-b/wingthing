# Wingthing phone pairing v1

The contract is `/Users/ehrlich/repos/claude/state/drive/wingthing-pairing-spec.md` (11 October 2026). Pair Bryan's iPhone to **EhrlichMac's main wing** first; pair **forge's wing** separately. A phone grant permits session inventory, attachment, input and stop on precisely one wing. It confers no account, configuration, enrollment, shell-launch or other-machine authority. `wt pair` must contact the running wing's local control endpoint, not start a second wing with a new identity.

This document specifies the implementation target. The storyboard is a visual specification; simulator tests do not establish camera or cellular acceptance.

## Cryptographic dependencies

Use **BoringSSL's SPAKE2 implementation**, not a new PAKE implementation. Vendor the symbol-prefixed source shipped by Apple's Swift Crypto at revision `1c80d3aff53fedc445b60692980cb4f19a618657`, derived from BoringSSL `0226f30467f540a3f62ef48d453f93927da199b6`. Go and Swift call the same `SPAKE2_CTX_new`, `SPAKE2_generate_msg` and `SPAKE2_process_msg` library APIs. This is BoringSSL's Ed25519 SPAKE2 dialect; do not claim wire compatibility with arbitrary RFC 9382 implementations. Each context is used once and freed. BoringSSL is a maintained cryptographic library used by Chromium and Swift Crypto; this integration still needs independent review before a public security claim.

Primary references: [BoringSSL SPAKE2 API](https://commondatastorage.googleapis.com/chromium-boringssl-docs/curve25519.h.html), [Swift Crypto source](https://github.com/apple/swift-crypto/tree/1c80d3aff53fedc445b60692980cb4f19a618657), [BoringSSL security policy](https://github.com/google/boringssl/blob/main/SECURITY.md). AES-256-GCM, X25519, HKDF-SHA256, HMAC-SHA256 and P-256 signing use Go standard crypto / x/crypto and Apple CryptoKit. No curve arithmetic, password exchange or cipher is implemented here.

## Invitation and transport

The wing already has a persistent X25519 `wing_key` and WingID. Its SHA-256 fingerprint is the full lowercase hex digest of the decoded 32-byte public key. The QR is `wingthing://pair#` followed by percent-encoded compact JSON:

```json
{"v":1,"wing_id":"persistent-wing-id","name":"EhrlichMac","key":"base64-X25519-key","local":"http://192.168.1.40:6078","relay":"https://wingthing.ai","id":"random-invitation-id","secret":"six randomly selected words"}
```

The secret is in the fragment, never an HTTP URL/query. Six independent words from a fixed 2048-word vocabulary provide 66 bits of entropy. Show the same six words in the terminal as the fallback code. A fallback additionally needs the invitation's address and public identity; the app's hidden paste-link route carries these together. A secret alone cannot identify a machine on an arbitrary network.

The invitation lives only in the running wing's memory. Deadline is exactly creation + 300 seconds, enforced by the wing's clock at every transition. An attempted PAKE claims it atomically; no second phone may claim it. Wrong proof, cancel, timeout, successful pairing and terminal exit invalidate it. Enter creates a fresh invitation, never extends an old secret. Limit invitation count, attempts, message size, concurrent connections and idle duration. Local administration uses a loopback endpoint protected by a mode-0600 random control token; remote phone endpoints never accept that token.

On Wi-Fi, connect directly to the QR's LAN endpoint. HTTP is a carrier for authenticated PAKE and E2E ciphertext, not a confidentiality boundary. Only a literal private/link-local IP address or `.local` hostname is permitted for local transport; disable redirects and cookies. The iOS local-network permission and narrow local-network ATS exception are required. Never downgrade a failed pinned-key check into another connection attempt.

Away from home, use `/ws/phone?wing_id=<routing-id>` at the explicitly scanned HTTPS relay origin. The wing must be registered there by `wt login` once. The phone sends no account bearer, cookie or phone login. The relay forwards bounded `tunnel.req` messages with public purpose `phone` to that wing and correlates `tunnel.res` replies; it never injects account authority for phone messages. Existing hosted admission still applies to the wing. Phone grants are validated at the wing even if the relay is compromised. A future tunn carrier must preserve the same protocol and pin.

## PAKE and two-sided confirmation

All binary fields below are standard padded base64. Transport requests are JSON objects; errors are stable codes (`expired`, `used`, `cancelled`, `identity_mismatch`, `bad_proof`, `revoked`, `unreachable`, `denied`). Do not reflect secrets in errors or logs.

1. `pair.start {id, message}`: phone creates SPAKE2 Alice; wing uses Bob. Alice identity is `wt-phone-v1`; Bob identity is `wt-wing-v1:<wing_id>:<full fingerprint>:<invitation id>`. Both use the exact UTF-8 six-word secret. `message` is Alice's library message. The wing claims the invitation and returns `{message, key, name, expires_at}` with Bob's library message. The phone compares the returned key to the scanned key BEFORE continuing. A mismatch is a red hard stop.
2. Both library contexts process the peer's message. Build the transcript as UTF-8 `wt-pair-v1\n<wing_id>\n<key>\n<id>\n<Alice message>\n<Bob message>`. HKDF-SHA256 of SPAKE2 output, salt SHA-256(transcript), info `wt-pair-v1`, yields a 32-byte AES-GCM key. Nonces are fresh random 12-byte values; encode `nonce || ciphertext || tag` as base64. The encrypted plaintext binds its operation and invitation ID.
3. `pair.prove {id, payload}` encrypts `{op:"prove", id, name:"Bryan's iPhone", public_key:"base64 P-256 X9.63 key"}`. The wing validates the key/name and possession of the PAKE key. It returns an encrypted `{op:"proved", id, emoji}`. The four emoji are the first four bytes of HMAC-SHA256(pair key, `wt-emoji-v1`) mapped into a fixed ordered 256-entry emoji vocabulary. This binds the emoji to the PAKE transcript, wing pin and invitation. Only then show the request in the terminal.
4. The phone offers **Emoji match · Pair** and **Cancel · emoji differ**. Its explicit approval sends encrypted `pair.confirm {op:"confirm", id, emoji}`. A mismatch cancels the invitation. Local terminal Enter sets an independent approval bit; the phone cannot set it. Terminal Ctrl-C cancels. Approval on one side alone never issues a credential. The phone may query encrypted `pair.status {op:"status", id}` while waiting for Enter.
5. Once both approvals exist, persist `{device_id, name, public_key, credential_hash, paired_at, last_seen}` with an atomic mode-0600 write before sending success. Issue a random 32-byte credential, encrypted as `{op:"paired", id, wing_id, key, device_id, credential}`. The raw credential is never written on the wing. The completed response is retrievable only under that invitation's PAKE key for its remaining lifetime, so a dropped final response can be recovered without creating another grant. The secret is already consumed. Duplicate confirms never issue a second device.

## Device connections and sessions

Generate a persistent P-256 signing key per pairing on the phone. Use Secure Enclave when supported, otherwise CryptoKit with a non-synchronizing, `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` Keychain item. Store the opaque device credential in a separate device-only Keychain item scoped by WingID and pinned wing key. Public machine names, endpoints, pins and device IDs may be stored in Application Support. Never place a secret or private key in UserDefaults, profile JSON, setup URLs, diagnostics or the clipboard. Each pairing is independent. X25519 connection keys are ephemeral and remain in memory.

For each connection, phone creates ephemeral X25519 and derives AES-GCM to the scanned persistent wing key with the existing `wt-phone-v1` HKDF domain (32 zero salt bytes). `device.hello {sender_pub}` returns an encrypted random challenge and random connection ID. The phone signs UTF-8 `wt-device-v1\n<wing_id>\n<connection_id>\n<challenge>\n<sender_pub>` with its stored P-256 key. It sends encrypted `device.auth {device_id, credential, signature, connection_id}`. The wing checks credential hash in constant time, signature, fresh challenge, live device record and exact wing binding. Challenge is single-use, expires in 30 seconds and authentication failure closes the connection.

Authenticated requests and replies carry the connection ID and strictly increasing sequence number INSIDE ciphertext. Reject replay, duplicate sequence, wrong wing, mismatched response ID and oversized input. An RPC permits only `sessions`, `attach`, `read`, `input`, `stop`, `detach` and `close`. Inventory returns name, status and blocked/unread attention. Attach resolves an existing session on this wing and obtains the existing egg controller/observer capability; commands use the existing wing session service. There is no phone method for configuration, pairing administration, credentials, billing, file browsing or another wing. Revocation is checked on every command and before delivering stream output. Idle connections expire; disconnect cancels attachment streams and releases writer leases. Never automatically replay input after reconnect.

## Administration and revocation

`wt devices` talks to the same main wing over local authenticated control. It lists paired phone name, paired-at and last seen. `wt devices revoke <name>` resolves exactly one active name; ambiguous names require the device ID. Under the same registry lock that admits commands, persist revocation and cancel all connections/attachments for that device before acknowledging success. New commands fail `revoked`; a relay cannot revive a revoked credential. Re-pairing requires a new QR and both confirmations. On loss, revoke separately on EhrlichMac and forge. Cached content on a lost phone cannot be remotely erased by this protocol.

## Threat model and visibility

| Party / attack | Boundaries |
| --- | --- |
| Wing | Knows device public key, name, credential hash and commands/content for its own sessions. Its existing persistent key never leaves it. |
| Phone | Knows credentials and content only for independently paired wings. Possession does not grant machine configuration or enrollment rights. |
| Relay / hostile Wi-Fi | Sees routing IDs, IPs, connection timing/sizes and PAKE public messages. No QR secret, device credential, phone name, session metadata or content. Can drop/delay/replay traffic; AEAD, transcript binding, challenges and counters detect substitution and replay. Availability is not guaranteed. |
| QR photograph | Gives temporary invitation knowledge, which must still pass the terminal's name/emoji check and phone approval. After use or 300 seconds it is useless. A photograph taken before expiry can race the real phone and cause denial of service; the terminal must never auto-approve. |
| Key substitution | PAKE identities/transcript and direct key comparison bind the scanned persistent pin. Changed pin always stops; no trust-on-first-use replacement. |
| Lost phone | Revocation closes live access and denies future requests. A thief with an unlocked phone before revocation has the phone's granted session authority. |
| Compromised wing/phone | Out of scope for confidentiality of content already present there. Revocation cannot retract cached content, undo commands already executed or terminate an input accepted before revocation acquired the admission lock. |

A machine name is a display label, never an authorization selector. The persistent key and WingID identify the wing. A malicious QR can name a different machine “EhrlichMac”; Bryan's trusted terminal and two-sided check are the out-of-band authority. Never claim simulator acceptance proves a real QR camera scan, Keychain/Secure Enclave behavior on Bryan's hardware, or cellular reachability. Those remain required physical-phone checks.
