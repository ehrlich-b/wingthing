# wingthing as remote tmux

> Historical framing note: the local-first direction and layer boundaries are now
> developed in [local-first-architecture.md](local-first-architecture.md). The CLI
> attach priority described below is now available as a first-class SSH route:
> `wt --remote <host>`, including launches, attach, and bounded session control.

Session persistence from any device. That's the pitch. SSH gives you a shell. mosh gives you a roaming shell. tmux gives you persistent sessions on one machine. wingthing gives you persistent sessions accessible from anywhere - your phone, your laptop, a browser on someone else's computer.

This doc frames the project as a remote terminal tool first. AI agents and sandboxing are features on top.

## The gap in the market

SSH assumes inbound connectivity. You need an open port, a static IP or DDNS, and key management. Behind a NAT? Port forward. Behind a corporate firewall? Tough. On cellular? Good luck.

mosh fixes roaming and local echo but inherits the inbound port requirement. No NAT traversal, no relay fallback. UDP blocked? mosh doesn't work.

tmux solves session persistence but only locally. You still need SSH to reach the machine, and you're back to the same inbound connectivity problem.

All three give you a single terminal connection. Want to check on a long-running process from your phone while your desktop is still attached? You need tmux inside SSH, and both machines need to reach the host.

## What wingthing does today

**Outbound-only connectivity.** The wing connects outbound to a roost. No ports to open, no static IP. Works behind any NAT, any firewall, any cellular network.

**Application encryption.** Terminal I/O is encrypted between the shipped browser client and wing (X25519 + AES-256-GCM), so the normal roost forwarding path receives ciphertext. The hosted web client and initial TOFU wing-key pin are part of the trust boundary; this is not a claim that malicious service-supplied JavaScript cannot read a session.

**Session persistence with VTE snapshots.** A server-side virtual terminal emulator (`charmbracelet/x/vt`) captures full terminal state in the egg process. On reconnect, the egg sends a VTE snapshot (current screen + scrollback) instead of replaying raw bytes. This is the same architecture as tmux and mosh - a userspace terminal emulator sits between the PTY and the network. Close your laptop, open your phone, reattach. The browser gets the current screen state instantly.

The VTE also maintains a 50,000-line scrollback ring buffer. Lines that scroll off the top of the terminal grid are captured and stored, same as a local terminal emulator like ghostty or iTerm. On reconnect, scrollback is included in the snapshot. Start a build, go to lunch, come back, scroll up to see what happened.

**Fresh client keys, without a forward-secrecy claim.** Each browser tab generates
a fresh X25519 keypair in session storage, but the wing's X25519 key is persistent.
Later theft of that wing key can therefore expose previously recorded exchanges.
The exact current boundary is documented in [security.md](security.md#no-forward-secrecy-claim).

**Passkey auth.** A passkey is just a P-256 keypair where the private key never leaves your device. The wing stores your public key. On reattach, the wing sends a 32-byte random challenge, the browser calls `navigator.credentials.get()` (biometric/PIN prompt), and the device signs the challenge with ECDSA-SHA256. The wing verifies the signature against the stored public key. Same concept as SSH keys, but the key lives in your device's secure enclave instead of `~/.ssh/`, and you unlock it with a fingerprint instead of a passphrase.

After verification, the wing issues an auth token made from 32 random bytes and
encoded as 64 hexadecimal characters, cached in the browser's sessionStorage.
Subsequent reattaches present the token instead of re-prompting. Tokens are
boot-scoped by default (cleared on wing restart) with optional TTL via `auth_ttl`
in wing.yaml.

Tunnel challenges, assertions, and tokens travel inside the application-encrypted tunnel; PTY ceremonies use fresh wing challenges on the PTY control path. The wing verifies a locally pinned public key and binds tokens to the client key and relay user. This prevents the as-built relay from minting a locked-wing token, but a compromised hosted service can still replace the browser JavaScript; see `security.md`.

**Browser with no client install, or native CLI.** Open a browser when that is
the convenient client, or use the local and SSH `wt` routes. The browser is no
longer the only product surface.

**Sandboxing.** Optional per-session OS-level sandbox (seatbelt on macOS, namespaces + seccomp + cgroups on Linux). Not relevant for plain terminal access, but available when you want to constrain what a process can touch.

## Three independent layers

The terminal stack has three layers that don't know about each other. Changes to one don't affect the others.

**Rendering (browser).** xterm.js today. Possibly ghostty-web (WASM-compiled Ghostty parser, GPU-accelerated) in the future. The renderer receives bytes and paints pixels. It doesn't care how the bytes got there - WebSocket relay, direct P2P, or a local pipe. Swapping the renderer is a frontend change only.

**Session state (egg).** The VTE in the egg process is the source of truth for terminal state. It captures the grid, cursor, modes, scrollback. It produces snapshots on reconnect and passes raw bytes through for the live path. This is independent of how those bytes reach the browser.

**Transport (relay).** Today, entitled hosted browser terminals and self-hosted browser terminals flow through their gateway via WebSocket. Its normal forwarding path carries application ciphertext. Free native MCP is separate and connects directly to a wing after coordination; browser-direct terminal transport has not shipped. The browser transport could later change to WebRTC DataChannels without touching the VTE or renderer. The payload encryption is transport-independent - the same ECDH key agreement and AES-GCM can protect bytes whether they flow through a gateway or directly between peers.

## What's missing

### Multi-attach

Today: one browser per session. Reattach replaces the previous connection.

Goal: multiple terminals attached to the same session simultaneously. Working on your desktop, pull out your phone to check progress, both see the same output. A teammate attaches to watch.

The relay currently tracks one `BrowserConn` per session. Multi-attach means a set of connections, with output broadcast to all. Input from any attached terminal goes to the wing - first-come-first-served, same as two people typing into a shared tmux pane.

Key management is the hard part. Each browser has its own ephemeral key, so the wing can't encrypt once and broadcast. Two options: (a) derive a shared session key that all attached browsers learn during attach, or (b) the wing encrypts separately per browser. Option (a) is cleaner but the key distribution needs thought. Option (b) is simpler but CPU scales linearly with viewers.

### CLI client

The SSH route runs the remote machine's installed `wt` under the ordinary login
user. It covers persistent launches, attach, and session inspection/control:

```bash
wt --remote work1
wt --remote work1 --remote-cwd /home/me/work/project
wt --remote work1 terminal --cwd /home/me/work/project --name shell
wt --remote work1 egg codex --cwd /home/me/work/project --name review --json -- -m gpt-5.6-sol
wt --remote work1 attach review
wt --remote work1 session ps --json
wt --remote work1 session stop review --json
```

The existing `wt attach <session-id> --remote <ssh-host>` spelling remains
valid. Foreground sessions allocate an SSH TTY only when the local input and
output are terminals. JSON and scripted session control use a non-TTY channel,
preserving machine-readable stdout.

Bare interactive entry attaches the only active session, opens a picker for
several, or starts a persistent shell when the inventory is empty. When
`--remote-cwd` is supplied, only sessions in that existing remote directory are
considered. A bare noninteractive call returns the matching inventory as JSON
and does not create a session.

Next goal: converge the local, SSH, direct, P2P, and relayed clients on the same
wing-owned attach protocol. No browser should be required, and the hosted relay
should be an optional transport rather than the product's center of gravity.

**Auth today: let SSH be SSH.** The client uses normal OpenSSH authentication,
then the remote `wt` uses that login user's paths, config, credentials, and
user-private egg sockets. No local provider token, Wingthing state, or SSH key
is copied to the remote host. SSH access accepts the authority of that remote OS
principal; it does not bypass browser or organization policy because those
surfaces are not involved.

A compatible `wt` must already be installed or staged on the remote host.
`--remote-binary` selects its exact path; the client never installs or replaces
it.

Remote `--cwd` paths must already exist. Wingthing does not clone or synchronize
workspaces. Remote `egg.yaml` discovery and sandbox enforcement are identical to
a local invocation on that host. `--unsandboxed` remains an explicit launch
choice and is never added by SSH routing.

The route requires a POSIX remote command shell because each argument is POSIX
shell-quoted before OpenSSH starts the remote binary. Configure ports, bastions,
keys, and host-key policy in OpenSSH. This route does not disable host-key
verification. Agent launches require an explicit provider immediately after
`egg`; provider arguments after `--` remain distinct argv entries. Remote
`session kill`/`stop` and `egg stop` return success only after the existing egg
stop RPC confirms the session process exited. Doctor, MCP, daemon, and browser commands are
deliberately excluded.

If a future native client attaches through the hosted relay without SSH, it will
need its own CLI-friendly authentication. Signing a challenge through
`ssh-agent` is one possible design, but accepting SSH key types in `allow_keys`
is not implemented and should not be implied by the current command.

This is what makes wingthing usable as a daily driver for people who live in the terminal.

### Disk-backed scrollback

The VTE's 50,000-line ring buffer covers most interactive use. But for sessions that run for days (CI, long builds, training runs), you'd want the full history on disk.

The audit recording feature already writes session output to disk. Extending this to serve as the scrollback backing store - stream from disk on attach, then switch to the live VTE path - would give unbounded history. The ring buffer handles the common case. Disk handles the edge case.

## P2P as a transport optimization

This is orthogonal to session management but worth noting.

Entitled hosted and self-hosted browser terminals transit their gateway today. The
gateway cannot read normal terminal ciphertext, but every browser-terminal byte
flows through it, which adds latency and makes that gateway a bottleneck. Native
remote MCP is already direct after coordination and is not part of this proposed
browser transport change.

Tailscale's DERP relays work the same way and solve it with NAT traversal - try to establish a direct connection, fall back to relay when it fails. wingthing could do the same. The roost handles signaling and auth, and falls back to relaying only when hole punching fails.

For browsers: WebRTC DataChannels are the only browser API that does P2P with NAT traversal. pion/webrtc (pure Go, used by LiveKit) handles the wing side. ICE negotiation gets ~80-85% direct connections on residential NATs.

For CLI clients: QUIC gives UDP-based multiplexing with connection migration (handles roaming). quic-go has NAT traversal support.

This is a later optimization. The relay works fine for now. P2P reduces relay exposure to payload ciphertext and traffic volume, but it does not remove signaling, client-distribution, endpoint, or initial-key trust from the security model.

### Tailscale complementarity

If you're already on a tailnet, wingthing doesn't need its own NAT traversal. The wing is reachable at its Tailscale IP. wingthing adds the session layer on top: PTY management, VTE snapshots, multi-attach, scrollback, passkey auth, sandboxing.

A `--relay tailscale` flag (or auto-detection) could skip the roost for connectivity. The roost still serves the web UI and handles discovery for non-Tailscale users.

Tailscale solves the network. wingthing solves the session.

## How AI fits

AI agents are a session type. `wt egg claude` is "start a sandboxed Claude Code session." The underlying machinery - PTY management, E2E encryption, VTE snapshots, remote access - works for any terminal process.

`wt terminal` is a sandboxed shell. `wt terminal -- python` is a persistent
Python REPL. `egg.yaml` controls filesystem access, network filtering, and
resource limits for any session, not just agents.

## Implementation priorities

1. **CLI client** (`wt attach`) - first local + SSH slice implemented; converge transports next
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
