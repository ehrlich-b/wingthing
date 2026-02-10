# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

One CLI, every AI agent. Your machine, reachable from anywhere.

```
wt "summarize my git log"              # runs with your default agent
wt --skill compress --agent ollama     # curated skill, any backend
wt wing                                # your machine, reachable from anywhere
```

## Wings

A wing is your machine, reachable from anywhere. `wt wing` connects outbound to the relay via WebSocket — no port forwarding, no static IP, works behind any NAT.

```
Browser (xterm.js)  <->  Relay  <->  Wing (your machine)
```

Open the web dashboard from your phone. Start a live terminal (Claude Code, Codex, ollama), chat with agents, detach and reattach across devices. The relay is a dumb pipe — it forwards opaque bytes, never reads your data.

## Skills

Skills are markdown files with YAML frontmatter — prompt templates that work with any agent. Curated and checked into this repo, installable with `wt skill add`.

```markdown
---
name: compress
description: Compress RSS articles to 1024 chars
memory: [feeds]
isolation: standard
---
Read the RSS feeds below. Extract the 5 most recent articles per feed.
Compress each to max 1024 characters.

## Feeds
{{memory.feeds}}
```

Skills declare what memory they need, what sandbox isolation level to run at, and optionally a cron schedule. No `agent:` field means it uses your default — override with `--agent`.

## Agents

`wt doctor` detects what's installed. One skill works with any backend.

| Agent | CLI | Notes |
|-------|-----|-------|
| Claude Code | `claude` | 200k context |
| Ollama | `ollama` | free, local, offline |
| Gemini | `gemini` | 1M context |
| Codex | `codex` | OpenAI |

Resolution: **`--agent` flag > skill frontmatter > config default**

## Sandbox

Every agent execution is sandboxed. Isolation level is per-skill via frontmatter.

| Platform | Method |
|----------|--------|
| macOS 26+ | Apple Containers |
| Linux | Namespaces + seccomp + landlock |
| Fallback | Process isolation |

Levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (full network), `privileged` (no sandbox).

## Install

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make check     # test + build → ./wt
```

Requires Go 1.25+ and Node.js.

## Self-hosting

Single binary, SQLite, no external dependencies.

```bash
./wt serve --addr :8080                          # from source
docker run -p 8080:8080 -v wt-data:/data wingthing  # docker
wt wing --relay http://localhost:8080             # connect a wing
```

## License

MIT
