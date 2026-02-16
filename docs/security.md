# Wingthing Security Model

## Design Goal

The relay (roost) should never be able to read wing data. It routes encrypted bytes between browsers and wings without inspecting, logging, or tampering with them. This covers terminal I/O, directory listings, session history, audit recordings, egg configs, and passkey assertions. This document describes how that works, where the gaps are, and what happens if the relay is compromised.

## Architecture

```
Browser (app.wingthing.ai)
    |
    | WebSocket (TLS)
    |
Roost (wingthing.fly.dev)    <-- untrusted
    |
    | WebSocket (TLS)
    |
Wing (your machine, running `wt wing`)
```

The roost sits between the browser and the wing. All connections use TLS, but TLS only protects the transport - the roost terminates both TLS connections and could read plaintext at the application layer. E2E encryption prevents this.

## Three Security Domains

### The egg: protecting you from the agent

Agents run inside an OS-level sandbox - Seatbelt on macOS, user namespaces + seccomp on Linux. The sandbox controls filesystem access, network reach, and system calls. A local CONNECT proxy enforces domain-level filtering so agents can only reach their own API. See `docs/egg-sandbox-design.md` for implementation details.

### The wing: protecting you from the roost

All traffic between browser and wing is E2E encrypted (X25519 + AES-GCM). The roost forwards ciphertext. Wings connect outbound only - no inbound ports, no static IP, works behind any NAT or firewall.

Lock your wing (`wt wing lock`) and sessions also require passkey auth. The browser sends a WebAuthn assertion to the wing inside the encrypted channel. The roost forwards the blob but can't read or forge it. The wing verifies against its local allowlist and issues a boot-scoped nonce. A compromised roost can't start sessions on a locked wing.

### The roost: controlling access

The roost handles login (OAuth, device auth) and routes connections to the right wing. It stores passkey credential IDs and public keys so it can hand them to wings during `wt wing allow`. The actual WebAuthn verification happens on the wing, not the roost.

## E2E Encryption

### Two HKDF Domains

| Domain | Browser key | Wing key | HKDF info | Carries |
|--------|-------------|----------|-----------|---------|
| PTY | Ephemeral per session | Persistent (`~/.wingthing/wing_key`) | `"wt-pty"` | Terminal I/O |
| Tunnel | Ephemeral per tab (sessionStorage) | Persistent (`~/.wingthing/wing_key`) | `"wt-tunnel"` | Dir listings, session history, audit, egg config, passkey auth |

Both derived keys are ephemeral. The wing's base key is persistent on disk, but the browser always generates a fresh key - per session for PTY, per tab for tunnel. Close the tab and the browser's private key is gone. Previous sessions can't be decrypted.

### PTY Key Exchange

Every PTY session does an ephemeral X25519 ECDH exchange between browser and wing. The roost forwards public keys but never has either private key.

**Wing side** (`internal/auth/keypair.go`):
- `wt wing` generates a persistent X25519 keypair at `~/.wingthing/wing_key` on first run
- The public key is embedded in the wing's JWT during device auth
- The private key never leaves the machine

**Browser side** (`web/src/crypto.js`):
- Each `connectPTY()` or `attachPTY()` call generates a fresh ephemeral X25519 keypair
- The public key goes in the `pty.start` or `pty.attach` message
- The private key lives in memory for the session duration only

**Derivation** (`internal/auth/crypto.go`):
```
shared_secret = X25519(my_private, peer_public)
aes_key = HKDF-SHA256(shared_secret, salt=zeros(32), info="wt-pty")
cipher = AES-256-GCM(aes_key)
```

Both sides compute the same AES-256-GCM key independently. The roost can't derive the shared secret.

### Tunnel Key Exchange

Same ECDH + HKDF pattern, different info string. The browser's identity key lives in sessionStorage (ephemeral per tab):

```
shared_secret = X25519(browser_identity_priv, wing_pub)
aes_key = HKDF-SHA256(shared_secret, salt=zeros(32), info="wt-tunnel")
cipher = AES-256-GCM(aes_key)
```

Tunnel messages (`tunnel.req` / `tunnel.res` / `tunnel.stream`) carry encrypted inner payloads. The roost routes by `wing_id` and `request_id` but can't read the payload.

### Message Format

Every encrypted message is `base64(nonce[12] || ciphertext || tag[16])`. AES-GCM provides confidentiality and integrity - the roost can't modify ciphertext without detection.

### Session Reattach

When a browser reconnects to an existing session (`pty.attach`):

1. Browser generates a new ephemeral keypair
2. Sends the new public key in `pty.attach`
3. Wing derives a new shared key with the new browser public key
4. Wing replays buffered output encrypted with the new key
5. All subsequent I/O uses the new key

A compromised previous session key doesn't help with future reattaches.

## Passkey Auth

When a wing is locked (`wt wing lock`), browsers must prove they hold a passkey on the allowlist before the wing responds to anything.

**How it works:**
1. Browser sends a tunnel request to a locked wing
2. Wing replies with a `passkey.challenge` containing a random nonce
3. Browser shows an "authenticate with passkey" button (no auto-prompt)
4. User clicks, completes WebAuthn assertion via their password manager or platform authenticator
5. Browser sends `passkey.response` back through the encrypted tunnel
6. Wing verifies the signature against its local allowlist (`allow_keys` in `wing.yaml`)
7. Wing issues a boot-scoped nonce - valid until the wing process restarts

The nonce is shared between PTY and tunnel sessions. Configure `auth_ttl` in `wing.yaml` to force periodic re-authentication (default `0` means boot-scoped, no expiry). Wing restart revokes all nonces (in-memory cache).

**Manage the allowlist:**
```
wt wing lock                         # require passkey auth
wt wing allow --email user@co.com    # add a user
wt wing revoke user@co.com           # remove a user
wt wing unlock                       # disable passkey requirement
```

The allowlist lives in `wing.yaml` on your machine. The roost stores passkey public keys and credential IDs so it can hand them to the wing during `wt wing allow`. The security model assumes the roost is not compromised at the moment you add a key - after that, verification happens entirely on the wing.

## What the Roost Can See

**CAN see** (even with E2E active):
- Routing metadata: user ID, wing ID, session ID, agent name
- Session lifecycle: when sessions start, attach, detach, exit
- Message timing and sizes
- Control messages: `pty.resize` (terminal dimensions)
- Wing registration data: machine ID, available agents, project paths, labels

**CANNOT see** (with E2E active):
- Terminal content (keystrokes in, agent output out)
- File contents displayed in the terminal
- Credentials typed or displayed
- Directory listings, session history, audit recordings (tunnel-encrypted)
- Egg config updates (tunnel-encrypted)
- Passkey assertions (tunnel-encrypted)

## Roost Compromise Threat Model

If an attacker gains full control of the Fly.io deployment (SSH, deploy credentials, or Fly API token):

### What they can do

1. **Deploy a modified relay binary** that logs plaintext metadata, performs traffic analysis, drops sessions (DoS), or injects fake error messages.

2. **Read the SQLite database** containing user accounts (emails, GitHub/Google IDs), device auth codes (not reusable after claim), JWT signing secret, and passkey public keys/credential IDs. Public keys can't be used to impersonate users. See the privacy policy for what a database leak means.

3. **Forge wing auth JWTs** to impersonate a wing connection or route sessions to an attacker-controlled fake wing (MITM). The attacker's fake wing does its own key exchange with the browser, so it can decrypt traffic. The real wing never sees these sessions.

4. **Serve a modified web app** that captures keystrokes into a fake terminal UI or establishes sessions with forged credentials.

### How locking mitigates this

On a locked wing, the attacker's fake wing can't produce a valid passkey assertion. The browser sends its WebAuthn challenge through the encrypted tunnel to the real wing. A fake wing would need a private key on the allowlist to pass verification - the roost only has public keys, which aren't enough.

A modified web client is reduced to a phishing attack. It can capture what you type into a fake terminal, but it can't connect to your real wing (locked wings reject unknown keys), can't read existing session output (E2E encrypted), and can't hijack running sessions (already keyed to the real browser's ephemeral key).

### What they cannot do

1. **Decrypt existing E2E traffic** - the relay binary as-built can't read encrypted PTY data
2. **Access wing machines** - the roost has no shell access to wings
3. **Read wing private keys** - stored at `~/.wingthing/wing_key`, never transmitted
4. **Access API keys** - env vars like `ANTHROPIC_API_KEY` exist only on the wing

## Known Limitations

### Web app served from roost
The roost serves the JavaScript that runs in your browser. A compromised roost can serve modified JS that bypasses E2E. This is the fundamental limitation of any web-based E2E system (same as WhatsApp Web, ProtonMail, etc.). On a locked wing, this is reduced to phishing - the modified client can't connect to your wing.

Mitigation: SRI hashes published out-of-band, or a native client that doesn't depend on roost-served code.

### Metadata is not encrypted
Session metadata (who connects when, to which project, with which agent) is visible to the roost. Routing requires it.

### Static zero salt in HKDF
The HKDF derivation uses a 32-byte zero salt. HKDF is designed to be secure with a zero salt, but a random salt sent alongside the public key would add defense in depth.

### Ring buffer stores plaintext on wing
The wing keeps a plaintext ring buffer of recent terminal output for session reattach replay. If the wing machine itself is compromised, this is accessible.

### Unlocked wings trust the roost for identity
An unlocked wing accepts any session from a JWT-validated user. If the roost is compromised and the attacker forges a JWT, the wing has no second factor to reject it. Lock your wing to add passkey verification on top.

## Recommendations

1. **Signed web assets** - publish SRI hashes out-of-band or build a native client. On locked wings a compromised client can only phish, but this eliminates that vector too.
2. **Rotate JWT signing secret** - support secret rotation to limit blast radius of database compromise.
3. **Audit logging** - tamper-evident log of wing registrations and session starts, stored separately from the roost.

## Reference

| What | Protected? | How |
|------|-----------|-----|
| Terminal I/O | Yes | X25519 + AES-256-GCM, `wt-pty` domain, per-session keys |
| Wing data (dirs, sessions, audit, config) | Yes | X25519 + AES-256-GCM, `wt-tunnel` domain, per-tab keys |
| Passkey assertions | Yes | Encrypted inside tunnel, roost sees opaque bytes |
| Session metadata | No | Needed for routing |
| Wing auth (unlocked) | Partial | JWT forgeable if DB compromised |
| Wing auth (locked) | Yes | Passkey verification on wing, roost can't forge assertions |
| Web client integrity | Partial | Served from roost, but locked wings reduce compromise to phishing |
| Wing machine access | Yes | Roost has no shell access |
