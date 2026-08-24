# Wingthing security model

Reviewed: 2026-08-20

## Scope of the promise

Wingthing has three independent security boundaries:

1. The **egg sandbox** limits what an agent process can reach on the wing.
2. The **wing access policy** decides which principal may operate a local resource.
3. **Application-layer encryption** protects terminal and tunnel payloads while an
   optional relay carries them.

Do not collapse those into one claim. E2E encryption does not constrain a
same-user local process, and a local allowlist does not authenticate code served
by a compromised web service.

Local CLI and MCP access use authenticated Unix sockets and operating-system file
permissions. SSH attach uses OpenSSH's authentication, encryption, and host-key
verification. Neither path uses Wingthing's relay encryption.

## Hosted relay architecture

```text
browser/native client -- TLS --> wingthing.ai relay -- TLS --> wing
              \____________ application ciphertext ___________/
```

The shipped relay forwards application ciphertext for terminal content and
encrypted tunnel requests. During normal service operation it does not receive
the plaintext of terminal I/O, directory listings, session history, audit data,
egg configuration, or tunnel passkey assertions.

The relay still sees and controls routing metadata. It terminates TLS, serves the
browser application, authenticates accounts, and chooses which wing connection
receives a message. The threat model below distinguishes an honest-but-curious
relay from a fully compromised relay.

## Cryptography

Wingthing uses X25519 ECDH, HKDF-SHA256, and AES-256-GCM in two domains:

| Domain | Client key | Wing key | HKDF info | Content |
|---|---|---|---|---|
| PTY | Browser identity key, retained for the tab | Persistent `~/.wingthing/wing_key` | `wt-pty` | Keystrokes and terminal output |
| Tunnel | Browser/native client identity key | Persistent `~/.wingthing/wing_key` | `wt-tunnel` | Wing APIs and encrypted PTY controls |

Encrypted messages are `base64(nonce || ciphertext || GCM tag)`. A modified
ciphertext fails authentication.

The browser and native tunnel client pin the first public key seen for a wing ID
and reject later changes. This is trust on first use (TOFU): it detects accidental
rotation and later control-plane substitution while trusted client code is still
running. It does not make a malicious first connection safe. Browser pins live in
site storage; native CLI pins live in `~/.wingthing/known_wings.json`.

### No forward-secrecy claim

The client key changes, but the wing's X25519 key is persistent. Someone who
records an old exchange and later steals `wing_key` can derive that old session's
key. The current protocol therefore provides unique per-client/per-tab keys, but
not forward secrecy against later wing-key compromise. Forward secrecy requires
signed ephemeral wing session keys or an authenticated handshake such as Noise.

### Encrypted and visible controls

Terminal input/output, resize, and kill operations are encrypted. The wing rejects
plaintext resize and kill messages received from a relay. P2P resize may travel
directly over WebRTC's authenticated DTLS channel.

Session start/attach routing, session IDs, timing, sizes, disconnects, and lifecycle
messages remain visible to the relay. The protocol does not yet bind every envelope
field into AEAD associated data or maintain a per-direction replay counter.

## Locked-wing passkeys

`wt wing lock` requires at least one valid passkey public key pinned in the local
`wing.yaml`. If no key is present, the explicit local lock command fetches the
logged-in user's registered key and pins it; locking fails if that cannot be done.
Runtime relay envelopes never auto-enroll a key into a locked wing.

Tunnel authentication uses a two-step ceremony:

1. `passkey.auth.begin` asks the wing for a challenge.
2. The wing creates a random one-time challenge bound to the relay user ID and the
   client's X25519 public key.
3. The browser performs a `webauthn.get` ceremony with user verification required.
4. `passkey.auth.finish` returns the assertion through the encrypted tunnel.
5. The wing consumes the challenge once and validates the challenge, type, origin,
   RP ID hash, cross-origin flag, user-presence and user-verification flags, P-256
   signature, and locally pinned key ownership.
6. The issued token is bound to the same user ID and X25519 client key.

PTY start and reattach use the same validation rules with fresh wing-generated
challenges. Locked starts, controller reattaches, and spectator attaches are
rejected when the relay user has no matching locally pinned key.

Tokens are held only in wing memory. A restart revokes them; `auth_ttl` can impose
an earlier expiry. A token issued to one browser key or relay user does not work
for another.

Manage local approval with:

```bash
wt wing lock
wt wing allow --email user@example.com
wt wing allow --all
wt wing revoke user@example.com
wt wing unlock
```

The relay stores passkey public keys and credential IDs for account registration
and explicit local approval flows. Public keys are not secrets, but key data read
from the relay is trusted only when a person deliberately pins it locally. For a
high-assurance deployment, verify the key over another trusted channel.

## Local CLI and MCP authority

Each egg socket and token is readable only by the local OS user. Processes running
as that user can bypass MCP and invoke `wt` directly, so local MCP principals are
an accident-prevention and attribution boundary, not hostile same-UID isolation.

`wt mcp stdio --client NAME` records NAME on sessions it creates. Named MCP clients
see and control only their sessions; the human CLI still sees all sessions. Every
MCP tool call appends a timestamp, principal, tool, target, decision, and argument
digest to `~/.wingthing/mcp-audit.log`. `~/.wingthing/clients.yaml` can require an
explicit client, restrict grants, and bound sessions/spawns. Real isolation between
hostile local clients still requires different OS users or client sandboxes.

On a dedicated sandbox VM, `wt egg ... --unsandboxed` and
`wt mcp stdio --unsandboxed` explicitly make the outer VM the agent boundary.
Wingthing keeps terminal persistence and the control/audit plane but applies no
nested filesystem, network, syscall, or resource restrictions. The MCP server
announces `outer-boundary` mode and records it on every audit entry. Because the
agent has the authority of the wing's OS user, it is a trusted endpoint for the
encryption model: it may read Wingthing state or invoke local control paths. E2E
encryption protects bytes from the relay, not from a compromised client or wing.

## What the relay observes

The relay can observe:

- Account, organization, device-token, and passkey registration records.
- Wing ID/public key, org binding, lock state, and connection presence.
- Session IDs, selected agent and working-directory metadata needed by PTY routing.
- Start/attach/detach/exit timing, message sizes, and IP/network metadata.
- Traffic availability: it can delay, drop, duplicate, or reroute messages.

With the as-built trusted client and wing, the relay does not receive plaintext:

- Terminal keystrokes or output.
- Encrypted resize/kill requests.
- Tunnel directory/session/audit/config payloads.
- Tunnel WebAuthn assertions and issued auth tokens.

Plaintext terminal and task history exists on the wing for replay and durability.
Wingthing does not currently provide application-level encryption at rest; use OS
disk encryption when that threat matters.

## Relay-compromise threat model

An attacker controlling the hosted relay can:

- Read its database and all listed metadata.
- Forge account/JWT/control-plane identity, impersonate a wing route, or cause DoS.
- Present a false wing key on first use.
- Serve modified browser JavaScript that reads plaintext before encryption or uses
  an authenticated browser as a decryption oracle.
- Start sessions on an unlocked wing as an account the relay claims is authorized.

Consequently, the hosted browser application does **not** provide a
malicious-service-resistant E2EE guarantee. TOFU pinning and locked-wing passkeys
meaningfully protect against later database/routing errors and a relay binary that
cannot modify already trusted client code, but they cannot protect against malicious
JavaScript served by the same compromised service.

A native, reproducibly distributed client with an independently verified wing pin
can make the malicious-relay boundary substantially stronger. Until that handshake
and distribution story is complete, public wording should be "application-encrypted
through the hosted relay," not a Signal-style server-compromise guarantee.

## Remaining protocol work

- Signed ephemeral wing session keys for forward secrecy.
- A user-verifiable pairing/fingerprint flow instead of TOFU alone.
- AEAD associated data binding wing, session, message type, direction, and request ID.
- Monotonic per-direction counters or another replay-defense construction.
- Persisted WebAuthn signature counters if cloned-authenticator detection becomes
  part of the passkey guarantee.
- A trusted native/browser-extension client path if malicious web delivery is in scope.
- At-rest protection for local history if Wingthing chooses to offer that guarantee.

## Reference

| Surface | Current protection |
|---|---|
| Local CLI/MCP | Same-OS-user permissions; named MCP guardrails and audit |
| Trusted VM (`--unsandboxed`) | Outer VM/network policy; no nested Wingthing sandbox |
| SSH attach | OpenSSH transport and host-key policy |
| Terminal content through relay | X25519/HKDF/AES-GCM application encryption |
| Tunnel wing APIs | X25519/HKDF/AES-GCM application encryption |
| Resize/kill | Encrypted tunnel or direct WebRTC DTLS |
| Locked-wing authentication | Wing-generated WebAuthn challenge, local key, client-bound token |
| Relay metadata | Visible by design |
| Browser code integrity | Trusted-service assumption |
| Forward secrecy | Not currently provided |
| Local history at rest | Plaintext on the wing |
