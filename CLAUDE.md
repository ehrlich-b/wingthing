# Wingthing

## What This Is

`wt` runs AI agents sandboxed on your machine, accessible from anywhere. The primary use case is `wt egg <agent>` (sandboxed agent sessions) and `wt wing` (remote access via relay). Skills, social feed, and task execution are secondary features.

- `wt egg claude` -- run Claude Code in a per-session sandbox with PTY persistence
- `wt wing -d` -- connect your machine to the relay, access from app.wingthing.ai
- `wt serve` -- relay server (social feed, web UI, WebSocket relay), HTTP + SQLite

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
- `wt serve` -- relay server (social feed + web UI + WebSocket relay), HTTP + SQLite
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

## Spaces

- `spaces.yaml` is the single source of truth for space definitions (159 entries)
- `internal/embedding.SpaceIndex` loads YAML, embeds centroids, caches per-embedder as `.bin` files
- Multi-embedder: embed centroids with every provided Embedder, anyone can bring their own

## Agent Resolution Precedence

Single resolution path for all contexts: **CLI flag (`--agent`) > skill frontmatter (`agent:`) > config default (`default_agent`)**

This means `wt --skill compress --agent ollama` always runs ollama, regardless of what the skill declares.

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
| `memory` | no | List of memory files to load (e.g. `[feeds, identity]`) |
| `agent` | no | Default agent; overridable with `--agent` |
| `isolation` | no | Sandbox isolation level (`strict`, `standard`, `network`, `privileged`) |
| `timeout` | no | Duration string (e.g. `60s`) |
| `tags` | no | Categorization tags |
| `schedule` | no | Cron expression for recurring execution |
| `mounts` | no | Directories to mount into sandbox |

Install with `wt skill add skills/compress.md`. Memory files referenced by skills go in `~/.wingthing/memory/`.

## Sandbox

Implementations in `internal/sandbox/`:

| Platform | Implementation | How |
|----------|---------------|-----|
| macOS | Seatbelt | `sandbox-exec` with generated SBPL profile |
| Linux | User namespaces + seccomp | CLONE_NEWUSER/NEWNS/NEWPID/NEWNET, BPF syscall filter |

No fallback — if the platform can't enforce the requested isolation, the egg fails with `EnforcementError`.

Isolation levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (no sandbox).

Configure via `egg.yaml` (project-level, `~/.wingthing/egg.yaml`, or built-in defaults). The sandbox auto-injects mounts for the agent binary's install root and config dir (`~/.<agent>/`) so config authors don't need to know where agents are installed. Resource limits (CPU, memory, max FDs) only apply when explicitly configured — no defaults.

## Key Packages

| Package | Role |
|---------|------|
| `internal/egg` | Per-session egg server (gRPC, PTY, sandbox lifecycle), client, config |
| `internal/egg/pb` | Protobuf-generated gRPC types (Kill, Resize, Session) |
| `internal/sandbox` | Seatbelt (macOS) and namespace (Linux) sandbox implementations |
| `internal/ws` | WebSocket protocol (wing<->relay messages), client with auto-reconnect |
| `internal/auth` | ECDH key exchange, AES-GCM E2E encryption, device auth, token store |
| `internal/agent` | LLM agent adapters (claude, ollama, gemini, codex, cursor) |
| `internal/relay` | Relay server: social feed, web UI, WebSocket handler, wing registry |
| `internal/orchestrator` | Prompt assembly, config resolution, budget management |
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, SpaceIndex, cosine/blend |
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
| `wt post "text" --link URL --mass N --date DATE` | Post to wt social |
| `wt vote <post-id>` | Upvote a post on wt social |
| `wt comment <post-id> "text"` | Comment on a post |

## Social Pipeline

The self-hosted content pipeline for wt.ai/social:

```
feeds.md (509 feeds) → pipeline.go → articles.tsv → compress_and_post.sh → social.db
```

**Feed target: 1000+ validated working feeds.** Validate with `/tmp/validate_feeds.go` — prune dead/empty feeds, replace with working ones.

1. **Fetch**: `go run /tmp/pipeline.go skills/feeds.md > /tmp/articles.tsv` — reads feeds.md, fetches RSS/Atom in parallel (10 at a time), outputs TSV with SOURCE, TITLE, LINK, DATE, TEXT
2. **Compress + Post**: `/tmp/compress_and_post.sh ./wt` — reads articles.tsv, compresses each via `claude -p sonnet` (free on $200 plan), posts via `wt post --date --link --mass`
3. **Result**: everything lands in `~/.wingthing/social.db` with real embeddings (ollama mxbai-embed-large), space assignments (cosine similarity), and article publish dates (for proper decay)

**Key details:**
- Embeddings: ollama mxbai-embed-large (free, local)
- Summarization: `claude -p --model claude-sonnet-4-5-20250929` (effectively free on $200 plan)
- `--mass 10` for bot-curated content; `POST /api/post` forces mass=1 (public)
- `--date` accepts RFC3339 or YYYY-MM-DD; decay uses `COALESCE(published_at, created_at)`
- URL dedup: same link returns existing post
- Back up DB: `cp ~/.wingthing/social.db ~/wt_bak/social_$(date +%Y%m%d).db`
- Pipeline scripts live in `/tmp/` (not checked in — generated content)
