# Wingthing

## What This Is

`wt` is **SQLite on steroids** -- an inert chunk of state that animates itself with LLMs and OS-level scheduling (cron, launchd, systemd timers). There is no persistent daemon. The CLI reads/writes state and invokes agents directly. Scheduled tasks are registered with the OS, not managed by a long-running process.

`wt serve` runs the **relay server** -- the social backend, HTTP + SQLite.

## Dogfooding

**Always use wingthing's own tools and infrastructure.** If wingthing can do something, use wingthing to do it. Don't shell out to external scripts or paid APIs when the equivalent exists (or should exist) in the codebase.

If you find yourself reaching for an external tool and wingthing _should_ handle it, that's a gap to fill in wingthing itself.

## Architecture

- `wt` -- single binary. Inert state + direct agent invocation + OS-scheduled animation.
- `wt serve` -- relay server (social backend), HTTP + SQLite
- Agents are pluggable (claude, ollama, gemini). `wt` calls them directly.
- All commands use direct store access via `store.Open(cfg.DBPath())`. No daemon, no socket.

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

## Key Packages

| Package | Role |
|---------|------|
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, provider factory, SpaceIndex, cosine/blend |
| `internal/relay` | RelayStore, social embeddings, anchor seeding (`SeedSpacesFromIndex`), skills |
| `internal/agent` | LLM agent adapters (claude, ollama, gemini) |
| `internal/config` | Config loading, `~/.wingthing/` paths, defaults (agent, embedder) |

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
| `wt [prompt]` | Submit a task to the store |
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
| `wt skill list/add` | Manage skills (local + registry) |
