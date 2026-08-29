# Wingthing security model

Reviewed: 2026-08-28

## Scope of the promise

Wingthing has three independent security boundaries:

1. The **egg sandbox** limits what an agent process can reach on the wing.
2. The **wing access policy** decides which principal may operate a local resource.
3. **Transport encryption and signaling integrity** protect direct WebRTC payloads;
   application-layer encryption protects payloads when an optional relay carries them.

Do not collapse those into one claim. E2E encryption does not constrain a
same-user local process, and a local allowlist does not authenticate code served
by a compromised web service.

Local CLI and MCP access use authenticated Unix sockets and operating-system file
permissions. SSH attach uses OpenSSH's authentication, encryption, and host-key
verification. Neither path uses Wingthing's relay encryption.

## Local self-hosted HTTPS

`wt serve --local --https` and `wt roost start --https` add an HTTPS browser
listener at `https://localhost:8443`. The flag is explicit because it performs a
local trust-store change:

1. WT creates a private ECDSA P-256 CA key and localhost server key on demand in
   `~/.wingthing/local-tls`.
2. The directory is mode `0700`; both private keys are mode `0600`.
3. The CA is name-constrained to `localhost`, `127.0.0.0/8`, and `::1`.
4. WT installs only the public CA certificate in the current user's trust store.
   Neither private key is passed to a trust command, copied to a wing, or sent over
   the network.

On macOS this is the user's login keychain and macOS displays its native
Certificate Trust Settings authorization dialog. On Windows it is the current
user's root store. On Linux WT uses the current user's Chromium NSS database;
`certutil` must be available from the distribution's `libnss3-tools` or
`nss-tools` package. These operations do not require a system-wide root store.

Use `wt local-cert status` to inspect the paths and verified trust marker. Use
`wt local-cert remove` to remove the public root from the current user's trust
store. Removal deliberately leaves the local key material intact so WT does not
silently replace a previously trusted authority.

HTTPS mode refuses wildcard, LAN, and public listener addresses. It uses two
loopback listeners: port 8443 for the browser and HTTP port 8080 for embedded
wings or wings arriving through an SSH reverse tunnel. That HTTP hop never leaves
loopback; the SSH tunnel protects the host-to-host segment, and Wingthing's
application encryption still protects browser-to-wing terminal payloads.

This device-local CA is not used by hosted, organization, or public shared-roost
deployments. Those retain their existing externally provisioned HTTPS termination
and OAuth behavior. Local HTTPS is opt-in. Without it, a single-user/no-login
portal remains HTTP but its implicit listener is loopback-only and an explicit
LAN or wildcard address is rejected; authenticated deployments retain their
configured listener behavior.

Local mode also accepts only localhost/loopback Host headers. Browser WebSockets
must be same-origin, and unsafe browser methods with an Origin header must match
the exact HTTP or HTTPS origin. Requests marked cross-site by browser fetch
metadata are rejected even if Origin is absent. Native wing and CLI calls do not
send browser Origin metadata and remain available over the loopback transport.
These are DNS-rebinding and browser-CSRF defenses; they do not make a no-login
listener safe to expose beyond loopback.

## Browser-local state and agent previews

For fast terminal restore and session-card thumbnails, the browser stores up to
200,000 serialized terminal characters and a WebP thumbnail per session in
plaintext origin `localStorage`. It also caches inventory, layout, and wing-key
pins there. This data does not go into the gateway database, but it is readable by
the browser profile, extensions with site access, and any script that can execute
in the portal origin. Clear the site's browser data to remove it. Local wing
history is a separate plaintext-at-rest store described below.

Agent-authored Markdown previews are rendered as network-inert text: raw HTML is
escaped, links are not active, image syntax does not fetch, scripts are disabled,
and the iframe has no normal origin. An agent-authored absolute HTTP(S) preview URL
is displayed without being requested. Every new URL requires the user to choose
**load preview** or **open**. Once chosen, the request originates from the browser
and is outside the egg's network allowlist; the destination sees the request and
network metadata. The embedded iframe uses `allow-scripts` without
`allow-same-origin` and a no-referrer policy so an agent-selected page or redirect
cannot become same-origin with the portal.

## Hosted connection architecture

The free/default path uses the hosted service only for identity, the
access-filtered wing directory, and encrypted WebRTC signaling:

```text
native client ===== authenticated WebRTC/DTLS ===== wing
       \------ wingthing.ai coordination only -----/
```

The WebRTC certificate fingerprints are inside the offer/answer exchanged
through the X25519-encrypted, TOFU-pinned signaling tunnel. MCP request and
result bytes then travel on the DataChannel, not through `wingthing.ai`.

Accounts with hosted relay access and self-hosted policies may use the browser relay path:

```text
browser -- TLS --> wingthing.ai relay -- TLS --> wing
        \_________ application ciphertext ________/
```

When entitled, the shipped relay forwards application ciphertext for terminal
content and encrypted tunnel requests. During normal service operation it does not receive
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
| Tunnel/signaling | Browser/native client identity key | Persistent `~/.wingthing/wing_key` | `wt-tunnel` | Wing APIs, encrypted PTY controls, and WebRTC SDP |
| Direct control | Ephemeral WebRTC certificate negotiated in pinned signaling | Ephemeral WebRTC certificate | WebRTC DTLS | Remote MCP operations and results |

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

For a direct-only hosted account, the relay accepts only bounded tunnel purposes
for wing discovery, WebRTC signaling, and passkey authentication. The wing checks
the coordinator-visible purpose against the decrypted inner message before it
responds. General tunnel control and PTY start/attach are denied before forwarding.
The wing advertises this purpose-binding capability at registration; direct-only
coordination to an older wing is denied with an upgrade error.

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

## Native direct MCP authority

The wing derives native direct authority from the coordinator-authenticated user,
organization role, wing-local admin list, configured paths, and the local
`direct_mcp` policy. The caller cannot supply its own principal, role, grants, or
paths in a tool request.

The compatible default grants every operation currently reviewed for the direct
surface, but does so with a non-nil explicit grant set and positive wing-enforced
bounds: eight live direct-MCP terminal sessions and sixty spawns per hour per
principal. The rolling spawn window is shared across reconnecting data channels in
one wing process. It is a process-lifetime guardrail, not a durable billing quota.

Operators can narrow the direct surface in `wing.yaml`:

```yaml
direct_mcp:
  allow_grants: [capabilities.read, terminal.read, agent.read]
  max_sessions: 4
  max_spawns_per_hour: 20
```

Use `deny_grants` instead of `allow_grants` to subtract from the compatible default,
or set `disabled: true`. Allow and deny cannot be combined. Unknown fields, unknown
grant names, negative or excessive bounds, and malformed policy fail wing startup or
SIGHUP reload rather than falling back to unrestricted direct access. Omitting the
entire section preserves existing `wing.yaml` files.

Organization owners and wing-configured admins can use every configured path.
Ordinary members see only legacy-open paths and paths tagged with their email; their
sessions are owner-scoped and use the sealed shared-host boundary. Existing OAuth
shared-roost identities with an empty organization role retain member privilege,
while an empty role outside shared-roost mode fails closed.

Coordinator-derived user and organization identity has a 15-minute maximum lifetime
on a direct data channel. The wing closes the channel when that lease expires, so
continued use requires a fresh access-filtered discovery and signaling exchange.
This bounds the effect of organization membership revocation. Wing-local lock,
passkey, grant, path, and bound changes are evaluated on every request, including on
an already-open channel after a successful `SIGHUP` reload.

The authenticated shared-roost HTTP MCP adapter uses the same explicit reviewed
wing-operation grant set and the same default per-user bounds. Its admission state is
shared across OAuth clients and requests in the roost process, so reconnecting or
switching between Codex and Claude does not reset the spawn window.

For a private OAuth gateway or all-in-one roost, `WT_ROOST_ALLOWED_EMAILS` is a
separate enrollment boundary. Exact, case-insensitive email matches are enforced when a browser login
finishes and again for cookies, device tokens, the wing inventory, relay access,
and MCP authorization. OAuth login requires the provider's current verified email
to match. OAuth proves that a provider controls an identity; it does not make every
identity accepted by that provider a roost member. If the list is empty, any
identity accepted by the provider can enroll. Internet-reachable roosts therefore
need either an explicit list or an equivalent restriction at the provider or
ingress.

## Hosted relay opt-out

Each wing can independently refuse hosted payload relay, even when its account has
hosted relay access or is connected to a self-hosted roost:

```yaml
hosted_relay: deny
```

Omitting the field is the compatibility value `allow`. The setting is restart-bound
so registration, session reclaim, and message handling cannot observe different
policies in one daemon run. Use `wt wing config set hosted_relay=deny`, then restart
the wing.

An honest gateway checks the advertised policy before forwarding terminal starts,
attaches, input, terminal authentication/control, or general encrypted tunnel
payloads. The wing client also suppresses outbound session-attention metadata. It
enforces the same decision locally, so an old, stale, or
modified coordinator cannot cause those handlers to run. Bounded WebRTC discovery,
signaling, and passkey coordination remains available; the wing decrypts each request
and verifies that the declared purpose matches the inner message type.

Authorized wing roster entries and encrypted `wing.info` responses report the
effective `hosted_relay` value. Gateway denials create a content-free audit entry with
actor, wing, operation, and policy; wing-local denials append operation and policy to
`~/.wingthing/policy-audit.log` with mode `0600`. Neither record includes command,
working directory, terminal bytes, or tunnel payload.

This opt-out prevents payload handling by conforming gateway and wing binaries. It
does not make hosted browser JavaScript safe against a malicious service, and an old
gateway can still observe a connection attempt before the new wing rejects it. Use a
native client over a private network or a self-hosted coordinator when the hosted
service itself is outside the trust boundary.

`hosted_relay: deny` is not a downgrade-compatible security policy. A wing binary
from before this field existed ignores it and resumes the historical relay behavior.
Do not roll such a wing back while this setting is part of the security boundary:
stop the wing or isolate it from the coordinator until a conforming binary is
restored. Rolling back only the gateway is different—the current wing still enforces
the denial locally, although the old gateway can observe the attempted connection.

On a dedicated sandbox VM, `wt egg ... --unsandboxed` and
`wt mcp stdio --unsandboxed` explicitly make the outer VM the agent boundary.
Wingthing keeps terminal persistence and the control/audit plane but applies no
nested filesystem, network, syscall, or resource restrictions. The MCP server
announces `outer-boundary` mode and records it on every audit entry. Because the
agent has the authority of the wing's OS user, it is a trusted endpoint for the
encryption model: it may read Wingthing state or invoke local control paths. E2E
encryption protects bytes from the relay, not from a compromised client or wing.

## What the relay observes

The coordinator can observe:

- Account, organization, device-token, and passkey registration records.
- Wing ID/public key, org binding, lock state, and connection presence.
- WebRTC signaling size/timing, candidate network metadata, and IP/network metadata.
- For entitled relay sessions only: session IDs, selected agent and working-directory
  routing metadata plus start/attach/detach/exit timing and message sizes.
- Traffic availability: it can delay, drop, duplicate, or reroute messages.

With the as-built trusted client and wing, the coordinator does not receive plaintext:

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
and distribution story is complete, direct-path wording must still disclose the
hosted client-distribution and first-use trust boundaries rather than imply a
Signal-style server-compromise guarantee.

## Remaining protocol work

- Signed ephemeral wing session keys for forward secrecy.
- A user-verifiable pairing/fingerprint flow instead of TOFU alone.
- AEAD associated data binding wing, session, message type, direction, and request ID.
- Monotonic per-direction counters or another replay-defense construction.
- Persisted WebAuthn signature counters if cloned-authenticator detection becomes
  part of the passkey guarantee.
- A trusted native/browser-extension client path if malicious web delivery is in scope.
- A native passkey ceremony before direct control of locked wings.
- At-rest protection for local history if Wingthing chooses to offer that guarantee.

## Reference

| Surface | Current protection |
|---|---|
| Local CLI/MCP | Same-OS-user permissions; named MCP guardrails and audit |
| Trusted VM (`--unsandboxed`) | Outer VM/network policy; no nested Wingthing sandbox |
| SSH attach | OpenSSH transport and host-key policy |
| Direct remote MCP | WebRTC DTLS; offer/answer protected by pinned encrypted signaling |
| Terminal content through relay | X25519/HKDF/AES-GCM application encryption |
| Tunnel wing APIs | X25519/HKDF/AES-GCM application encryption |
| Resize/kill | Encrypted tunnel or direct WebRTC DTLS |
| Locked-wing authentication | Wing-generated WebAuthn challenge, local key, client-bound token |
| Relay metadata | Visible by design |
| Browser code integrity | Trusted-service assumption |
| Forward secrecy | Not currently provided |
| Local history at rest | Plaintext on the wing |
