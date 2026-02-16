# TODO — wingthing

**Your agentic swiss army knife.** One CLI, every backend, accessible from anywhere.

## Where We Are

Wings are live. PTY relay works end-to-end. E2E encryption, passkey auth, org support,
per-process egg sandbox, `wt wing config` with live SIGHUP reload. Social feed running
at wt.ai/social. Single Fly node (shared-cpu-2x, 512MB), horizontal scaling built and
tested — edge nodes are one uncomment away in fly.toml.

---

## MVP — Demo-Ready

The bar: someone new (e.g. your boss) can use a wing without confusion or broken UX.

### Docs
- [x] Update docs for orgs, passkeys, wing config, allow/revoke, lock/unlock
- [x] Self-hosting guide: `wt serve` on your own box, what you get, how sandbox works
- [x] Architecture overview: relay is a dumb pipe, wing owns all data, E2E encryption

### UX Polish
- [x] Session ID in URL on session start
- [x] Close / end session from terminal header
- [x] Ctrl-V paste + Ctrl-C copy on Windows
- [x] Auto-reconnect UI without navigate-away
- [x] Fix cursor ghost typing — re-inject cursor hide after replay buffer trim (Claude)
- [x] Deep link reattach — `#s/<sessionId>` works on refresh
- [x] Passkey challenge UI — button prompt (no auto WebAuthn popup)
- [x] Wing offline reconnect — browser shows banner + auto-reattach
- [ ] Fix cursor preamble for other agents (codex, cursor, ollama) — same pattern, lower priority
- [ ] Fix notifications — multiple tabs all fire, only one clears; also never notify about locked wings
- [ ] Latency pass — audit round-trip times, find low-hanging optimizations

### Self-Hosting First Class
- [x] `wt serve` should work standalone with zero config for single-user self-hosted
- [ ] Local user mode: not "pro" but "local" — all features unlocked, no tier restrictions
- [ ] Hide orgs UI in self-hosted mode — orgs are a hosted-relay concept
- [ ] Throughput speed limits configurable, no speed caps by default for self-hosted

---

## 0.1 — Ship Week

### Core Features
- [ ] Native shell — use a wing without any agent installed (plain bash/zsh PTY)
- [ ] Egg reattach on CLI — resume existing sessions from terminal (`wt egg attach <id>`)
- [ ] PTY watch mode — multiple concurrent consumers of same PTY (pair programming, monitoring)
- [ ] Kick revoked users in real time — don't wait for next session, terminate active sessions on revoke/org removal

### Revenue
- [ ] Turn on Stripe — paid tier for hosted relay (self-hosted is always free/unlimited)

### Performance
- [ ] WebSocket direct to Fly — bypass Cloudflare for ws:// traffic (ws.wingthing.ai)

### Security
- [ ] Encrypt pty.resize — cols/rows sent as plaintext, should go through E2E like pty.input
- [ ] Tunnel passkey replay protection — `passkey.auth.begin`/`finish` protocol with server-generated nonce
- [ ] Internal API trust boundary — mTLS or signed service tokens for node-to-node calls
- [ ] Invite consume transaction ordering — race condition in `internal/relay/org.go`

---

## Backlog

- [ ] Image paste into terminal — intercept paste, upload via PTY, buffer output, loading bar
- [ ] Offline web app — PWA with cached wing data, works without network
- [ ] Break down render.js further — it's getting large
- [ ] Facilitate worktrees — dev workflow for parallel feature branches
- [ ] GUI streaming — H.264 over WebSocket for graphical agent windows (Cursor, etc.)
- [ ] Wing-to-wing communication — wings coordinate via shared thread
- [ ] Context sync — teleport CLAUDE.md, memory files to wings on connect
- [ ] Cinch CI — GitHub release pipeline, badges
- [ ] Wing self-update — `wt update` pulls latest release by GOOS/GOARCH

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

</details>
