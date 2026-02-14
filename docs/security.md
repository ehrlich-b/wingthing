# Wingthing Security Model

## Design Goal

The relay server (currently hosted on Fly.io) should **never be able to read wing data**. It functions as a dumb pipe — routing encrypted bytes between browsers and wings without the ability to inspect, log, or tamper with the data. This covers terminal I/O (PTY sessions) and all wing management data (directory listings, session history, audit recordings, egg configs, passkey assertions) via the encrypted tunnel protocol. This document describes how that goal is achieved, what the current limitations are, and what the attack surface looks like if the relay infrastructure is compromised.

## Architecture Overview

```
Browser (app.wingthing.ai)
    |
    | WebSocket (TLS)
    |
Relay Server (wingthing.fly.dev)    <-- untrusted
    |
    | WebSocket (TLS)
    |
Wing (your machine, running `wt wing`)
```

The relay sits between the browser and the wing. All three connections use TLS, but TLS only protects the transport — the relay terminates both TLS connections and could theoretically read the plaintext at the application layer. E2E encryption solves this.

## E2E Encryption: How It Works

### Two Key Types, Two HKDF Domains

| Key | Lifecycle | HKDF info | Purpose |
|-----|-----------|-----------|---------|
| PTY session key | Per-session ephemeral X25519 | `"wt-pty"` | Terminal I/O encryption |
| Tunnel key | Persistent identity X25519 | `"wt-tunnel"` | All non-PTY wing data (dir listings, sessions, audit, egg config, passkey auth) |

### PTY Key Exchange

Every PTY session performs an ephemeral X25519 ECDH key exchange directly between the browser and the wing. The relay forwards the public keys but never possesses either private key.

**Wing side** (`internal/auth/keypair.go`):
- On first run, `wt wing` generates a persistent X25519 keypair stored at `~/.wingthing/wing_key`
- The public key is embedded in the wing's JWT during device auth (claim flow)
- The private key never leaves the machine

**Browser side** (`web/main.js`):
- On each `connectPTY()` or `attachPTY()` call, the browser generates a fresh ephemeral X25519 keypair using `@noble/curves/ed25519`
- The ephemeral public key is sent in the `pty.start` or `pty.attach` message's `public_key` field
- The ephemeral private key is held in memory only for the duration of the session

**Key derivation** (`internal/auth/crypto.go`):
```
shared_secret = X25519(my_private, peer_public)
aes_key = HKDF-SHA256(shared_secret, salt=zeros(32), info="wt-pty")
cipher = AES-256-GCM(aes_key)
```

Both sides independently compute the same AES-256-GCM key. The relay never has access to either private key, so it cannot derive the shared secret.

### Tunnel Key Exchange

The tunnel uses the browser's persistent identity key (stored in sessionStorage, ephemeral per tab for PFS) and the wing's persistent X25519 key. Same ECDH + HKDF pattern, different info string:

```
shared_secret = X25519(browser_identity_priv, wing_pub)
aes_key = HKDF-SHA256(shared_secret, salt=zeros(32), info="wt-tunnel")
cipher = AES-256-GCM(aes_key)
```

Tunnel messages (`tunnel.req` / `tunnel.res` / `tunnel.stream`) carry encrypted inner payloads. The relay routes by `wing_id` and `request_id` but cannot read the payload. Inner message types: `dir.list`, `sessions.list`, `sessions.history`, `audit.request`, `egg.config_update`, `pty.kill`, `wing.update`, `pins.list`, `passkey.auth`.

Passkey auth tokens (from Touch ID verification on pinned wings) are shared between PTY and tunnel sessions. Configurable TTL via `auth_ttl` in wing.yaml. Wing restart revokes all sessions (in-memory token cache).

### Encryption in Practice

**Output (wing → browser)** — `cmd/wt/wing.go` lines 697–733:
1. Wing reads raw PTY bytes from the child process
2. Stores plaintext in a ring buffer (for session reattach replay)
3. If E2E is active: encrypts with `AES-256-GCM(random_nonce)`, sends as base64
4. If E2E is not active: sends as plain base64 (fallback — see Limitations)

**Input (browser → wing)** — `cmd/wt/wing.go` lines 736–820:
1. Browser encrypts keystrokes with `AES-256-GCM(random_nonce)` via Web Crypto API
2. Sends as base64 in the `pty.input` message `data` field
3. Wing decrypts and writes to the PTY file descriptor

**Format**: Every encrypted message is `base64(nonce[12] || ciphertext || tag[16])`. AES-GCM provides both confidentiality and integrity — the relay cannot modify ciphertext without detection.

### Session Reattach Re-keying

When a browser reconnects to an existing session (`pty.attach`):
1. Browser generates a new ephemeral keypair
2. Sends the new public key in the `pty.attach` message
3. Wing derives a new shared key with the new browser public key
4. Wing replays buffered output encrypted with the new key
5. All subsequent I/O uses the new key

This means even if a previous session key were compromised, the attacker cannot read future reattached sessions (partial forward secrecy per reattach).

## What the Relay Can See

### CAN see (even with E2E active):
- **Routing metadata**: user ID, wing ID, session ID, agent name
- **Session lifecycle**: when sessions start, attach, detach, exit
- **Message timing and sizes**: number of messages, approximate byte counts
- **Control messages**: `pty.resize` (terminal dimensions)
- **Wing registration data**: machine ID, available agents, project names/paths, labels

### CANNOT see (with E2E active):
- **Terminal content**: all `pty.output` and `pty.input` data fields are encrypted
- **Keystrokes**: what you type in the terminal
- **Agent output**: what Claude/Codex/Ollama writes back
- **File contents**: anything displayed in the terminal (cat, vim, etc.)
- **Credentials**: API keys, passwords, tokens typed or displayed in the terminal
- **Directory listings**: encrypted via tunnel (`dir.list` inner type)
- **Session history and audit recordings**: encrypted via tunnel
- **Egg config updates**: encrypted via tunnel
- **Passkey assertions**: encrypted via tunnel

## "Fly Account Compromised" Threat Model

If an attacker gains full control of the Fly.io deployment (SSH access, deploy credentials, or Fly API token), they can:

### What they CAN do:

1. **Deploy a modified relay binary** that:
   - Logs all plaintext metadata (user IDs, session IDs, CWDs, project names)
   - Performs traffic analysis (message timing, sizes, patterns)
   - Blocks or drops sessions (denial of service)
   - Injects fake `error` messages to disrupt sessions

2. **Read the SQLite database** containing:
   - User accounts (GitHub/Google IDs, display names, avatars)
   - Device auth codes (used during initial setup, not reusable after claim)
   - JWT signing secret (can forge wing auth tokens — see below)
   - Social feed content (posts, votes, comments — public data)

3. **Forge wing auth JWTs** using the signing secret to:
   - Impersonate any user's wing connection
   - Route sessions to an attacker-controlled wing (MITM)
   - This is the most critical attack: the attacker deploys a malicious wing that pretends to be the victim's machine

4. **MITM via fake wing**: By forging a JWT and registering a malicious wing:
   - The browser sends `pty.start` with an ephemeral public key
   - The attacker's fake wing generates its own keypair, derives a shared key with the browser
   - The attacker can now decrypt all terminal traffic
   - The real wing never receives the session
   - **Mitigated by wing-side pinning**: The real wing ignores sessions from unknown public keys. The attacker can intercept new sessions but cannot trick the real wing into responding. The user sees their real wing is unresponsive to the attacker's sessions, which is the correct behavior.

5. **Serve a modified web app** (`index.html`, `main.js`):
   - Show a fake terminal UI that captures keystrokes
   - Attempt to establish sessions with forged credentials
   - **With wing-side pinning**: this is reduced to a **phishing attack**. The modified client can capture what you type into a fake terminal, but cannot connect to your real wing (pinned keys reject unknown PKs), cannot read existing session output (E2E encrypted), and cannot hijack running sessions (already keyed to the real browser's ephemeral key). The attacker collects keystrokes into a void.
   - **Without pinning**: the modified client could establish a real session with the wing and exfiltrate traffic

### What they CANNOT do (without additional compromise):

1. **Decrypt existing E2E traffic** without deploying modified code — the relay binary as-built genuinely cannot read encrypted PTY data
2. **Access wing machines** — the relay never has SSH or shell access to wings
3. **Read wing private keys** — stored on user machines at `~/.wingthing/wing_key`, never transmitted
4. **Access API keys** — environment variables like `ANTHROPIC_API_KEY` exist only on the wing machine

## Known Limitations and Gaps

### 1. Wing-side key pinning (CRITICAL — mitigates fake-wing MITM and compromised relay)
The wing is the trust anchor — it's the machine you physically control. By default, a wing accepts any session from a user with a valid JWT (relay-validated). But wings can **pin** which browser public keys / user identities they respond to.

**How it works**: The wing maintains an allowlist of trusted public keys at `~/.wingthing/pinned_keys`. When a `pty.start` or `tunnel.req` arrives, the wing checks the sender's public key against the allowlist. If pinning is enabled and the key isn't recognized, the wing refuses — no PTY session, no tunnel responses, nothing at all. Pinned wings require passkey verification (Touch ID) before responding. Auth is deferred to the first actual tunnel request (e.g., browsing files triggers it). Dashboard shows lock icon + "pinned" badge for pinned wings.

**Why this matters**: If Fly is compromised and an attacker forges a JWT to inject a new browser identity into your account, the wing blocks it. The attacker can talk to the relay all day, but the wing won't feed them back anything. This is the same model as Tailscale — the node (wing) controls who it accepts connections from.

**Default behavior**: Open — wings accept any session from a JWT-validated user. No friction for new users. Pinning is opt-in for users who want stronger guarantees (`wt wing pin <fingerprint>`).

### 2. Web app served from relay (CRITICAL)
Since `app.wingthing.ai` serves the JavaScript from the same infrastructure, a compromised relay can serve modified JS that bypasses E2E. This is the fundamental limitation of any web-based E2E system (same problem as WhatsApp Web, ProtonMail, etc.).

**Mitigation path**: Subresource Integrity (SRI) hashes in the HTML, published in a separate channel. Or a native/desktop app that doesn't depend on relay-served code.

### 3. Metadata is not encrypted
Session metadata (who connects when, from where, to which project, with which agent) is visible to the relay. This is inherent to routing — the relay needs to know where to send data.

### 4. Static zero salt in HKDF
The HKDF derivation uses a 32-byte zero salt. While HKDF is designed to be secure with a zero salt, using a random salt (sent alongside the public key) would add defense in depth.

### 5. Ring buffer stores plaintext on wing
The wing keeps a plaintext ring buffer of recent terminal output for session reattach replay. This is necessary for the feature but means the wing's memory contains cleartext terminal history. If the wing machine itself is compromised, this is accessible.

## Recommendations (Priority Order)

1. **Signed web assets** — Publish SRI hashes out-of-band, or build a native client. With wing-side pinning, this is less critical (compromised client can only phish, not breach the wing), but still eliminates the phishing vector entirely.
2. **Rotate JWT signing secret** — Support secret rotation to limit blast radius of database compromise
3. **Audit logging** — Tamper-evident log of wing registrations and session starts, stored separately from the relay

## Summary

| Layer | Protected? | Notes |
|-------|-----------|-------|
| Terminal I/O | Yes (E2E) | X25519 + AES-256-GCM via PTY key (`wt-pty`), relay sees only ciphertext |
| Wing data (dir listings, sessions, audit, egg config) | Yes (E2E) | Encrypted via tunnel key (`wt-tunnel`), relay forwards opaque blobs |
| Passkey auth | Yes (E2E) | Assertions encrypted in tunnel, relay cannot see Touch ID responses |
| Session metadata | No | Needed for routing (user ID, wing ID, session ID) |
| Wing auth | Partial -> Strong | JWT forgeable if DB compromised, but wing-side pinning + passkey blocks unknown keys |
| Web client integrity | Partial | Served from relay — compromised relay can phish, but pinned wings reject fake sessions |
| Wing machine access | Yes | Relay has no shell/SSH access to wings |

The E2E encryption covers **all wing data** — terminal I/O via per-session PTY keys, and all management data (directory listings, session history, audit recordings, egg configs, passkey assertions) via the persistent tunnel key. The relay is a dumb forwarder that cannot read any of it. Wing-side key pinning extends this protection against an active attacker who compromises the relay — the wing refuses to respond to unknown public keys, so a forged JWT alone isn't enough to intercept traffic. Pinned wings additionally require passkey verification (Touch ID), with auth deferred to first actual tunnel request. A compromised relay can serve a modified web client, but with pinning enabled this is reduced to a phishing attack: the attacker can capture keystrokes typed into a fake terminal, but cannot connect to the real wing or read its existing sessions. The combination of E2E encryption + wing-side pinning + passkey auth means a full relay compromise yields metadata and phishing opportunities, but not access to terminal content, wing data, or the wing itself.
