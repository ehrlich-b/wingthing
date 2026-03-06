# wingthing as remote tmux

Session persistence from any device. That's the pitch. SSH gives you a shell. mosh gives you a roaming shell. tmux gives you persistent sessions on one machine. wingthing gives you persistent sessions accessible from anywhere - your phone, your laptop, a browser on someone else's computer.

This doc frames the project as a remote terminal tool first. AI agents and sandboxing are features on top.

## The gap in the market

SSH assumes inbound connectivity. You need an open port, a static IP or DDNS, and key management. Behind a NAT? Port forward. Behind a corporate firewall? Tough. On cellular? Good luck.

mosh fixes roaming and local echo but inherits the inbound port requirement. No NAT traversal, no relay fallback. UDP blocked? mosh doesn't work.

tmux solves session persistence but only locally. You still need SSH to reach the machine, and you're back to the same inbound connectivity problem.

All three give you a single terminal connection. Want to check on a long-running process from your phone while your desktop is still attached? You need tmux inside SSH, and both machines need to reach the host.

## What wingthing does today

**Outbound-only connectivity.** The wing connects outbound to a roost. No ports to open, no static IP. Works behind any NAT, any firewall, any cellular network.

**E2E encryption.** Terminal I/O is encrypted between browser and wing (X25519 + AES-256-GCM). The roost forwards ciphertext it can't read.

**Session persistence with VTE snapshots.** A server-side virtual terminal emulator (`charmbracelet/x/vt`) captures full terminal state in the egg process. On reconnect, the egg sends a VTE snapshot (current screen + scrollback) instead of replaying raw bytes. This is the same architecture as tmux and mosh - a userspace terminal emulator sits between the PTY and the network. Close your laptop, open your phone, reattach. The browser gets the current screen state instantly.

The VTE also maintains a 50,000-line scrollback ring buffer. Lines that scroll off the top of the terminal grid are captured and stored, same as a local terminal emulator like ghostty or iTerm. On reconnect, scrollback is included in the snapshot. Start a build, go to lunch, come back, scroll up to see what happened.

**Forward secrecy on every reattach.** Each reattach does a fresh X25519 key exchange. The browser generates a new ephemeral keypair per tab (stored in sessionStorage), so old sessions can't be decrypted even if keys are later compromised.

**Passkey auth.** A passkey is just a P-256 keypair where the private key never leaves your device. The wing stores your public key. On reattach, the wing sends a 32-byte random challenge, the browser calls `navigator.credentials.get()` (biometric/PIN prompt), and the device signs the challenge with ECDSA-SHA256. The wing verifies the signature against the stored public key. Same concept as SSH keys, but the key lives in your device's secure enclave instead of `~/.ssh/`, and you unlock it with a fingerprint instead of a passphrase.

After verification, the wing issues an auth token (64 random hex bytes) cached in the browser's sessionStorage. Subsequent reattaches present the token instead of re-prompting. Tokens are boot-scoped by default (cleared on wing restart) with optional TTL via `auth_ttl` in wing.yaml.

The roost never sees any of this. The challenge, signature, and token all travel inside the E2E encrypted tunnel. The roost forwards ciphertext. A compromised roost can't forge passkey assertions because it doesn't have the private key and can't even read the challenge.

**No client install.** It's a web terminal. Open a browser, connect.

**Sandboxing.** Optional per-session OS-level sandbox (seatbelt on macOS, namespaces + seccomp + cgroups on Linux). Not relevant for plain terminal access, but available when you want to constrain what a process can touch.

## Three independent layers

The terminal stack has three layers that don't know about each other. Changes to one don't affect the others.

**Rendering (browser).** xterm.js today. Possibly ghostty-web (WASM-compiled Ghostty parser, GPU-accelerated) in the future. The renderer receives bytes and paints pixels. It doesn't care how the bytes got there - WebSocket relay, direct P2P, or a local pipe. Swapping the renderer is a frontend change only.

**Session state (egg).** The VTE in the egg process is the source of truth for terminal state. It captures the grid, cursor, modes, scrollback. It produces snapshots on reconnect and passes raw bytes through for the live path. This is independent of how those bytes reach the browser.

**Transport (relay).** Today, all bytes flow through the roost via WebSocket. The roost is a dumb pipe forwarding ciphertext. The transport could change to P2P (WebRTC DataChannel for browsers, QUIC for CLI clients) without touching the VTE or the renderer. The E2E encryption is transport-independent - same ECDH key exchange, same AES-GCM, whether bytes flow through the roost or directly between peers.

## What's missing

### Multi-attach

Today: one browser per session. Reattach replaces the previous connection.

Goal: multiple terminals attached to the same session simultaneously. Working on your desktop, pull out your phone to check progress, both see the same output. A teammate attaches to watch.

The relay currently tracks one `BrowserConn` per session. Multi-attach means a set of connections, with output broadcast to all. Input from any attached terminal goes to the wing - first-come-first-served, same as two people typing into a shared tmux pane.

Key management is the hard part. Each browser has its own ephemeral key, so the wing can't encrypt once and broadcast. Two options: (a) derive a shared session key that all attached browsers learn during attach, or (b) the wing encrypts separately per browser. Option (a) is cleaner but the key distribution needs thought. Option (b) is simpler but CPU scales linearly with viewers.

### CLI client

Today: browser-only. The web terminal handles remote access well but not "I'm already in a terminal and want to attach to a remote session."

Goal: `wt attach <session-id>` from any terminal. Same protocol, same encryption, same multi-attach. The CLI client connects via WebSocket and renders in the local terminal emulator (ghostty, iTerm, whatever). No browser needed.

**Auth: SSH keys instead of passkeys.** The browser uses WebAuthn passkeys because that's what browsers have. A CLI client doesn't have `navigator.credentials.get()`, but the user already has SSH keys. The auth model is the same - challenge-response with an asymmetric keypair. The wing sends a challenge, the CLI signs it with the user's SSH private key (ed25519, ecdsa, whatever `ssh-agent` has), the wing verifies against the stored public key. Same security properties, different key container.

This means the wing's `allow_keys` config accepts both passkey public keys (from browser enrollment) and SSH public keys (from `wt attach` enrollment or manual config). One list, two key types, same verification flow.

This is what makes wingthing usable as a daily driver for people who live in the terminal.

### Disk-backed scrollback

The VTE's 50,000-line ring buffer covers most interactive use. But for sessions that run for days (CI, long builds, training runs), you'd want the full history on disk.

The audit recording feature already writes session output to disk. Extending this to serve as the scrollback backing store - stream from disk on attach, then switch to the live VTE path - would give unbounded history. The ring buffer handles the common case. Disk handles the edge case.

## P2P as a transport optimization

This is orthogonal to session management but worth noting.

The roost transits all traffic today. It can't read the ciphertext, but every byte flows through it. This adds latency and makes the roost a bottleneck.

Tailscale's DERP relays work the same way and solve it with NAT traversal - try to establish a direct connection, fall back to relay when it fails. wingthing could do the same. The roost handles signaling and auth, and falls back to relaying only when hole punching fails.

For browsers: WebRTC DataChannels are the only browser API that does P2P with NAT traversal. pion/webrtc (pure Go, used by LiveKit) handles the wing side. ICE negotiation gets ~80-85% direct connections on residential NATs.

For CLI clients: QUIC gives UDP-based multiplexing with connection migration (handles roaming). quic-go has NAT traversal support.

This is a later optimization. The relay works fine for now, and the E2E encryption means the security model doesn't change either way.

### Tailscale complementarity

If you're already on a tailnet, wingthing doesn't need its own NAT traversal. The wing is reachable at its Tailscale IP. wingthing adds the session layer on top: PTY management, VTE snapshots, multi-attach, scrollback, passkey auth, sandboxing.

A `--relay tailscale` flag (or auto-detection) could skip the roost for connectivity. The roost still serves the web UI and handles discovery for non-Tailscale users.

Tailscale solves the network. wingthing solves the session.

## How AI fits

AI agents are a session type. `wt egg claude` is "start a sandboxed Claude Code session." The underlying machinery - PTY management, E2E encryption, VTE snapshots, remote access - works for any terminal process.

`wt egg bash` is a sandboxed shell. `wt egg python` is a sandboxed REPL. The sandbox is optional. egg.yaml controls filesystem access, network filtering, and resource limits for any session, not just agents.

## Implementation priorities

1. **CLI client** (`wt attach`) - proves the protocol works outside a browser
2. **Multi-attach** - multiple terminals on one session, output broadcast
3. **Disk-backed scrollback** - unbounded session history
4. **P2P** (browser WebRTC, CLI QUIC) - reduce relay load, lower latency
5. **Tailscale integration** - auto-detect tailnet, skip roost for connectivity

Each step is independently useful.

## Open questions

- Multi-attach key management: shared session key vs per-browser encryption?
- Multi-attach input: interleaved (tmux model) or locking/turn-taking?
- Auth model for multi-attach: does each viewer need passkey auth, or does the session owner grant access?
- Should the CLI client be a separate binary (`wt-attach`) to keep the main binary small?
- Disk-backed scrollback: how much storage per hour of terminal output? Compression ratio for ANSI data?
- pion/webrtc adds ~8-15 MB to the binary. Acceptable for a daemon, but worth measuring precisely.
