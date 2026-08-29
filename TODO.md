# TODO — wingthing

**An agent manager for agents.** One control plane for durable agents across the
machines where their code and credentials already live.

The current product thesis, real user stories, implementation gaps, security
invariants, release gates, and next coding slice are recorded in
[`docs/agent-manager-product-brief.md`](docs/agent-manager-product-brief.md). Read
that brief before continuing `feature/direct-control-free-tier`; this backlog alone
does not describe the end-to-end agent-manager product.

## Where We Are

Wings are live. PTY relay works end-to-end. E2E encryption, passkey auth, org support,
per-process egg sandbox, folder-based ACLs (per-path member lists), `wt wing config`
with live SIGHUP reload, and `wt roost` for single-process self-hosted mode. Production
currently uses one Fly `login` machine (shared-cpu-2x, 512MB) with the SQLite volume;
horizontal scaling is built and tested, while the `edge` process remains disabled and
scaled to zero in `fly.toml`.

VTE reconnect and opt-in browser WebRTC migration are implemented, with legacy
replay and terminal-specific cleanup still present. Browser-direct transport is not
the free hosted terminal path: free remote MCP is direct, while browser terminal
startup still requires relay entitlement or a self-hosted roost. The current
architectural direction is to make the existing runtime local-first and
client-agnostic, then layer collaboration on top; see
`docs/local-first-architecture.md`. The former August vacation freeze is an expired
historical record; current scope and promotion gates live in the agent-manager
product brief and CI workflows.

---

## Enterprise Blocker: Project Discovery in Multi-Role Repos

Git repo parent (`ai-playground/`) swallows role subdirs (`dev/`, `qa/`, etc.) that have
`egg.yaml`. `scanDir` finds `.git` and stops — subdirs never appear as projects. Users
land in the parent (no egg.yaml → wrong sandbox), can't select the role dir they need.

**Fix**: after finding a git repo, check immediate children for `egg.yaml`. If any exist,
offer those children as projects instead of (or in addition to) the parent. Path ACLs
complicate this — members may only see specific subdirs.

**Files**: `cmd/wt/wing.go:462-496` (`scanDir`)

---

## MVP — Demo-Ready

The bar: someone new can use a wing without confusion or broken UX.

### Docs
- [x] Update docs for orgs, passkeys, wing config, allow/revoke, lock/unlock
- [x] Self-hosting guide: `wt serve` on your own box, what you get, how sandbox works
- [x] Architecture overview: gateway/wing responsibilities, visible routing
  metadata, wing-owned session state, and application payload encryption

### UX Polish
- [ ] Split org and personal wings in dashboard UI — personal vs work icon when tabbing through wings
- [ ] Fix command palette passkey prompt — 1) never auto-prompt for passkey (require Enter), 2) passkey completion should actually unlock the palette
- [x] Session ID in URL on session start
- [x] Close / end session from terminal header
- [x] Ctrl-V paste + Ctrl-C copy on Windows
- [x] Auto-reconnect UI without navigate-away
- [x] Fix cursor ghost typing — re-inject cursor hide after replay buffer trim (Claude)
- [x] Deep link reattach — `#s/<sessionId>` works on refresh
- [x] Passkey challenge UI — button prompt (no auto WebAuthn popup)
- [x] Wing offline reconnect — browser shows banner + auto-reattach
- [ ] Fix cursor preamble for other agents (codex, cursor, ollama) — same pattern, lower priority
- [x] Fix notifications — multi-tab dedup via BroadcastChannel, nonce-based ntfy dedup, isViewingSession suppression
- [ ] Latency pass — audit round-trip times, find low-hanging optimizations
- [ ] Rescan `paths` for new folders — wing only discovers project directories at
  startup, so new folders under configured paths don't appear until `wt stop && wt start`.
  Add periodic rescan (e.g. every 60s) so the directory listing stays fresh without restart

### Self-Hosting First Class
- [x] `wt serve` should work standalone with zero config for single-user self-hosted
- [x] Local user mode: auto-grant pro tier, no bandwidth cap for self-hosted
- [x] Hide orgs UI in self-hosted roost mode — covered by the organization-mode
  browser E2E suite
- [x] Uniform 3 Mbit/s rate for all tiers, only monthly cap differentiates free vs pro

---

## 0.1 — Ship Week

### Core Features
- [x] Native shell and arbitrary persistent commands — `wt terminal` / `wt new`
- [x] Egg reattach on CLI — `wt attach <id>` locally or `--remote <ssh-host>`;
  next converge SSH, direct, P2P, and relay behind one attach protocol
- [x] Human-readable session names, native picker, and local read/send/wait CLI
- [x] Local MCP meta-access — terminal control, agent prompt runs, bounded loops,
  dependency-DAG swarms, durable task output, and versioned prompt assets; see
  `docs/agent-meta-layer.md`
- [x] Trusted outer-boundary mode for dedicated AI VMs — local CLI and MCP can
  keep durable sessions without requiring nested Ubuntu user namespaces; see
  `docs/sandboxed-ai-vm.md`
- [ ] PTY watch mode — multiple concurrent consumers of same PTY (pair programming, monitoring)

### Revenue
- [ ] Turn on Stripe — paid tier for hosted relay (self-hosted is always free/unlimited)

### Performance
- [ ] WebSocket direct to Fly — bypass Cloudflare for ws:// traffic (ws.wingthing.ai)

### Security
- [ ] Stop storing JWTs in device_tokens — new ES256 tokens should be stateless (verify
  by signature only). Remove `CreateDeviceToken` call from JWT issuance, remove
  `ValidateToken` fallback from wing/PTY auth paths. Keep device_tokens for local mode
  UUID tokens and web session auth only. Can force re-login to flush old HS256 tokens.
- [x] Ubuntu 24.04 AppArmor support — probe real user+mount namespace operations,
  fail before agent launch, install an executable-scoped profile through
  `wt doctor --fix`, verify effective mount state, and exercise a readable
  denied-file canary as an unprivileged user on Ubuntu 24.04/kernel 6.8.
- [x] Encrypt pty.resize and pty.kill through the tunnel; wing rejects plaintext relay controls
- [x] Tunnel passkey replay protection — `passkey.auth.begin`/`finish`, one-time wing nonce, full WebAuthn context validation, client-bound token
- [ ] Authenticated ephemeral wing handshakes — replace TOFU-only static-wing ECDH with verified pairing and forward secrecy
- [ ] Bind encrypted envelopes to wing/session/type/direction/request with AEAD associated data and replay counters
- [x] Internal API baseline — Fly nodes require a cluster-private source and
  production-shaped Fly server config; non-Fly split deployments require a distinct
  `WT_INTERNAL_SECRET`, which every built-in node client propagates. JWT signing
  material is never accepted as an HTTP secret.
- [ ] Add cryptographic Fly node identity (mTLS or signed service tokens) so a split
  deployment need not trust every application on the Fly organization's 6PN. Until
  then, set `WT_INTERNAL_SECRET` on Fly when that network trust is too broad.
- [x] Invite consume transaction ordering — invite claim, membership, seat check, and
  entitlement grant commit in one transaction, with rollback regressions.

---

## Shipped foundations that still need cleanup

### VTE: Server-Side Virtual Terminal Emulator
The VTE snapshot reconnect path is shipped. The 2MB raw replay path and
`findSafeCut`, `trackCursorPos`, and `agentPreamble` compatibility code remain for
fallback and older modes; remove them only after the VTE path has enough field time.
See `docs/vte/README.md` for the current phased cleanup plan.

### P2P: WebRTC Direct Connection for Same-LAN Wings
Opt-in `p2p`/`p2p_only` browser migration and the native direct-MCP WebRTC transport
are shipped. Browser P2P still begins from entitled or self-hosted signaling and is
not a browser-direct free hosted terminal. See `docs/p2p_design.md` for the current
transport design.

---

## Backlog

### PTY: UTF-8 boundary safety in replay buffer trim and chunking

- [x] Replay-buffer trimming and independently compressed web replay chunks now
  move a proposed cut back at most one UTF-8 sequence. Focused tests place a
  four-byte emoji across each former boundary and retain bounded behavior for
  arbitrary invalid PTY bytes.
- [ ] Surface browser replay decompression errors instead of silently dropping a
  failed chunk. UTF-8 splitting no longer creates that failure, but corrupted or
  truncated compressed data should still produce visible diagnostics.

---

- [ ] Image paste into terminal — intercept paste, upload via PTY, buffer output, loading bar
- [ ] Offline web app — PWA with cached wing data, works without network
- [ ] Facilitate worktrees — dev workflow for parallel feature branches
- [ ] GUI streaming — H.264 over WebSocket for graphical agent windows (Cursor, etc.)
- [ ] Wing-to-wing communication — wings coordinate via shared thread
- [ ] Context sync — teleport CLAUDE.md, memory files to wings on connect
- [x] CI + release pipeline + badge — GitHub Actions (migrated off cinch 2026-06-16)

---

## Code Cleanup — Review Findings

Findings from deep code review. Bug fixes for unchecked errors in passkey auth,
PID file writes, gzip log rotation, tunnel type assertion safety, and tunnel
retry depth limit are already landed. Below is what remains.

### Go: Split wing.go (3,140 lines)

The single biggest structural issue. `cmd/wt/wing.go` is a god file containing
PTY session handling, tunnel dispatch, egg management, audit streaming, passkey
verification, attention state, project discovery, and log rotation.

**Split into:**
- `cmd/wt/pty.go` — `handlePTYSession`, PTY output goroutine, replay chunking
- `cmd/wt/tunnel.go` — `handleTunnelRequest`, tunnel inner dispatch, tunnel key cache
- `cmd/wt/audit.go` — audit streaming, audit recording playback
- `cmd/wt/wingutil.go` — attention state, project discovery, log rotation, egg cleanup

**Also:**
- [ ] Extract PTY output goroutine into shared helper — identical ~50-line
  encrypt-and-forward block is copy-pasted 4x across initial connect and
  reattach paths
- [ ] Collapse 3 attention `sync.Map`s (`wingAttention`, `wingAttentionCooldown`,
  `wingAttentionNonce`) into one map to a struct
- [ ] Refactor 10-13 parameter function signatures into config/context structs:
  `runWingForeground` (11 params), `handleTunnelRequest` (13 params),
  `handlePTYSession` (10 params)
- [ ] Remove `goto authDone` in PTY passkey auth — restructure into early-return
  or extracted function

### Go: Relay concurrency follow-up

- [x] `bandwidth.go` month rollover is serialized under the meter lock.
- [x] `WingRegistry` publishes immutable connection snapshots; config and heartbeat
  updates replace rather than mutate a previously returned entry.
- [x] Unconfirmed PTY routes and viewers are bounded and expired; browser disconnect
  cleanup and wing-offline notification preserve reconnect behavior without an
  attacker-growable provisional map.
- [x] Notification nonce dedup is server-scoped, per-user, and bounded to 10,000
  insertion-ordered entries.

### JS: Split render.js (2,135 lines)

Same god-file problem on the frontend. Contains wing rendering, session tabs,
account management, org settings, audit display.

- [ ] Split into `renderWings.js`, `renderSessions.js`, `renderAccount.js`,
  `renderOrg.js`, `renderAudit.js`
- [ ] Fix event listener leaks in sidebar re-renders — `renderSidebar()` calls
  `addEventListener` on every tab without removing old listeners, so after N
  re-renders each tab has N click handlers. Use event delegation on the
  container instead
- [x] Investigated the session switching guard: `swept` means confirmed by the
  latest wing inventory sweep. The guard intentionally refuses cached sessions after
  a wing goes offline; tests cover the reconciliation state. Rename the field in a
  later UI cleanup if the terminology continues to confuse readers.

### JS: Async correctness

- [ ] `data.js` `probeWing()` dedup — `_probeInflight` is deleted in `.finally()`
  before callers resolve, creating a race window for duplicate probes
- [ ] `bytesToB64()` in `helpers.js` uses O(n²) string concatenation in a loop
  on every encrypt/decrypt — use `String.fromCharCode.apply(null, bytes)` or
  typed array approach
- [ ] `terminal.js` `saveTermBuffer()` debounces at 500ms and clears a session's
  buffer on deletion, but still serializes up to 200KB per retained session with no
  global quota. Add oldest-entry eviction or a total storage budget.

### Tests

- [x] Add config regressions for loading, concurrent wing-ID creation, variable
  resolution, missing-config fallback, atomic persistence, and additive wing policy.
- [ ] Add tests for agent adapters — `claude.go` (145 lines, 0%), `codex.go`
  (119 lines, 0%), `cursor.go` (81 lines, 0%). At minimum test stream parsing
- [ ] Remove compile-time interface checks from runtime test functions
  (`var _ Agent = (*Gemini)(nil)` in gemini_test.go, ollama_test.go) — these
  don't execute at runtime

### Cross-harness agent handoffs

- [x] Prove a live Codex–Claude conversation using an owner-local mailbox and
  exact-TTY notification while dogfooding the Arli workload.
- [x] Add owner-scoped durable message objects and typed `message_send`,
  `message_wait`, and `message_list` controls. Local stdio clients may declare
  one owner with distinct actors; OAuth roost clients derive owner and actor
  from authentication. Proved two-way Codex/Claude actor exchange, cursor
  replies, blocking wait, cross-owner isolation, TTL bounds, and content-free
  audit records through real stdio processes.

### Narrow agent egress

- [x] Add `network.agent_domains: none`, exact-host derivation from
  `WT_PROVIDER_BASE_URL`, strict provider URL validation, and visible derived
  and suppressed provenance in `wt egg explain`.
- [ ] Run the same live egress conformance table on macOS and Linux: one allowed
  provider connection, denied vendor domains, same-IP host separation, raw-IP
  bypass, DNS behavior, loopback ports, observe mode, and the default-merge
  compatibility row. Linux enforce mode depends on the netns proxy path in
  `docs/sandbox-enhancement-design.md`.

### Factory deploy environments

- [ ] Add owner-scoped `environment_create`, `environment_status`,
  `environment_stage`, `environment_run`, `environment_evidence`, and
  `environment_destroy` controls with closed schemas, TTLs, resource bounds,
  transaction IDs, and audit events.
- [ ] Add a Proxmox provider adapter bounded to an operator-configured pool,
  VMID range, template set, storage, bridge, and SSH bootstrap. Prove concurrent
  ticket environments and exact-target cleanup.
- [ ] Package the software-factory skill: PRD/context admission, ticket
  workspace creation, Terra/Opus implementation and independent review loops,
  immutable artifact digest, deploy evidence, stacked-PR preparation, and a
  deliberate human publish/review step.
- [ ] Move the Ubuntu factory canary from manual SSH/SCP into those roost
  operations. Preserve the current denied-file negative control and include
  one readable filesystem control in the evidence bundle.

### Private roost proving ground

- [ ] Dogfood a private roost on `ehrlich.dev`, with its Hopper as a real
  owner-scoped workload target. Prove private enrollment, personal Codex and
  Claude login homes, typed task control, sealed workspace access, model
  selection, lifecycle supervision, and audit records end to end. Keep this as
  the final proving ground for the programmable-roost route after the shared
  roost foundations above are complete.

---

## Done

<details>
<summary>Completed milestones (click to expand)</summary>

### v0.1 — Core Pipeline
Tagged v0.1.0 — 12 packages, ~129 tests, ~5500 lines Go.
Foundation, config, store, memory, agent (claude), parse, skill, sandbox stubs,
thread, orchestrator, timeline, transport, CLI, daemon, integration tests.

### v0.2 — Production Runtime
Tagged v0.2.0 — 15 packages, 7 integration tests, ~8500 lines Go.
Ollama adapter, Apple seatbelt sandbox, Linux namespace sandbox, cron/recurring tasks,
retry policies, cost tracking stubs, agent health checks.

### v0.3 — Sync + Relay
Tagged v0.3.0 — 21 packages, ~12000 lines Go.
Memory sync engine, WebSocket client with auto-reconnect, device auth (`wt login`),
relay server (`wt serve`), web UI (PWA), session management, encrypted sync.

### v0.4 — Skill Registry + Agents
Gemini adapter, skill registry at wingthing.ai/skills, E2E encryption (X25519 + AES-GCM),
task dependencies, multi-machine thread merge, 59 curated skills with verified URLs.

### v0.5 — Social Feed
Semantic link aggregator (wt.ai/social), 159 spaces, embedding-based assignment,
hot/new/best sort, RSS pipeline, compress bot, voting, comments, GitHub/Google OAuth.

### Wings (v0.6-v0.44)
Per-process egg architecture, PTY relay (browser ↔ relay ↔ wing ↔ egg), E2E encrypted
tunnel protocol, passkey auth with allow/revoke, org support, seatbelt + namespace sandbox
with agent auto-drilling, `wt wing config` with live SIGHUP reload, horizontal scaling
(login + edge nodes with fly-replay routing), codex + cursor adapters, audit logging,
session replay, wing lock/unlock, project discovery, directory browsing.

### Folder ACLs (v0.48)
Per-path member lists in wing.yaml, enforce on PTY start and tunnel requests,
web UI for path management. Three-tier enforcement: PTY start (CWD clamp + egg.yaml
requirement for members), tunnel requests (filtered dir/session/audit responses),
admin-only path management (paths.list/set/add_member/remove_member).

### Org Mode Completion (v0.55)
Admin session management (view/disconnect/replay live sessions), real-time kick on
ACL revocation (`killSessionsViolatingACLs`), mid-session audit replay (gzip flush
every 100 frames, `streamAuditData` tolerates incomplete gzip).

### Roost: Combined Relay + Wing Mode (v0.56)
`wt roost` runs relay and wing in a single process. Daemon mode with `roost.pid`,
foreground mode for systemd. Unified PID lookup (`readPid` checks both `wing.pid`
and `roost.pid`), `wt update` auto-restarts the correct daemon type. Signal handling
refactored: `runWingWithContext` takes caller-owned context + SIGHUP channel.

</details>
