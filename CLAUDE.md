# Wingthing

## What This Is

`wt` is a universal interface to AI agents. One binary, one skill format, any backend. The skill library is the product -- a curated, growing collection of validated skills checked into this repo. Users enable what they want, add their own, swap agents with a flag. When a new AI tool drops, we ship a skill. Users learn `wt` once.

This is the well-built version of what OpenClaw, Moltbook, and the rest are stumbling toward: agent tooling that's curated instead of a malware marketplace, sandboxed instead of "Docker is optional", and affordable instead of $300/day.

`wt serve` runs the relay server -- social feed, web UI, HTTP + SQLite.

## Design Philosophy

**Curated > marketplace.** Skills live in `skills/` in this repo. They're reviewed, validated, and version-controlled. No storefront where anyone can publish prompt injections. Private skills go in `~/.wingthing/skills/`.

**Sandbox-first.** `internal/sandbox/` has full implementations for Apple Containers (macOS) and namespace/seccomp (Linux). Isolation level is per-skill via frontmatter. **Current gap:** sandbox is built and tested but not wired into `runTask()` -- this is a high-priority TODO.

**Agent-agnostic.** Every skill works with every backend. `--agent ollama` for free local inference, `--agent claude` when you need it. The interface is stable; providers change behind it.

**Local-first.** Your machine, your keys, your data. No cloud dependency. Offline with ollama.

## Dogfooding

**Always use wingthing's own tools and infrastructure.** If wingthing can do something, use wingthing to do it. Don't shell out to external scripts or paid APIs when the equivalent exists (or should exist) in the codebase.

If you find yourself reaching for an external tool and wingthing _should_ handle it, that's a gap to fill in wingthing itself.

## Architecture

- `wt` -- single binary. SQLite + direct agent invocation + OS-scheduled tasks (cron, launchd, systemd timers). No daemon, no socket.
- `wt serve` -- relay server (social feed + web UI), HTTP + SQLite
- Agents are pluggable (claude, ollama, gemini). `wt` calls them as child processes.
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

Full implementations exist in `internal/sandbox/`:

| Platform | Implementation | How |
|----------|---------------|-----|
| macOS 26+ | Apple Containers | `container` CLI, per-task Linux VMs |
| Linux | Namespaces + seccomp | CLONE_NEWNS/PID/NET, syscall filter, landlock |
| Fallback | Process isolation | Restricted env, isolated tmpdir |

Isolation levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (full access).

**Status:** Built, tested, and wired into `runTask()`. Agents receive a `CmdFactory` via `RunOpts` that routes execution through the sandbox. Privileged isolation skips sandbox. Skill mounts and timeout flow through `PromptResult`.

## Key Packages

| Package | Role |
|---------|------|
| `internal/agent` | LLM agent adapters (claude, ollama, gemini) |
| `internal/orchestrator` | Prompt assembly, config resolution, budget management |
| `internal/sandbox` | Container/namespace isolation per task |
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, SpaceIndex, cosine/blend |
| `internal/relay` | RelayStore, social feed, space seeding, skills registry |
| `internal/skill` | Skill loading, template interpolation |
| `internal/memory` | Memory loading, layered retrieval |
| `internal/config` | Config loading, `~/.wingthing/` paths, defaults |
| `internal/store` | SQLite store -- tasks, thread, agents, logs |

## Build

**Always use `make`, never bare `go build` / `go test`.**

| Command | What it does |
|---------|-------------|
| `make check` | Run tests then build (the default verification step) |
| `make build` | Build the `wt` binary |
| `make test` | Run `go test ./...` |
| `make web` | Build vite output (`cd web && npm run build`) |
| `make clean` | Remove built binary |

Run `make check` to verify changes. Run `make web` before `make check` if you changed anything in `web/`.

## CLI Commands

| Command | What it does |
|---------|-------------|
| `wt [prompt]` | Submit and run a task |
| `wt --skill [name]` | Run a named skill |
| `wt --agent [name]` | Override agent for this task |
| `wt timeline` | List recent tasks |
| `wt thread` | Print daily thread |
| `wt log [id]` | Show task log events |
| `wt retry [id]` | Retry a failed task |
| `wt status` | Task counts and token usage |
| `wt schedule` | Manage recurring tasks |
| `wt agent list` | List configured agents |
| `wt embed` | Generate embeddings |
| `wt doctor` | Scan for available agents, API keys, services |
| `wt serve` | Start the relay web server |
| `wt init` | Initialize ~/.wingthing directory and DB |
| `wt login/logout` | Device auth with relay server |
| `wt skill list/add/enable/disable` | Manage skills (local + registry) |
| `wt post "text" --link URL --mass N` | Post to wt social (local, self-hosted) |
| `wt vote <post-id>` | Upvote a post on wt social |
| `wt comment <post-id> "text"` | Comment on a post |
