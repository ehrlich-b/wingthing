# Wingthing

## What This Is

`wt` runs AI agents sandboxed on your machine, accessible from anywhere. The primary use case is `wt egg <agent>` (sandboxed agent sessions) and `wt wing` (remote access via relay). Skills are a secondary feature.

- `wt egg claude` -- run Claude Code in a per-session sandbox with PTY persistence
- `wt start` -- connect your machine to the relay, access from app.wingthing.ai
- `wt serve` -- relay server (web UI, WebSocket relay, skill registry), HTTP + SQLite

## Current Push: AI-Usable API

**An AI must be able to orchestrate wingthing as easily as a human can.** This is
the primary focus of the current work. Anything a human can do from the terminal
or the browser, a model must be able to do through a typed interface with the
same authority model and the same audit trail.

The rule: **if you ship a capability only a human can drive, it is unfinished.**

- `wt mcp stdio` is the model-facing surface. Tools have closed JSON Schemas,
  return structured content, and declare read-only/mutating/destructive intent.
- CLI subcommands take `--json` for scripting. Human-readable output is a
  rendering of structured data, never the only representation.
- **Never treat a UI as an API.** No scraping the web UI, and no parsing a TUI's
  screen when the underlying runtime can report the fact directly.
- Terminal snapshots (`wt session read`) are raw ANSI state, not a conversation
  transcript. Do not let callers pretend otherwise — agent-aware state belongs
  in typed events above the PTY, sourced from agent hooks and protocols first
  and screen heuristics only as a declared fallback.
- Ambient authority is not accessibility. Every new model-reachable action needs
  a principal, a grant, a bound (time/iterations/concurrency), and a log line.

When adding a capability, the checklist is: runtime primitive → CLI verb with
`--json` → MCP tool with a schema → tests at two tiers → doc line. See
`docs/agent-meta-layer.md` for the object model.

## Design Philosophy

**Curated > marketplace.** Skills live in `skills/` in this repo. They're reviewed, validated, and version-controlled. No storefront where anyone can publish prompt injections. Private skills go in `~/.wingthing/skills/`.

**Sandbox-first.** `internal/sandbox/` has Seatbelt (macOS) and user namespace/seccomp (Linux). The sandbox IS the permission boundary for egg sessions — agents get `--dangerously-skip-permissions` because the sandbox constrains them.

**Agent-agnostic.** Every skill works with every backend. `--agent ollama` for free local inference, `--agent claude` when you need it. The interface is stable; providers change behind it.

**Local-first.** Your machine, your keys, your data. No cloud dependency. Offline with ollama.

## Dogfooding

**Always use Wingthing's own tools and infrastructure.** Agent, terminal,
sandbox, and orchestration work on this repository should run through Wingthing
whenever it can perform the job.

Treat Wingthing friction as product work. When a real dogfood task is awkward or
fails:

1. reproduce the smallest underlying product gap;
2. fix the runtime or typed contract, with a regression test;
3. rebuild Wingthing and retry the original task through Wingthing; and
4. record any remaining limitation in the relevant design document.

Recursive fixes count toward the task. Fix the first genuine blocker before
completing the parent task via terminal scraping, ad hoc scripts, or a second
orchestration system. Leave working paths alone unless a real task exposes a
problem.

The destination is the shared roost: any useful local operation added while
dogfooding must be designed as a reusable runtime primitive that can also be
exposed through an authenticated, owner-scoped, typed, audited roost adapter.
A local-only convenience is incomplete unless it is a deliberate intermediate
step toward that parity.

The only current real user workflow is Slide's shared roost in the web UI.
Preserve it by default while iterating on other workflows. Breaking changes are
allowed when they materially simplify or improve the product, but first present
Bryan with the concrete benefit, affected workflow, and migration plan and get
his agreement. Compatibility remains a conscious tradeoff.

## Architecture

- `wt egg <agent>` -- spawns a per-session child process (`wt egg run`) with its own sandbox, PTY, and gRPC socket at `~/.wingthing/eggs/<session-id>/`
- `wt attach [session-id]` -- list or reattach to local eggs; `--remote <ssh-host>` runs the same attach path over ordinary SSH
- `wt wing` -- WebSocket client that connects outbound to the relay, handles PTY sessions and encrypted tunnel requests, spawns eggs for each session
- `wt serve` -- relay server (web UI + WebSocket relay + skill registry), HTTP + SQLite. For encrypted terminal/tunnel payloads the relay is a router, but it still owns account/routing metadata and serves the browser code. See `docs/security.md` before making security claims.
- The relay stores wing IDs/public keys, ownership/org binding, lock state, and connection metadata. Rich wing metadata (hostname, platform, agents, projects, labels) comes from the wing via encrypted tunnel requests (`wing.info`). The frontend caches this metadata in localStorage and shows cached data on page load while probing wings in the background.
- `wt run` -- direct agent invocation for prompts and skills (the old `wt [prompt]`)
- `wt roost` -- combined relay + wing in one process for self-hosted deployments
- Agents are pluggable (claude, ollama, gemini, codex, cursor, opencode). `wt` calls them as child processes.
- All commands use direct store access via `store.Open(cfg.DBPath())`.

### Encrypted Tunnel Protocol

Wing API payloads (directory listings, session history, audit recordings, egg configs, and tunnel passkey assertions) flow through an application-encrypted tunnel. The shipped relay does not receive their plaintext during normal operation. This is not a malicious-web-service guarantee: the relay serves the browser JavaScript, initial wing-key trust is TOFU, and routing metadata stays visible.

| Message | Direction | Description |
|---------|-----------|-------------|
| `tunnel.req` | browser -> relay -> wing | Encrypted request: `{type, wing_id, request_id, sender_pub, payload}` |
| `tunnel.res` | wing -> relay -> browser | Encrypted response: `{type, request_id, payload}` |
| `tunnel.stream` | wing -> relay -> browser | Encrypted streaming: `{type, request_id, payload, done}` |

Inner message types (inside encrypted payload): `dir.list`, `wing.info`, `webrtc.offer`, `sessions.list`, `sessions.history`, `audit.request`, `egg.config_update`, `pty.kill`, `wing.update`, `passkey.auth`, `allow.list`, `allow.add`, `allow.remove`, `paths.list`, `paths.set`, `paths.add_member`, `paths.remove_member`

### Two Key Types, Two HKDF Domains

| Key | Lifecycle | HKDF info | Purpose |
|-----|-----------|-----------|---------|
| PTY session key | Per-session ephemeral X25519 | `"wt-pty"` | Terminal I/O encryption |
| Tunnel key | Persistent identity X25519 | `"wt-tunnel"` | All non-PTY wing data |

Browser identity key is stored in sessionStorage (ephemeral per tab). Because the wing key is persistent, this alone does **not** provide forward secrecy against later wing-key compromise. Passkey auth tokens are shared between PTY and tunnel but bound to relay user ID plus the client's X25519 key, with configurable TTL via `auth_ttl` in wing.yaml. Wing restart revokes all tokens (in-memory cache).

### Wing ID Scheme (IMPORTANT — two different IDs)

Wings have TWO identifiers. Confusing them breaks session routing.

| ID | Field | Format | Lifecycle | Example |
|----|-------|--------|-----------|---------|
| **Machine ID** (`wing_id` / `WingID`) | `ConnectedWing.WingID`, API `wing_id` | 24-char hex (MongoDB-style) | Persistent, stored in `~/.wingthing/wing.yaml` | `1ae20a6b28854276b1514d14` |
| **Connection ID** (`id` / `ID`) | `ConnectedWing.ID`, registry map key | UUID prefix or random | Ephemeral, assigned on WebSocket connect | `a1b2c3d4` |

**The API (`/api/app/wings`) returns `wing_id` (machine ID).** The frontend uses `wing_id` everywhere. The `wings` map in `WingRegistry` is keyed by connection ID (`ConnectedWing.ID`).

**Lookup patterns:**
- `WingRegistry.FindByID(id)` — looks up by **connection ID** (map key)
- `findAnyWingByWingID(wingID)` — linear scan of all wings matching **machine ID** (`ConnectedWing.WingID`)
- `PeerDirectory.FindWing(id)` — looks up peer by **connection ID**
- `PeerDirectory.FindByWingID(wingID)` — looks up peer by **machine ID**

**Fly-replay routing:** The PTY WebSocket handler receives `?wing_id=<machine_id>` from the browser. Before upgrading the WebSocket, it checks local wings and peers, issuing `fly-replay` to route to the correct Fly machine. After upgrade, `pty.start` messages also contain `wing_id` (machine ID) — the handler must look up by machine ID (via `findAnyWingByWingID`), not just connection ID.

**Rule: when the frontend sends a wing identifier, it's always the machine ID (`wing_id`). Always use `findAnyWingByWingID()` (or try both lookups) to resolve it to a `ConnectedWing`.**

### Organizations and Multi-User

Wings can be shared via organizations. The relay has a full org system:

- **Schema:** `orgs`, `org_members`, `org_invites`, `subscriptions`, `entitlements` tables
- **API:** `POST/GET/DELETE /api/orgs`, `/api/orgs/{orgID}/members`, `/api/orgs/{orgID}/invite`, etc.
- **Roles:** owner (full control), admin (invite/remove), member (access wings)
- **Wing binding:** `wing.yaml` has `org:` field; `wt start --org <slug>` links a wing to an org
- **Access control:** `canAccessWing()` in `internal/relay/access.go` checks owner, org membership, or roost mode
- **Path ACLs:** `wing.yaml` can restrict per-folder access to specific org members by email
- **Key files:** `internal/relay/org.go` (854 lines), `internal/relay/access.go`, `internal/relay/store.go`

### Roost Mode

`wt roost` runs relay + wing in one process for self-hosted deployments. See `docs/roost_design.md`.

- Two auth modes: local (no OAuth, single user auto-created) and roost (with OAuth, all authenticated users access wings)
- Daemon mode with `~/.wingthing/roost.pid` and `~/.wingthing/roost.log`
- `wt roost start/stop/status` subcommands
- **Key file:** `cmd/wt/roost.go`

## Provider System

### Agents (brains)
CLI tools detected by `wt doctor`:
- `claude` CLI -- Anthropic Claude
- `ollama` CLI -- local models (`qwen3:4b` default; native structured tools)
- `gemini` CLI -- Google Gemini
- `codex` CLI -- OpenAI Codex
- `cursor` CLI (`agent` subcommand) -- Cursor
- `opencode` CLI -- OpenCode

### Embedders
- **ollama** -- local, default model `mxbai-embed-large`, 512 dims
- **openai** -- `text-embedding-3-small`, 512 dims, needs `OPENAI_API_KEY`

### Auto-detection (`default_embedder: auto`)
1. Ping ollama at localhost:11434 -- if up, use it
2. Fall back to openai if `OPENAI_API_KEY` is set
3. Error with clear message if neither available

### Well-known env vars
- `OPENAI_API_KEY` -- OpenAI embeddings + agents
- `ANTHROPIC_API_KEY` -- Anthropic/Claude API
- `GEMINI_API_KEY` / `GOOGLE_API_KEY` -- Google/Gemini

## Agent Resolution Precedence

Single resolution path for all contexts: **CLI flag (`--agent`) > skill frontmatter (`agent:`) > config default (`default_agent`)**

## Skill System

Skills are the core abstraction. Markdown files with YAML frontmatter and a prompt template body.

### Philosophy
- **Repo skills** (`skills/`) are the validated library -- curated, tested, checked in
- **User skills** (`~/.wingthing/skills/`) are private -- your own workflows, not shared
- Skills are enableable/disableable (planned: `wt skill enable/disable`)
- No agent lock-in: omit `agent:` from frontmatter and the user's default applies
- Skills declare their memory deps, isolation level, and schedule -- the orchestrator handles the rest

### Frontmatter fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Skill identifier (matches filename) |
| `description` | yes | One-line summary |
| `memory` | no | List of memory files to load (e.g. `[identity]`) |
| `agent` | no | Default agent; overridable with `--agent` |
| `isolation` | no | Sandbox isolation level (`strict`, `standard`, `network`, `privileged`) |
| `timeout` | no | Duration string (e.g. `60s`) |
| `tags` | no | Categorization tags |
| `schedule` | no | Cron expression for recurring execution |
| `mounts` | no | Directories to mount into sandbox |

Install with `wt skill add skills/dream.md`. Memory files referenced by skills go in `~/.wingthing/memory/`.

## Sandbox

Implementations in `internal/sandbox/`:

| Platform | Implementation | How |
|----------|---------------|-----|
| macOS | Seatbelt | `sandbox-exec` with generated SBPL profile |
| Linux | User namespaces + seccomp + cgroups v2 | CLONE_NEWUSER/NEWNS/NEWPID/NEWNET, BPF syscall filter (27+ denied), cgroups v2 memory/PIDs + prlimit |

No fallback — if the platform can't enforce the requested isolation, the egg fails with `EnforcementError`.

Isolation levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (no sandbox).

Configure via `egg.yaml` (project-level, `~/.wingthing/egg.yaml`, or built-in defaults). The sandbox auto-injects mounts for the agent binary's install root and config dir (`~/.<agent>/`) so config authors don't need to know where agents are installed. Resource limits (CPU, memory, max FDs, max PIDs) only apply when explicitly configured. No defaults. On Linux, cgroups v2 provides real memory (RSS) and PID tree limits; prlimit covers CPU time, virtual address space, and FDs.

### Agent network auto-drilling

When `isolation` is `strict` or `standard` (no network), the sandbox automatically punches holes for the agent to function. Each agent has a profile declaring its network needs:

| Agent | Network | What it opens |
|-------|---------|---------------|
| claude | HTTPS | **All outbound TCP 443/80 + DNS.** Required for api.anthropic.com. macOS seatbelt cannot filter by hostname or IP — only by port. |
| codex | HTTPS | Same as claude (for api.openai.com) |
| gemini | HTTPS | Same as claude (for googleapis.com) |
| cursor | HTTPS | Same as claude |
| ollama | Local | Localhost only (127.0.0.1, no external) |
| opencode | HTTPS | Same as claude (for anthropic, openai, googleapis) |

**Important:** `standard` isolation with a cloud agent (claude, codex, gemini, cursor) allows outbound HTTPS to **any host**, not just the agent's API. This is a platform limitation — macOS seatbelt cannot filter by domain or IP range. On Linux, the agent currently gets full network access (no port filtering in unprivileged namespaces). See `docs/egg-sandbox-design.md` for details and the roadmap for SNI-based domain filtering.

## Key Packages

| Package | Role |
|---------|------|
| `internal/egg` | Per-session egg server (gRPC, PTY, sandbox lifecycle), client, config |
| `internal/egg/pb` | Protobuf-generated gRPC types (Kill, Resize, Session) |
| `internal/sandbox` | Seatbelt (macOS) and namespace + seccomp + cgroups v2 (Linux) sandbox implementations |
| `internal/ws` | WebSocket protocol (wing<->relay messages, tunnel.req/res/stream types), client with auto-reconnect |
| `internal/auth` | ECDH key exchange, AES-GCM E2E encryption (PTY + tunnel HKDF domains), device auth, passkey verification, token store |
| `internal/agent` | LLM agent adapters (claude, ollama, gemini, codex, cursor, opencode) |
| `internal/relay` | Relay server: web UI, WebSocket handler, wing registry, skill registry. Dumb forwarder for encrypted wing data (tunnel, PTY). |
| `internal/orchestrator` | Prompt assembly, config resolution, budget management |
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, cosine/blend utilities |
| `internal/skill` | Skill loading, template interpolation |
| `internal/memory` | Memory loading, layered retrieval |
| `internal/config` | Config loading, `~/.wingthing/` paths, defaults |
| `internal/store` | SQLite store -- tasks, thread, agents, logs |
| `internal/webrtc` | P2P WebRTC: PeerManager, SwappableWriter, SDP helpers |
| `internal/cron` | Cron scheduler for recurring tasks |
| `internal/thread` | Daily thread assembly and persistence |
| `internal/direct` | Direct agent invocation (non-relay mode) |
| `internal/sync` | Sync primitives and helpers |
| `internal/parse` | Parsing utilities (structured output markers, etc.) |
| `internal/ntfy` | Push notifications via ntfy.sh |

## Build

**Always use `make`, never bare `go build` / `go test`.**

| Command | What it does |
|---------|-------------|
| `make check` | Run tests then build (the default verification step) |
| `make build` | Build the `wt` binary |
| `make test` | Unit tier — `go test ./...` |
| `make test-integ` | Integration tier — relay/wing/PTY protocol with simulated endpoints |
| `make test-linux` / `test-linux-ubuntu` | E2E tier — privileged Linux sandbox battery in Docker |
| `make test-provider-swap` | Opt-in real-harness/Ollama/LiteLLM release smoke matrix |
| `make coverage` | Statement coverage report |
| `make web` | Build vite output (`cd web && npm run build`) |
| `make serve` | Build then run `wt serve` in foreground |
| `make clean` | Remove built binary |

Run `make check` to verify changes. Run `make web` before `make check` if you changed anything in `web/`.

`make build` and the Go test targets seed a placeholder `web/dist` when it is
missing, because `web/embed.go` embeds it and the built assets are gitignored.
`make web` overwrites the placeholder with the real vite output.

### Testing bar

**Target: roughly half of the Go in this repo is test code.** As of 2026-08-09 it
is 33% (17.7k test lines against 35.7k non-test). New work should close that gap,
not widen it.

This is a floor on how much testing effort a change deserves — **not** a license
for coverage theater. A test that asserts a getter returns what the setter set is
worth nothing and still inflates the ratio. Prefer tests that pin a real
contract: an exact argv, a wire message, a sandbox denial, a reconnect.

Three tiers, and every new capability lands with tests in **at least two**:

| Tier | Command | Proves |
|------|---------|--------|
| Unit | `make test` | Exact contracts — argv, parsing, schemas, validation, config resolution |
| Integration | `make test-integ` | Component protocol against simulated endpoints — no real agent, no network |
| E2E | `make test-linux-ubuntu` | Real enforcement and real lifecycle on a real kernel |

Rules:

- **A mocked test proves our routing, not that a vendor kept its flags.** Agent
  invocations need an exact-argv unit test so an upstream change is a test
  failure, not a support ticket.
- **Sandbox claims need E2E.** If a doc says a path is denied or a syscall is
  blocked, a test must run in the sandbox and observe the denial. An enforcement
  feature that is available but broken is a failure, never a skip.
- **Skips must be capability-driven and explicit.** Missing kernel feature or
  uninstalled real CLI may skip. Nothing else may.
- **Both halves of the AI surface get tested.** A new MCP tool needs schema and
  strict-argument-decoding tests plus one test that actually drives it.
- Reach for a real e2e test before a clever unit test when the risk is lifecycle
  (detach, reattach, restart, crash) rather than logic.

### CI

CI runs via **GitHub Actions** (`.github/workflows/ci.yml` on push/PR; `release.yml` on `v*` tags). Use `gh run list`, `gh run watch`, or `gh run view --log-failed` to check builds.

### Development is LOCAL

**Prod (wingthing.fly.dev) is Bryan's daily driver.** Do not deploy to Fly during development unless explicitly asked. All development and testing happens locally.

### Vacation freeze: local-first branch only

Through approximately 2026-08-20, treat `main` as frozen. Major runtime work
belongs on `feature-local-first-terminal-routing`. Do not merge it to `main`,
tag a version, create a release, deploy Fly, or change the Slide deployment.
Build the repository binary and use an isolated `WINGTHING_DIR` for local
dogfooding. See `docs/vacation-local-first.md` for the branch contract and
post-vacation promotion gate.

- `make serve` starts a local relay on `:8080`
- `wt wing --relay http://localhost:8080` connects a wing to the local relay
- Test the full stack locally before even thinking about prod
- End-of-day or explicit request = deploy to Fly
- **Never tail `fly deploy` output.** Run it with full output visible (no `| tail`). Truncating deploy output hides errors and makes it impossible to tell if prod is broken.

### Running `wt serve` during development

Use `make serve` in a separate terminal (or via Bash `run_in_background`). It builds and runs in foreground — ctrl-C to stop, rerun after code changes. For production self-hosted: launchd (macOS) or systemd user unit (Linux).

**NEVER use `&` in Bash commands.** Use the Bash tool's `run_in_background` parameter instead. Appending `&` causes the process to die immediately and produces garbage output. If you need a background process: `run_in_background: true` on the Bash tool call, then check output via `Read` on the output file or `TaskOutput`.

### Testing wing changes

After `make check`, restart the wing daemon with the local build: `./wt stop && ./wt start --debug`. This uses `os.Executable()` throughout, so the daemon and all egg child processes use the same local binary — no need to install to PATH first.

## CLI Commands

| Command | What it does |
|---------|-------------|
| `wt egg <agent> [-- args]` | Run agent in sandboxed session; args after `--` pass through to the agent CLI (`wt egg codex -- -m gpt-5.6-terra`) |
| `wt egg list` | List active egg sessions |
| `wt egg stop <id>` | Stop an egg session |
| `wt egg explain [agent]` | Show the effective sandbox policy and every auto-drilled agent hole (`--json`) |
| `wt attach [id]` | List or attach to a live local session; add `--remote <ssh-host>` for SSH-native attach |
| `wt wing` | Connect to relay, serve encrypted tunnel + PTY sessions |
| `wt wing start` | Start wing as background daemon |
| `wt wing stop` | Stop wing daemon |
| `wt wing status` | Check wing daemon and active sessions |
| `wt wing lock` / `unlock` | Require passkey auth for sessions |
| `wt wing allow` | Manage allowed public keys |
| `wt wing config` | View/set wing config |
| `wt start` / `wt stop` | Aliases for `wt wing start` / `wt wing stop` |
| `wt roost` / `roost start` | Combined relay + wing in one process (self-hosted) |
| `wt roost stop` / `status` | Stop or check roost daemon |
| `wt serve` | Start the relay web server (cloud/multi-node) |
| `wt login` / `wt logout` | Device auth with relay server |
| `wt whoami` | Show logged-in user |
| `wt run [prompt]` | Run a prompt or skill directly |
| `wt run --skill [name]` | Run a named skill |
| `wt doctor` | Scan for available agents, API keys, services |
| `wt timeline` | List upcoming/recent tasks |
| `wt thread` | Show daily thread |
| `wt status` | Task counts and token usage |
| `wt log [taskId]` | Show task log events |
| `wt retry [task-id]` | Retry a failed task |
| `wt agent list` | List agent adapters |
| `wt schedule list/remove` | Manage recurring tasks |
| `wt init` | Initialize ~/.wingthing |
| `wt support` | Collect diagnostic bundle |
| `wt embed [text]` | Generate embeddings |
| `wt keygen` | Generate key material |
| `wt update` | Update wt to latest release |
