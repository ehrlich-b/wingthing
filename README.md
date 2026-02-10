# wingthing

[![cinch](https://cinch.sh/badge/github.com/ehrlich-b/wingthing.svg)](https://cinch.sh/jobs/github.com/ehrlich-b/wingthing)

One interface to every AI agent. Curated skills, sandboxed execution, any backend. Your machine, reachable from anywhere.

## What it is

`wt` is a single Go binary that wraps every AI agent CLI behind one stable interface. New AI framework drops next week? `wt skill add [new-thing]`. You learn `wt` once. The providers change behind it.

```
wt "summarize my git log"              # runs with your default agent
wt --skill compress                    # run a curated skill
wt --skill compress --agent ollama     # same skill, different backend
wt --agent gemini "explain this error" # switch agents on the fly
```

## Wings (Tailscale for agents)

A wing is your machine, reachable from anywhere. Run `wt wing` and it connects outbound to the relay via WebSocket. No port forwarding, no tunneling, no static IP. Works behind any NAT/firewall.

```bash
wt wing                                # connect to relay
wt wing --relay https://my-relay.com   # self-hosted relay
wt wing --labels gpu,cuda              # with labels for routing
```

Open the web dashboard from your phone or another machine. Start a live terminal session (Claude Code, Codex, ollama — any agent with a CLI), or chat. Output streams back in real-time. Detach and reattach sessions across devices.

```
Browser (xterm.js)  <->  Relay  <->  Wing (your machine)
```

The relay is a dumb pipe. It forwards opaque bytes between your browser and your wing. E2E encryption means the relay operator can't read your data.

## How it's different

**Curated, not marketplace.** Skills are checked into the repo, reviewed, validated. You enable what you want, disable what you don't, add your own.

**Sandboxed by default.** Agents run in containers (Apple Containers on macOS, namespace/seccomp on Linux) with explicit mount points and network controls. Isolation level is per-skill.

**Agent-agnostic.** `claude`, `ollama`, `gemini`, `codex` -- and whatever ships next. One skill works with any backend.

**Local-first.** Your machine, your keys, your data. No cloud dependency. Works offline with ollama.

**Self-hostable.** Run your own relay with `wt serve`. SQLite, single binary, no external dependencies.

## Install

Requires Go 1.25+ and Node.js (for web assets).

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make check     # test + build (produces ./wt binary)
```

## Quick start

```bash
wt init                                # initialize config + database
wt doctor                              # scan for available agents
wt "summarize my git log"              # run a task
wt wing                                # connect to relay (remote access)
```

## CLI

```
wt "prompt"                 # one-shot task
wt --skill compress         # run a skill
wt --agent ollama "prompt"  # use specific agent
wt wing                     # connect to relay
wt wing --labels gpu        # with routing labels
wt timeline                 # recent tasks
wt thread                   # today's daily thread
wt status                   # task counts + token usage
wt log --last               # most recent task log
wt doctor                   # scan for agents, keys, services
wt serve                    # start the relay server
wt login / logout           # device auth with relay
wt skill list               # installed skills
wt skill add file.md        # install a skill
```

## Skills

Markdown files with YAML frontmatter. Checked into the repo, installable with `wt skill add`.

```markdown
---
name: compress
description: Compress RSS articles to 1024 chars
memory: [feeds]
tags: [rss, compress]
---
Read the RSS feeds below. For each, extract the 5 most recent
articles. Compress each to max 1024 characters.

## Feeds
{{memory.feeds}}
```

Skills declare what memory they need, what isolation level to run at, and optionally a cron schedule. The skill body is a prompt template with interpolation. No `agent:` declared means it falls through to your default, overridable with `--agent`.

## Agents

`wt doctor` detects what you have installed:

| Agent | CLI | Notes |
|-------|-----|-------|
| Claude Code | `claude` | 200k context |
| Ollama | `ollama` | free, local, offline |
| Gemini | `gemini` | 1M context |
| Codex | `codex` | OpenAI |

Resolution precedence: **`--agent` flag > skill frontmatter > config default**

## Sandbox

Every agent execution is isolated. Isolation level is per-skill via frontmatter.

| Platform | Implementation |
|----------|---------------|
| macOS 26+ | Apple Containers |
| Linux | Namespaces + seccomp + landlock |
| Fallback | Process isolation (restricted env, isolated tmpdir) |

Levels: `strict` (no network, minimal fs), `standard` (no network, mounted dirs), `network` (network + mounted dirs), `privileged` (full access, skips sandbox).

## Self-hosting

Run your own relay. Single binary, SQLite, no external dependencies.

```bash
make build
./wt serve --addr :8080
```

Connect a wing to your self-hosted relay:

```bash
wt wing --relay http://localhost:8080
```

Docker:

```bash
docker build -t wingthing .
docker run -p 8080:8080 -v wt-data:/data wingthing
```

## wt social

Link aggregator with embedding-based space assignment. 159 semantic spaces from physics to poetry. RSS feeds in, AI-compressed summaries scored and posted, natural time decay, voting, comments.

Live at [wingthing.ai/social](https://wingthing.ai/social).

## License

MIT
