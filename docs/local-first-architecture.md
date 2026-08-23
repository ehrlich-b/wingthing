# Local-first runtime architecture

Status: direction proposal and first implementation slice  
Reviewed: 2026-08-08

## Decision in one sentence

**A wing is the durable runtime; everything else is a client, transport, access policy, or deployment bundle around it.**

Wingthing should start locally, require no account, keep real terminal processes alive, and be useful from the terminal where the user already works. `wingthing.ai` is an optional browser client, rendezvous service, and relay fallback. It is not the place the work conceptually lives.

That framing resolves the apparent conflict between “everyone goes to the roost” and “wingthing.ai routes to every wing.” They are different deployment stories built from the same layers, not competing definitions of the product.

## What Herdr got right

This review used Herdr v0.8.0 (`3a76fea`), its public documentation, and its
Apache-2.0 source as of 2026-08-08.

The hype is measurable, even if public counters are not retention data. Herdr's
homepage reported 25,919 GitHub stars and 362,183 installs during this review. A
[May 27 engineering post](https://herdr.dev/blog/live-updates-without-killing-your-terminal-processes/)
reported 2.6k stars and more than 15k downloads only a few months after launch.
That is roughly tenfold star growth and at least twenty-four-fold install growth
in about ten weeks. It is strong evidence that the positioning found a nerve.

The hype is not really about rewriting tmux. The important choices are:

1. **The product is a runtime, not a dashboard.** A background server owns real PTYs and processes. The TUI, CLI, and remote connections are replaceable clients. Closing every client does not affect the work.
2. **Local is the complete product.** Install one binary and run it. No login, hosted account, proxy, or new terminal emulator is required.
3. **Remote means SSH first.** `herdr --remote workbox` uses the user's existing OpenSSH configuration and authentication. A local thin client bridges the remote server protocol over SSH stdio. Hosted networking is not part of the basic mental model.
4. **It preserves the user's terminal.** Herdr runs inside Ghostty, iTerm, Kitty, Alacritty, or an SSH client. It does not ask the user to move their work into a web or desktop app.
5. **It names the server/client split explicitly.** The server owns state; clients render and send input. That makes a TUI the first client, not the architecture.
6. **Agent semantics are additive.** A pane is always a raw terminal. An agent is a recognized occupant of a pane with semantic state such as `working`, `blocked`, `done`, or `idle`.
7. **Humans and agents use one control surface.** CLI wrappers, a local socket API, and the agent skill expose the same primitives: create, read, send, wait, split, and attach.
8. **It has a legible hierarchy.** Workspace → tab → pane is enough structure to navigate many concurrent processes without making collaboration, billing, or identity part of terminal state.

Primary references:

- [Herdr overview](https://herdr.dev/)
- [Herdr comparison and runtime/client framing](https://herdr.dev/compare/)
- [Herdr concepts](https://herdr.dev/docs/concepts/)
- [Persistence and SSH remote attach](https://herdr.dev/docs/persistence-remote/)
- [Agent automation primitives](https://herdr.dev/docs/agent-automation/)
- [Socket API](https://herdr.dev/docs/socket-api/)
- [Herdr source](https://github.com/herdrdev/herdr)

### What not to copy yet

- A full terminal multiplexer UI. Wingthing already owns one persistent PTY per egg and has a browser multi-session surface. A whole workspace/tab/pane renderer is a major product, not a prerequisite for native attach.
- Hundreds of screen-scraping rules as the primary source of agent state. Prefer official hooks and integrations; use terminal heuristics as a fallback.
- Automatic remote binary installation in the first SSH release. First make the protocol and command stable; installation can follow.
- Herdr's exact object model. Wingthing's sandbox, identity, audit, privileged tools, browser client, and shared appliances are real differentiators.

### The next ideas worth stealing

Herdr's second-order choices are at least as valuable as its pane UI:

1. **One status authority, with evidence.** Native lifecycle hooks should win
   when complete; otherwise a bounded screen rule may classify the live bottom
   buffer. Every state should carry its source, matched evidence, and fallback
   reason so `wt agent explain` can answer why an agent looks blocked or idle.
2. **Unknown is not success.** Waiting for terminal text, waiting for process
   exit, and waiting for an agent lifecycle transition are different APIs. A
   prompt that never leaves idle should fail as stalled; an unclassified agent
   should not satisfy a successful-completion wait.
3. **Observers and controllers are different sessions.** Many clients may
   consume rendered frames. Exactly one client owns input and resize, with an
   explicit, audited takeover. This is both better collaboration UX and a
   cleaner authorization boundary than treating every attachment alike.
4. **Keep the remote client local.** SSH should carry a transport-neutral client
   protocol, not merely start a remote UI. That preserves local keybindings,
   clipboard and notifications while the authoritative runtime remains on the
   remote wing.
5. **Make the control contract inspectable.** The installed binary should emit
   the exact versioned protocol schema it implements. CLI, MCP, plugins, and
   long-lived event subscribers should be progressively richer adapters over
   the same semantics rather than parallel feature surfaces.
6. **Keep workflow growth out of the core.** A small manifest-based executable
   plugin contract can own layouts, hooks, review boards, and project-specific
   flows. Wingthing should provide invocation context, policy, logs, terminal
   placement, and the stable API; plugins should own their language and durable
   state.
7. **Name the restore path precisely.** Live detach, process restart, terminal
   screen replay, and native agent conversation resume preserve different
   things. Product copy and tests should never collapse them into one vague
   claim that a session “persists.”

These ideas reinforce Wingthing's differentiation rather than turning it into a
Herdr clone: status is policy-relevant, observer/controller separation is a
collaboration primitive, schemas make the LLM-facing meta-layer dependable,
and plugins can inherit Wingthing's sandbox and audit boundaries.

## What Wingthing already has

Wingthing did not miss the runtime. It buried it under the network and organization stories.

- An egg owns a real PTY and agent process in a detached process session.
- The egg exposes bidirectional PTY I/O over a local authenticated gRPC socket.
- The egg keeps bounded replay state and a VTE snapshot with scrollback for reattach.
- Multiple gRPC readers can attach to a live egg.
- A wing discovers, starts, reclaims, and routes persistent eggs.
- Browser ↔ wing terminal content is application-encrypted through the relay;
  see `docs/security.md` for metadata, TOFU, web-code, and forward-secrecy limits.
- WebRTC can migrate browser traffic to a direct data channel with relay fallback.
- A direct WebSocket server exists for browser clients on reachable networks.
- Sandboxing, auditing, per-user homes, path policy, and privileged tools already sit next to the PTY runtime.

The older [`remote-tmux.md`](remote-tmux.md) even listed a CLI client as implementation priority one. The missing piece was a boring native product surface, not a new terminal core.

## The five layers

Every feature and noun should belong to exactly one of these layers.

| Layer | Owns | Must not own |
|---|---|---|
| **Runtime: wing** | eggs, PTYs, process lifecycle, terminal state, agent state, local policy | hosted accounts, billing, global routing |
| **Clients** | rendering, navigation, local input, clipboard, notifications | process lifetime, authoritative session state |
| **Transports** | bytes and reconnection between a client and a wing | org semantics, session ownership, UI hierarchy |
| **Collaboration policy** | identity, grants, roles, view/control rights, audit attribution | PTY state, network topology, deployment shape |
| **Deployment** | which components run together and how they are operated | new product semantics |

### Runtime: the wing

The wing is the thing that remains when every UI closes. It is authoritative for session IDs, PTYs, replay, agent processes, sandbox policy, and local metadata. Local access through Unix sockets must always work without a roost or account.

The egg remains the isolation and failure boundary for one terminal process. Over time the wing can expose a single local control socket that indexes eggs and presents a stable API to every client.

### Clients

Clients should be peers, not tiers:

- native single-session attach (`wt attach`)
- a future native session navigator/TUI
- the existing browser UI
- automation through CLI/socket/MCP
- mobile or editor clients later

The browser is valuable because it is universal. It is not the definition of remote access.

### Transports

Choose the simplest reachable transport, then fall back:

1. local Unix socket
2. SSH stdio using the user's SSH config, agent, VPN, or tailnet
3. direct LAN/tailnet socket
4. peer-to-peer negotiated path
5. encrypted relay through `wingthing.ai` or another gateway

The session protocol above these transports should converge. A client should attach to a wing and session; it should not need separate behavior because a relay happens to carry the bytes.

“Local-first” does not mean “local-only.” It means the runtime is complete without the cloud and the cloud does not become the source of truth when enabled.

### Collaboration policy

Collaboration is a relationship between principals and runtime resources:

- who may discover this wing?
- who may see this session?
- who may watch output?
- who currently owns input and resize?
- who may take over control?
- who may start or kill a session in this path/sandbox profile?
- whose action appears in the audit log?

An organization can answer who the principals are, but it is not a terminal namespace and should not be embedded into session transport. The core API should accept resolved principals and grants. Hosted orgs, a self-hosted identity provider, SSH keys, and a local Unix user can all resolve into that model.

### Deployment: roost

A roost is a deployment bundle: a wing plus a gateway/web service operated together. It is useful for a shared server, a team appliance, a homelab, or a one-command self-hosted install.

A roost is not:

- the universal center every personal wing must join
- a synonym for the hosted relay
- a collaboration group
- the owner of terminal state

Other wings may register with a roost's gateway, but that does not make their runtimes part of the roost's local wing. This distinction should eventually be reflected in flags and internal names: `--gateway` or `--relay` describes a transport endpoint more accurately than `--roost`.

## The two valid Wingthing stories

### Personal runtime

```text
local/SSH/native/web client ──transport──> wing ──local socket──> eggs
                                            │
                                            └── authoritative state
```

The user installs `wt`, starts agents on their own machine, detaches, and reattaches. They may enable `wingthing.ai` for browser access, discovery, and NAT/firewall fallback. Collaboration is explicit sharing of a wing or session.

This should be the default onboarding and README story.

### Shared runtime appliance

```text
team clients ──SSH or HTTPS──> roost gateway + local wing ──> shared eggs/tools/repos
                                      │
                                      └── team access policy
```

This is what the Slide deployment actually is: an Ansible-managed, always-on roost host with centrally synchronized repositories, role-scoped paths, per-user homes, audit, and privileged tools. It is successful because it is a shared agent workstation/appliance, not because every employee installed a wing and joined an org.

That deployment is a first-class product shape. It should be described as a **team runtime** or **shared roost**, not used as the internal model for personal Wingthing.

### Hosted gateway: keep it optional, do not delete it

The local/SSH path removes `wingthing.ai` from the critical path; it does not
remove the hosted product. An outbound relay is still materially useful when a
wing has no acceptable inbound route, when the client is a browser or phone, or
when identity, sharing, discovery, notifications, and support should work
without operating a gateway.

The product promise should therefore be **managed reachability for a runtime the
customer owns**, not “your agents run on our service” and not “all use must pass
through us.” A paid plan has to sell convenience and collaboration—reliable
rendezvous/relay, browser/mobile clients, identity and grants, team policy,
notifications, support, and bandwidth—not hold local terminal persistence
hostage. SSH remains the excellent free path for a laptop that can already
reach a VM.

Application encryption is part of that value, with the limits in
[`security.md`](security.md): routing metadata is visible, the hosted service
delivers the browser code, and a compromised wing/client endpoint is outside
the promise. Native clients and self-hosting remain the higher-assurance paths.

## Why the org work felt kludged

The organization feature was asked to solve too many unrelated problems:

- SaaS billing and seats
- discovery of other people's wings
- access to a shared wing
- role assignment
- per-folder authorization
- per-user filesystem isolation
- session ownership and filtering
- special browser rendering in `roost_mode`

Those are legitimate needs, but combining them made “org” both an account object and a runtime mode. The code then had to ask whether it was in roost mode to decide which product it was rendering.

The correction is not necessarily to delete organizations. It is to narrow them:

- **Org:** hosted identity, membership, and billing convenience.
- **Grant/policy:** authorization to a wing, path, tool set, or session.
- **Roost:** a deployable gateway + local wing.
- **Wing:** the runtime.
- **Session:** a terminal owned by a wing, with explicit creator/viewer/controller identity.

The personal and shared-appliance stories can then use the same runtime and policy model without pretending they have the same topology.

## Native attach: first slice

This branch adds the deliberately boring path:

```bash
wt egg claude                         # Ctrl+B Q detaches; egg keeps running
wt attach                             # list local live sessions
wt attach <session-id>                # reattach in the current terminal
wt attach <session-id> --remote box   # SSH to `box`, reattach there
```

`box` is an ordinary OpenSSH destination or alias. Authentication, bastions, VPN/tailnet routing, ports, and host keys stay in `~/.ssh/config`. The remote host needs `wt` installed; `--remote-binary` handles a nonstandard path.

This is intentionally not routed through `wingthing.ai`. It proves the native client path and restores the local-first invariant. The existing browser and relay paths continue to work.

The current SSH implementation runs the small attach client on the remote host and carries ANSI/input over an allocated SSH TTY. A later transport-neutral client protocol can keep the client-side state machine local, as Herdr does, when that creates concrete benefits such as local clipboard integration or seamless transport fallback.

The native client can also discover the online wings available to its relay
identity:

```bash
wt wings
wt wings --json
wt wings --roost https://roost.example.com --json
```

The relay supplies only the authorized online roster and routing public keys.
The CLI pins each first-seen wing identity and obtains hostname, version, agents,
and projects with an application-encrypted `wing.info` probe. This is hosted
rendezvous, not wing-to-wing discovery, and it does not yet make remote terminal
control available through the native CLI.

## Local runtime surface: second slice

The vacation feature branch extends the same egg runtime beyond named agents:

```bash
wt terminal --name work                  # persistent shell
wt terminal --name api -- npm run dev    # persistent command
wt egg claude --name research            # named agent
wt attach --select                        # native picker
wt attach research                        # resolve name or ID
```

Local automation uses the authenticated egg sockets rather than the browser or
hosted relay:

```bash
wt session ps --json
wt session read api
wt session send api r --enter
wt session wait api --contains ready
wt session rename api frontend
wt session kill frontend
```

These are deliberately raw terminal primitives. `read` returns ANSI terminal
state; `send` sends PTY bytes; `wait` observes live output or I/O idleness. They
do not pretend terminal activity is agent state. The future agent-aware API can
build typed events and `working`/`blocked`/`done` semantics above them.

Session names live beside immutable session IDs and are resolved locally on the
machine that owns the egg. This keeps naming out of hosted organizations and
global routing.

## Roadmap, in order

### P0: make the runtime obvious

- [x] Ship native detach/reattach locally and over SSH.
- [x] Lead documentation with local persistence; present the web relay as optional.
- [x] Add stable human-readable session labels alongside immutable IDs.
- [x] Add persistent shells and arbitrary commands alongside agent sessions.
- [x] Let the native CLI list and encrypted-probe wings available through a roost.
- [ ] Dogfood before post-vacation promotion.
- [ ] Make `wt attach` one client protocol across every transport.

### P1: one local control surface

- Add a wing-owned local socket API that lists sessions and reports snapshots/events.
- Move clients away from per-egg filesystem discovery.
- [x] Expose raw list/read/send/wait operations through the local CLI.
- [x] Expose local terminal and agent orchestration to LLM clients through MCP stdio.
- [x] Add structured one-shot prompt, bounded loop, and dependency-DAG swarm primitives.
- [x] Add named prompt templates with immutable revisions and task provenance.
- Expose the same control semantics through browser tunnel and other transports.
- Separate raw terminal operations from agent-aware operations.

### P2: agent awareness

- Define states: `working`, `blocked`, `done`, `idle`, `unknown`.
- Prefer native agent hooks and session metadata.
- Add a bounded terminal-heuristic fallback for agents without hooks.
- Make state transitions events so humans and agents can wait instead of poll.
- Record state authority and evidence; add `wt agent explain`.
- Treat stalled startup and `unknown` as explicit non-success outcomes.

### P3: collaboration primitives

- Model `viewer` and `controller` separately.
- Use an explicit single-controller lease with visible takeover rather than accidental concurrent resize/input.
- Grant access at wing, path/profile, or session scope.
- Keep identity resolution pluggable: local user, SSH key, hosted account/org, or self-hosted OIDC.
- Expose read-only frame observers separately from the controller lease.

### P4: transport convergence

- Put local, SSH, direct, P2P, and relay paths behind one attach protocol.
- Prefer direct reachability and make relay fallback observable.
- [x] Let `wingthing.ai` provide optional discovery/rendezvous without owning session metadata.
- Keep the browser as a first-class client on the same protocol.
- Emit a versioned control-protocol schema from the installed binary.
- Keep the native SSH client local so clipboard, keybindings, and notifications remain client capabilities.

### P5: product/UI cleanup

- Replace `roost_mode` product branching with capabilities returned by the runtime/policy layers.
- Rename transport configuration away from `roost` where it really means gateway/relay URL.
- Present shared roosts and personal wings as selectable targets, not different applications.
- Decide later whether a full multiplexer TUI is worth building; do not block the runtime/client cleanup on it.

## Product position

The clean version is:

> Wingthing is the local-first runtime and meta-access layer for persistent,
> sandboxed agent terminals. Humans and models can use it from the terminal,
> over SSH, through MCP, or in the browser; share a session or run a whole team
> roost when collaboration calls for it.

Herdr owns the “agent-aware tmux replacement” lane. Wingthing should not race to be a less mature Herdr. Its stronger position is the runtime that combines terminal persistence with sandbox boundaries, auditable collaboration, privileged tools, and an excellent browser escape hatch—while still being completely useful with one binary and no account.

The semantic orchestration layer is detailed in
[`agent-meta-layer.md`](agent-meta-layer.md). It treats terminals, prompt runs,
loops, swarms, and collaboration grants as separate composable objects.
