# TODO — wingthing

**Your agentic swiss army knife.** One CLI, every backend, accessible from anywhere.

## Where We Are

Wings are live. PTY relay works end-to-end. E2E encryption, passkey auth, org support,
per-process egg sandbox, folder-based ACLs (per-path member lists), `wt wing config`
with live SIGHUP reload. Single Fly node (shared-cpu-2x, 512MB), horizontal scaling
built and tested — edge nodes are one uncomment away in fly.toml.

More features for org mode at the top of the list.

---

## Org Mode — Priority

Folder ACLs, sandbox, OAuth, orgs, audit logging all exist. What's missing:

- [x] Admin session management — admins can view and disconnect active sessions
  - `sessions.list` already returns all sessions to admins (no filter)
  - Kill button (X) with confirmation already on every active session row
  - Added: replay + keylog buttons on live sessions (same as past sessions)
- [x] Kick revoked users in real time — terminate active sessions on ACL change
  - `paths.remove_member` and `paths.set` now trigger `killSessionsViolatingACLs`
  - Scans alive sessions, kills any whose CWD is no longer accessible to the owner
  - Creator email persisted in `egg.owner` for post-hoc ACL matching
- [x] Audit log access during active sessions — partial replay of live sessions
  - Replay + keylog buttons on active session rows in web UI
  - Egg gzip writer flushes every 100 frames to make data readable mid-session
  - `streamAuditData` tolerates incomplete gzip (partial data instead of error)

---

## MVP — Demo-Ready

The bar: someone new can use a wing without confusion or broken UX.

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
- [x] Fix notifications — multi-tab dedup via BroadcastChannel, nonce-based ntfy dedup, isViewingSession suppression
- [ ] Latency pass — audit round-trip times, find low-hanging optimizations

### Self-Hosting First Class
- [x] `wt serve` should work standalone with zero config for single-user self-hosted
- [x] Local user mode: auto-grant pro tier, no bandwidth cap for self-hosted
- [ ] Hide orgs UI in self-hosted mode — orgs are a hosted-relay concept
- [x] Uniform 3 Mbit/s rate for all tiers, only monthly cap differentiates free vs pro

---

## 0.1 — Ship Week

### Core Features
- [ ] Native shell — use a wing without any agent installed (plain bash/zsh PTY)
- [ ] Egg reattach on CLI — resume existing sessions from terminal (`wt egg attach <id>`)
- [ ] PTY watch mode — multiple concurrent consumers of same PTY (pair programming, monitoring)

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

## Next Targets — Prove the Concept

### VTE: Server-Side Virtual Terminal Emulator
Replace the 2MB replay buffer with a real terminal state machine (`charmbracelet/x/vt`).
On reconnect, paint the current screen (50 lines) instead of replaying megabytes of raw bytes.
Eliminates `findSafeCut`, `trackCursorPos`, `agentPreamble` hacks. Makes wingthing
"tailscale + tmux on the web" — nobody else has remote access + VTE + web terminal together.
See `docs/vt_design.md` for full design.

### Roost: Combined Relay + Wing Mode
Single command (`wt roost`) that runs relay and wing in one process. Eliminates the
two-process dance for self-hosted / on-prem deployments: one systemd unit, one log
stream, no `wt login` race condition, no `--local` flag confusion. `wt serve` stays
pure relay for cloud/multi-node. See `docs/roost_design.md` for full design.

### P2P: WebRTC Direct Connection for Same-LAN Wings
Bypass the relay entirely when browser and wing are on the same network.
WebRTC data channels for PTY I/O, encrypted tunnel stays E2E.
See `docs/webrtc-p2p-design.md` for full design.

---

## Backlog

### PTY: UTF-8 boundary safety in replay buffer trim and chunking

Replay buffer trimming and replay chunking are not UTF-8 aware. Multi-byte
sequences (emoji, box-drawing chars, CJK) that straddle a cut/chunk boundary
get split, producing permanent xterm rendering corruption (garbled status lines,
misplaced characters, mojibake).

**Where it happens:**

1. **`findSafeCut()` in `internal/egg/server.go` (~line 369)**
   Searches for safe trim points (sync frames, CRLF) but never checks if the
   chosen offset lands mid-UTF-8 sequence. The fallback returns `minOffset`
   raw, which can land anywhere. A 4-byte emoji split at byte 2 = broken
   decoder state in xterm.

2. **`sendReplayChunked()` in `cmd/wt/wing.go` (~line 138)**
   Splits replay data at fixed 128KB byte boundaries. Same problem — chunk
   boundary can bisect a multi-byte character. Each chunk is gzipped
   independently, so the halves never recombine. Only affects the web relay
   path (browser reattach), not local egg sessions.

3. **Browser gzip decompression in `web/src/pty.js` (~line 105)**
   If a corrupted chunk fails to decompress, the error handler silently drops
   it (`catch → null`). No logging, no user feedback — just a gap in the
   replay stream.

**Fix approach (when ready):**
- Add `isUTF8Boundary(buf, offset)` helper — check if byte at offset is a
  valid UTF-8 start byte (high bits 0xxxxxxx or 11xxxxxx, not 10xxxxxx)
- In `findSafeCut()`: after finding a cut point, walk backward (max 3 bytes)
  until on a UTF-8 boundary
- In `sendReplayChunked()`: same — adjust chunk end backward to nearest
  UTF-8 boundary before slicing
- Low risk per-fix, but touching the trim path is dangerous in aggregate —
  defer until we can test replay trim thoroughly

**Severity:** Cosmetic jank, not data loss. Observed as garbled Claude Code
status line after pasting unusual UTF-8 into an egg session. Self-heals on
full screen redraw but annoying.

---

- [ ] Image paste into terminal — intercept paste, upload via PTY, buffer output, loading bar
- [ ] Offline web app — PWA with cached wing data, works without network
- [ ] Facilitate worktrees — dev workflow for parallel feature branches
- [ ] GUI streaming — H.264 over WebSocket for graphical agent windows (Cursor, etc.)
- [ ] Wing-to-wing communication — wings coordinate via shared thread
- [ ] Context sync — teleport CLAUDE.md, memory files to wings on connect
- [ ] Cinch CI — GitHub release pipeline, badges
- [ ] Wing self-update — `wt update` pulls latest release by GOOS/GOARCH

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

### Go: Relay race conditions

- [ ] `bandwidth.go` month-boundary race — two goroutines calling `counter()`
  at month rollover both reset `b.counters`, second nuke first's data. Fix:
  double-check under lock after acquiring it
- [ ] `workers.go` `WingRegistry.UpdateConfig()` returns mutable `*ConnectedWing`
  pointer from inside the lock — callers can race on fields. Return a copy or
  use accessor methods
- [ ] PTY route orphan cleanup — sessions register on `pty.start`, unregister
  only on `pty.exited`. Crashed wings leave zombie entries forever. Add a sweep
  goroutine with TTL
- [ ] `ntfySentNonces` global `sync.Map` grows unbounded — entries never deleted.
  Add TTL or clear on session end

### JS: Split render.js (2,135 lines)

Same god-file problem on the frontend. Contains wing rendering, session tabs,
account management, org settings, audit display.

- [ ] Split into `renderWings.js`, `renderSessions.js`, `renderAccount.js`,
  `renderOrg.js`, `renderAudit.js`
- [ ] Fix event listener leaks in sidebar re-renders — `renderSidebar()` calls
  `addEventListener` on every tab without removing old listeners, so after N
  re-renders each tab has N click handlers. Use event delegation on the
  container instead
- [ ] Investigate `nav.js:49` session switching guard — `if (sess && !sess.swept) return`
  appears to bail when the session IS valid (preventing switch to active sessions).
  Either inverted logic or compensated elsewhere — needs investigation

### JS: Async correctness

- [ ] `data.js` `probeWing()` dedup — `_probeInflight` is deleted in `.finally()`
  before callers resolve, creating a race window for duplicate probes
- [ ] `bytesToB64()` in `helpers.js` uses O(n²) string concatenation in a loop
  on every encrypt/decrypt — use `String.fromCharCode.apply(null, bytes)` or
  typed array approach
- [ ] `terminal.js` `saveTermBuffer()` fires every 500ms serializing up to 200KB
  to localStorage with no quota checking. 100 sessions = 20MB. Add cleanup on
  session deletion and consider debouncing

### Tests

- [ ] Add tests for `internal/config/` (0% coverage) — config loading, wing ID
  generation, var resolution, missing config fallback
- [ ] Add tests for agent adapters — `claude.go` (145 lines, 0%), `codex.go`
  (119 lines, 0%), `cursor.go` (81 lines, 0%). At minimum test stream parsing
- [ ] Remove compile-time interface checks from runtime test functions
  (`var _ Agent = (*Gemini)(nil)` in gemini_test.go, ollama_test.go) — these
  don't execute at runtime

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

</details>
