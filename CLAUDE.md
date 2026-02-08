# Wingthing

## What This Is

`wt` is **SQLite on steroids** -- an inert chunk of state that animates itself with LLMs and OS-level scheduling (cron, launchd, systemd timers). There is no persistent daemon. The CLI reads/writes state and invokes agents directly. Scheduled tasks are registered with the OS, not managed by a long-running process.

`wtd` is the **relay server** -- the social backend. Separate concern, separate binary, separate deployment.

## Dogfooding

**Always use wingthing's own tools and infrastructure.** If wingthing can do something, use wingthing to do it. Don't shell out to external scripts or paid APIs when the equivalent exists (or should exist) in the codebase.

If you find yourself reaching for an external tool and wingthing _should_ handle it, that's a gap to fill in wingthing itself.

## Architecture

- `wt` -- generic AI swiss army knife CLI. Inert state + direct agent invocation + OS-scheduled animation.
- `wtd` -- relay server (social backend), HTTP + SQLite, separate deployment
- Agents are pluggable (claude, ollama, gemini). `wt` calls them directly, not through a daemon.

## Daemon Excision (in progress)

Most commands still go through a unix socket to a daemon process. We are migrating them to call agents/stores directly. **`wt embed` and `wt doctor` are the template** -- they use config + direct invocation, no socket.

Commands already daemon-free: `embed`, `doctor`, `init`, `login`, `logout`, `skill list` (local), `skill add`
Commands still on socket: root prompt, `timeline`, `thread`, `log`, `retry`, `agent list`, `schedule`, `status`

Pattern for migration: replace `clientFromConfig()` (socket) with `store.Open(cfg.DBPath())` (direct SQLite).

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

## CLI Commands

| Command | Status | What it does |
|---------|--------|-------------|
| `wt embed` | Direct | Embed text via auto-detected or specified provider |
| `wt doctor` | Direct | Scan for available agents, API keys, services |
| `wt init` | Direct | Initialize ~/.wingthing directory and DB |
| `wt login/logout` | Direct | Device auth with relay server |
| `wt skill list/add` | Direct | Manage skills (local + registry) |
| `wt [prompt]` | Socket | Submit task to daemon (needs migration) |
| `wt timeline` | Socket | List tasks (needs migration) |
| `wt thread` | Socket | Daily thread (needs migration) |
| `wt log` | Socket | Task logs (needs migration) |
| `wt daemon` | Legacy | Start daemon (will be removed) |
