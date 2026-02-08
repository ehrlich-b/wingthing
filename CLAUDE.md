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
- Agents are pluggable (claude, ollama, etc). `wt` calls them directly, not through a daemon.

## Spaces

- `spaces.yaml` is the single source of truth for space definitions (159 entries)
- `internal/embedding.SpaceIndex` loads YAML, embeds centroids, caches per-embedder as `.bin` files
- Multi-embedder: embed centroids with every provided Embedder, anyone can bring their own

## Key Packages

| Package | Role |
|---------|------|
| `internal/embedding` | Embedder interface, OpenAI/Ollama adapters, SpaceIndex, cosine/blend |
| `internal/relay` | RelayStore, social embeddings, anchor seeding, skills |
| `internal/agent` | LLM agent adapters (claude, ollama) |
| `internal/config` | Config loading, `~/.wingthing/` paths |
