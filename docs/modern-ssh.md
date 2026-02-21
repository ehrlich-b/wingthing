# wingthing as Modern SSH

wingthing already does most of what SSH does, plus things SSH can't. This doc frames the project as a remote terminal tool first, with AI agents and sandboxing as features on top.

## What SSH gets wrong

SSH assumes inbound connectivity. You need a port open, a static IP or DDNS, and key management. Behind a NAT? Port forward. Behind a corporate firewall? Tough. On cellular? Good luck.

mosh fixes the roaming and local echo problems but inherits the inbound port requirement. No NAT traversal, no relay fallback. UDP blocked? mosh doesn't work.

Both tools give you a single terminal session. Want to check on a long-running process from your phone? You need tmux or screen running inside SSH, and you need to be able to reach the machine.

## What wingthing already does

**Outbound-only connectivity.** The wing connects outbound to a roost. No ports to open, no static IP. Works behind any NAT, any firewall, any cellular network. The roost routes connections to the right wing.

**E2E encryption.** Terminal I/O is encrypted between browser and wing (X25519 + AES-256-GCM). The roost forwards ciphertext. Even if the roost is compromised, the attacker gets encrypted blobs.

**Session persistence.** Close your laptop, open your phone, reattach. The wing keeps a replay buffer. Each reattach does a fresh key exchange - the browser generates a new ephemeral keypair, so old sessions can't be decrypted.

**Passkey auth.** Lock your wing and sessions require a hardware passkey on top of encryption. The roost can't forge assertions - verification happens on your machine.

**No client install.** It's a web terminal. Open a browser, connect. No SSH keys to distribute, no client software to install.

**Sandboxing.** Optional, per-session OS-level sandbox. Not relevant for plain terminal access, but available when you want to constrain what a process can touch.

## What's missing

### Multi-attach

Today: one browser per session. Reattach replaces the previous connection.

Vision: multiple terminals attached to the same session simultaneously. You're working on your desktop, pull out your phone to check progress, both see the same output. Someone on your team attaches to watch.

**Protocol change:** The relay currently tracks one `BrowserConn` per session. Multi-attach means a set of connections, with output broadcast to all. Input from any attached terminal goes to the wing - first-come-first-served, same as two people typing into a shared tmux pane.

**Key management challenge:** Each browser has its own ephemeral key, so the wing can't encrypt once and broadcast. Two options: (a) establish a shared session key that all attached browsers derive, or (b) the wing encrypts per-browser. Option (a) is cleaner - the session key is derived from the wing's persistent key and a session-specific value that all browsers learn during attach.

### Full scrollback replay

Today: the wing keeps a ring buffer and replays recent output on reattach. You get the last N bytes.

Vision: full scrollback. Attach to a session that's been running for hours and scroll back to the beginning. The wing writes session output to disk (already partially there with the audit/recording feature). On attach, stream the full history, then switch to live.

This is the feature that makes wingthing strictly better than SSH + tmux for monitoring long-running processes. Start a build, go to lunch, attach from your phone, scroll back to see what happened.

### CLI client

Today: browser-only. The web terminal is good for remote access but not for "I'm already SSHed into a machine and want to attach to a session."

Vision: `wt attach <session-id>` from any terminal. Same protocol, same encryption, same multi-attach. The CLI client connects via WebSocket (or P2P, see below) and renders in the local terminal. No browser needed.

This makes wingthing a direct SSH replacement for power users who live in the terminal.

## P2P and hole punching

### The relay problem

The roost currently transits all traffic. It's a dumb pipe - can't read the ciphertext - but every byte flows through it. This costs bandwidth, adds latency, and creates a single point of failure.

### The Tailscale model

Tailscale's DERP relays work the same way: route encrypted WireGuard packets between peers. But Tailscale tries hard to establish direct connections via NAT traversal. DERP is the fallback, not the default path. They report >90% direct connections.

wingthing should do the same. The roost handles signaling (discovery, auth, session setup) and falls back to relaying when direct connections fail. When they succeed, the roost doesn't transit a single byte of session data.

### WebRTC for browser-to-wing P2P

The browser can't send raw UDP. WebRTC DataChannels are the only browser API that does P2P with NAT traversal. Ordered+reliable DataChannels give TCP-equivalent delivery guarantees - fine for terminal I/O.

**How it would work:**

1. Browser connects to roost via WebSocket (signaling channel - already exists)
2. Browser and wing exchange SDP offers/answers and ICE candidates through the roost
3. ICE negotiation: STUN discovers public IP/port, attempts hole punching
4. If direct path found (~80-85% of residential NATs): DataChannel carries PTY data, sub-40ms latency
5. If hole punching fails: fall back to existing WebSocket relay through roost

No TURN infrastructure needed. The roost IS the fallback relay, just like DERP is for Tailscale.

**Go side:** pion/webrtc is pure Go, production-ready (LiveKit uses it), no Cgo. Binary size cost is ~8-15 MB - meaningful but acceptable for a daemon.

**New message types:**

| Message | Direction | Purpose |
|---------|-----------|---------|
| `p2p.offer` | browser -> roost -> wing | SDP offer for WebRTC |
| `p2p.answer` | wing -> roost -> browser | SDP answer |
| `p2p.candidate` | both directions via roost | ICE candidates |
| `p2p.connected` | wing -> roost | Direct path established, stop relaying |

Once P2P is up, PTY messages flow directly. The roost only sees the signaling. The E2E encryption stays the same - the transport changes from WebSocket-through-roost to DataChannel-direct, but the ECDH key exchange and AES-GCM encryption are transport-independent.

### CLI-to-wing P2P

For the CLI client (`wt attach`), there's no browser constraint. Options:

- **QUIC** - UDP-based, multiplexed, encrypted, connection migration (IP changes). Go has mature libraries (quic-go). Can do its own hole punching via draft-seemann-quic-nat-traversal.
- **WireGuard/Noise** - same protocol Tailscale uses. Lightweight, well-understood.
- **Plain UDP with custom protocol** - mosh does this. Simplest but most work.

QUIC is probably the right choice. It gives you multiplexing (multiple sessions over one connection), built-in encryption (though we'd keep our own E2E layer), and connection migration for roaming.

### Not mutually exclusive with Tailscale

If you're already on a tailnet, wingthing doesn't need its own NAT traversal. The wing is reachable at its Tailscale IP. wingthing adds the session layer on top: PTY management, multi-attach, scrollback replay, passkey auth, sandboxing.

A `--relay tailscale` flag (or auto-detection) could skip the roost entirely for connectivity and use Tailscale's network. The roost still serves the web UI and handles discovery for non-Tailscale users.

The layering is: Tailscale solves the network. wingthing solves the session.

## How AI fits

AI agents are a session type, not the product. `wt egg claude` is "start a sandboxed Claude Code session." But the underlying machinery - PTY management, E2E encryption, remote access, session persistence - works for any terminal process.

`wt egg bash` is a sandboxed shell. `wt egg python` is a sandboxed REPL. The sandbox is optional. The remote terminal is the core.

The egg.yaml config that today controls agent sandboxing also controls any session's environment: filesystem access, network filtering, resource limits. An AI agent is just a process that happens to benefit from all three.

## The pitch

**SSH requires you to open a port. wingthing doesn't.**

**mosh requires you to open a port. wingthing doesn't.**

**tmux gives you session persistence on one machine. wingthing gives you session persistence from any device.**

Start a process on your workstation. Check on it from your phone. Attach from your laptop. Share it with a teammate. Full scrollback. E2E encrypted. No ports, no keys, no client install.

Sandboxing, AI agents, and passkey auth are features. The remote terminal is the product.

## Implementation order

1. **CLI client** (`wt attach`) - proves the protocol works outside a browser, unlocks power users
2. **Multi-attach** - multiple terminals on one session, output broadcast
3. **Full scrollback** - disk-backed session recording, stream on attach
4. **WebRTC P2P** (browser) - pion/webrtc on wing, ICE signaling through roost
5. **QUIC P2P** (CLI) - direct CLI-to-wing with hole punching
6. **Tailscale integration** - auto-detect tailnet, skip roost for connectivity

Each step is independently valuable. The roost gets lighter with each P2P improvement until it's mostly signaling and web UI.

## Open questions

- How much does pion/webrtc add to the `wt` binary? Need to prototype and measure. 8-15 MB estimate is wide.
- Is the ~80-85% P2P success rate for browser-to-daemon good enough, or do users on corporate networks need the relay path to be the assumed default?
- Should the CLI client be a separate binary (`wt-attach`) to keep the main `wt` binary small for users who don't need P2P?
- Full scrollback means writing every session to disk. How much storage per hour of terminal output? Compression ratio for terminal data?
- Multi-attach input conflicts: is interleaved input (tmux model) acceptable, or do users expect locking/turn-taking?
- What's the right auth model for multi-attach? Does each new viewer need passkey auth, or does the session owner grant access?
