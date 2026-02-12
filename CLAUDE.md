# Wingthing

## What This Is

`wt` runs AI agents sandboxed on your machine, accessible from anywhere. The primary use case is `wt egg <agent>` (sandboxed agent sessions) and `wt wing` (remote access via relay). Skills and task execution are secondary features.

- `wt egg claude` -- run Claude Code in a per-session sandbox with PTY persistence
- `wt wing -d` -- connect your machine to the relay, access from app.wingthing.ai
- `wt serve` -- relay server (web UI, WebSocket relay, skill registry), HTTP + SQLite

## Design Philosophy

**Curated > marketplace.** Skills live in `skills/` in this repo. They're reviewed, validated, and version-controlled. No storefront where anyone can publish prompt injections. Private skills go in `~/.wingthing/skills/`.

**Sandbox-first.** `internal/sandbox/` has Seatbelt (macOS) and user namespace/seccomp (Linux). The sandbox IS the permission boundary for egg sessions — agents get `--dangerously-skip-permissions` because the sandbox constrains them.

**Agent-agnostic.** Every skill works with every backend. `--agent ollama` for free local inference, `--agent claude` when you need it. The interface is stable; providers change behind it.

**Local-first.** Your machine, your keys, your data. No cloud dependency. Offline with ollama.

## Dogfooding

**Always use wingthing's own tools and infrastructure.** If wingthing can do something, use wingthing to do it. Don't shell out to external scripts or paid APIs when the equivalent exists (or should exist) in the codebase.

If you find yourself reaching for an external tool and wingthing _should_ handle it, that's a gap to fill in wingthing itself.

## Architecture

- `wt egg <agent>` -- spawns a per-session child process (`wt egg run`) with its own sandbox, PTY, and gRPC socket at `~/.wingthing/eggs/<session-id>/`
- `wt wing` -- WebSocket client that connects outbound to the relay, accepts remote PTY/task/chat requests, spawns eggs for each session
- `wt serve` -- relay server (web UI + WebSocket relay + skill registry), HTTP + SQLite
- `wt run` -- direct agent invocation for prompts and skills (the old `wt [prompt]`)
- Agents are pluggable (claude, ollama, gemini, codex, cursor). `wt` calls them as child processes.
- All commands use direct store access via `store.Open(cfg.DBPath())`.

## Provider System

### Agents (brains)
CLI tools detected by `wt doctor`:
- `claude` CLI -- Anthropic Claude
- `ollama` CLI -- local models (llama3.2 default)
- `gemini` CLI -- Google Gemini

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
| Linux | User namespaces + seccomp | CLONE_NEWUSER/NEWNS/NEWPID/NEWNET, BPF syscall filter |

No fallback — if the platform can't enforce the requested isolation, the egg fails with `EnforcementError`.

Isolation levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (no sandbox).

Configure via `egg.yaml` (project-level, `~/.wingthing/egg.yaml`, or built-in defaults). The sandbox auto-injects mounts for the agent binary's install root and config dir (`~/.<agent>/`) so config authors don't need to know where agents are installed. Resource limits (CPU, memory, max FDs) only apply when explicitly configured — no defaults.

### Agent network auto-drilling

When `isolation` is `strict` or `standard` (no network), the sandbox automatically punches holes for the agent to function. Each agent has a profile declaring its network needs:

| Agent | Network | What it opens |
|-------|---------|---------------|
| claude | HTTPS | **All outbound TCP 443/80 + DNS.** Required for api.anthropic.com. macOS seatbelt cannot filter by hostname or IP — only by port. |
| codex | HTTPS | Same as claude (for api.openai.com) |
| gemini | HTTPS | Same as claude (for googleapis.com) |
| cursor | HTTPS | Same as claude |
| ollama | Local | Localhost only (127.0.0.1, no external) |

**Important:** `standard` isolation with a cloud agent (claude, codex, gemini, cursor) allows outbound HTTPS to **any host**, not just the agent's API. This is a platform limitation — macOS seatbelt cannot filter by domain or IP range. On Linux, the agent currently gets full network access (no port filtering in unprivileged namespaces). See `docs/egg-sandbox-design.md` for details and the roadmap for SNI-based domain filtering.

## Key Packages

| Package | Role |
|---------|------|
| `internal/egg` | Per-session egg server (gRPC, PTY, sandbox lifecycle), client, config |
| `internal/egg/pb` | Protobuf-generated gRPC types (Kill, Resize, Session) |
| `internal/sandbox` | Seatbelt (macOS) and namespace (Linux) sandbox implementations |
| `internal/ws` | WebSocket protocol (wing<->relay messages), client with auto-reconnect |
| `internal/auth` | ECDH key exchange, AES-GCM E2E encryption, device auth, token store |
| `internal/agent` | LLM agent adapters (claude, ollama, gemini, codex, cursor) |
| `internal/relay` | Relay server: web UI, WebSocket handler, wing registry, skill registry |
| `internal/orchestrator` | Prompt assembly, config resolution, budget management |
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, cosine/blend utilities |
| `internal/skill` | Skill loading, template interpolation |
| `internal/memory` | Memory loading, layered retrieval |
| `internal/config` | Config loading, `~/.wingthing/` paths, defaults |
| `internal/store` | SQLite store -- tasks, thread, agents, logs, chat sessions |

## Build

**Always use `make`, never bare `go build` / `go test`.**

| Command | What it does |
|---------|-------------|
| `make check` | Run tests then build (the default verification step) |
| `make build` | Build the `wt` binary |
| `make test` | Run `go test ./...` |
| `make web` | Build vite output (`cd web && npm run build`) |
| `make serve` | Build then run `wt serve` in foreground |
| `make clean` | Remove built binary |

Run `make check` to verify changes. Run `make web` before `make check` if you changed anything in `web/`.

### CI

CI runs via **cinch**. Use `cinch run` to trigger a build, `cinch status` to check results. When Bryan says "cinch run" or "cinch status", run those commands directly.

### Development is LOCAL

**Prod (wingthing.fly.dev) is Bryan's daily driver.** Do not deploy to Fly during development unless explicitly asked. All development and testing happens locally.

- `make serve` starts a local relay on `:8080`
- `wt wing --relay http://localhost:8080` connects a wing to the local relay
- Test the full stack locally before even thinking about prod
- End-of-day or explicit request = deploy to Fly

### Running `wt serve` during development

Use `make serve` in a separate terminal (or via Bash `run_in_background`). It builds and runs in foreground — ctrl-C to stop, rerun after code changes. For production self-hosted: launchd (macOS) or systemd user unit (Linux).

**NEVER use `&` in Bash commands.** Use the Bash tool's `run_in_background` parameter instead. Appending `&` causes the process to die immediately and produces garbage output. If you need a background process: `run_in_background: true` on the Bash tool call, then check output via `Read` on the output file or `TaskOutput`.

## CLI Commands

| Command | What it does |
|---------|-------------|
| `wt egg <agent>` | Run agent in sandboxed session (claude, codex, ollama) |
| `wt egg list` | List active egg sessions |
| `wt egg stop <id>` | Stop an egg session |
| `wt wing` | Connect to relay, accept remote tasks |
| `wt wing -d` | Start wing as background daemon |
| `wt wing stop` | Stop wing daemon |
| `wt wing status` | Check wing daemon and active sessions |
| `wt start` / `wt stop` | Aliases for `wt wing -d` / `wt wing stop` |
| `wt login` / `wt logout` | Device auth with relay server |
| `wt run [prompt]` | Run a prompt or skill directly |
| `wt run --skill [name]` | Run a named skill |
| `wt doctor` | Scan for available agents, API keys, services |
| `wt serve` | Start the relay web server |
| `wt skill list/add/enable/disable` | Manage skills (local + registry) |
| `wt update` | Update wt to latest release |
