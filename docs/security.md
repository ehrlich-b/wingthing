# Wingthing Security Model

## Design Goal

The relay server (currently hosted on Fly.io) should **never be able to read terminal session content**. It functions as a dumb pipe — routing encrypted bytes between browsers and wings without the ability to inspect, log, or tamper with the data. This document describes how that goal is achieved, what the current limitations are, and what the attack surface looks like if the relay infrastructure is compromised.

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

### Key Exchange

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
- **Routing metadata**: user ID, wing ID, session ID, agent name, working directory (CWD)
- **Session lifecycle**: when sessions start, attach, detach, exit
- **Message timing and sizes**: number of messages, approximate byte counts
- **Control messages**: `pty.resize` (terminal dimensions), `pty.kill`
- **Wing registration data**: machine ID, available agents, project names/paths, labels
- **Directory listings**: `dir.list` / `dir.results` are not encrypted (filesystem paths only, no file contents)
- **Chat messages**: chat sessions (NLUX) are **not** E2E encrypted — the relay sees full chat content

### CANNOT see (with E2E active):
- **Terminal content**: all `pty.output` and `pty.input` data fields are encrypted
- **Keystrokes**: what you type in the terminal
- **Agent output**: what Claude/Codex/Ollama writes back
- **File contents**: anything displayed in the terminal (cat, vim, etc.)
- **Credentials**: API keys, passwords, tokens typed or displayed in the terminal

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
   - **This breaks E2E completely** — the browser has no way to verify it's talking to the real wing

5. **Serve a modified web app** (`index.html`, `main.js`):
   - Skip E2E encryption entirely
   - Exfiltrate the ephemeral private key
   - Send plaintext to a logging endpoint
   - **The browser is served from the relay, so compromising the relay compromises the client**

### What they CANNOT do (without additional compromise):

1. **Decrypt existing E2E traffic** without deploying modified code — the relay binary as-built genuinely cannot read encrypted PTY data
2. **Access wing machines** — the relay never has SSH or shell access to wings
3. **Read wing private keys** — stored on user machines at `~/.wingthing/wing_key`, never transmitted
4. **Access API keys** — environment variables like `ANTHROPIC_API_KEY` exist only on the wing machine

## Known Limitations and Gaps

### 1. No wing identity verification (CRITICAL)
The browser has no way to verify it's talking to the real wing vs. a relay-injected fake. A pin/fingerprint mechanism (TOFU or out-of-band verification of the wing's public key) would close this gap.

**Mitigation path**: Open by default — connections work without pinning. Users who want stronger guarantees can pin wing fingerprints. Display the wing's public key fingerprint in both the CLI (`wt wing status`) and the web UI. Pinned wings warn/block on mismatch.

### 2. Web app served from relay (CRITICAL)
Since `app.wingthing.ai` serves the JavaScript from the same infrastructure, a compromised relay can serve modified JS that bypasses E2E. This is the fundamental limitation of any web-based E2E system (same problem as WhatsApp Web, ProtonMail, etc.).

**Mitigation path**: Subresource Integrity (SRI) hashes in the HTML, published in a separate channel. Or a native/desktop app that doesn't depend on relay-served code.

### 3. Chat sessions are not E2E encrypted
Only PTY (terminal) sessions use E2E. Chat sessions via NLUX send plaintext through the relay. The relay can read all chat messages.

**Mitigation path**: Extend E2E to chat sessions using the same X25519 + AES-GCM pattern.

### 4. Metadata is not encrypted
Session metadata (who connects when, from where, to which project, with which agent) is visible to the relay. This is inherent to routing — the relay needs to know where to send data.

### 5. Static zero salt in HKDF
The HKDF derivation uses a 32-byte zero salt. While HKDF is designed to be secure with a zero salt, using a random salt (sent alongside the public key) would add defense in depth.

### 6. Directory listings are plaintext
The `dir.list`/`dir.results` API sends filesystem paths through the relay unencrypted. An attacker sees directory names and file names, though not file contents.

### 7. Ring buffer stores plaintext on wing
The wing keeps a plaintext ring buffer of recent terminal output for session reattach replay. This is necessary for the feature but means the wing's memory contains cleartext terminal history. If the wing machine itself is compromised, this is accessible.

## Recommendations (Priority Order)

1. **Wing identity pinning** — Open by default (any wing can connect, no friction), but users can opt into pinning a wing's public key fingerprint. On first connect the browser displays the fingerprint; `wt wing status` shows the same fingerprint locally. If the user pins it, subsequent connections warn/block on mismatch. This closes the fake-wing MITM attack for security-conscious users without adding friction to the default experience.
2. **E2E for chat** — Extend the same X25519 + AES-256-GCM encryption to chat sessions
3. **Signed web assets** — Publish SRI hashes out-of-band, or build a native client
4. **Rotate JWT signing secret** — Support secret rotation to limit blast radius of database compromise
5. **Audit logging** — Tamper-evident log of wing registrations and session starts, stored separately from the relay

## Summary

| Layer | Protected? | Notes |
|-------|-----------|-------|
| Terminal I/O | Yes (E2E) | X25519 + AES-256-GCM, relay sees only ciphertext |
| Chat messages | No | Plaintext through relay |
| Session metadata | No | Needed for routing |
| Directory listings | No | Paths only, no file contents |
| Wing auth | Partial | JWT can be forged if DB is compromised |
| Web client integrity | No | Served from relay — compromised relay = compromised client |
| Wing machine access | Yes | Relay has no shell/SSH access to wings |

The E2E encryption for terminal sessions is **real and meaningful** against passive observation and honest-but-curious relay operators. Against an active attacker who compromises the full Fly deployment, the main gaps are wing identity verification and the fact that the web client is served from the same infrastructure it's trying to distrust.
