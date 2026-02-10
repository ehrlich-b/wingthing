# wingthing

One interface to every AI agent. Curated skills, sandboxed execution, any backend.

## What it is

`wt` is a single Go binary that wraps every AI agent CLI behind one stable interface. New AI framework drops next week? `wt skill add [new-thing]`. You learn `wt` once. The providers change behind it.

```
wt "summarize my git log"              # runs with your default agent
wt --skill compress                    # run a curated skill
wt --skill compress --agent ollama     # same skill, different backend
wt --agent gemini "explain this error" # switch agents on the fly
```

AI tooling moves too fast to learn every new CLI. Wingthing is the stable layer: a curated library of validated skills, persistent memory, and sandboxed execution across any LLM backend.

## How it's different

Most open-source AI agent projects get the demand right but the execution wrong.

**Curated, not marketplace.** Skills are checked into the repo, reviewed, validated. Not a storefront where anyone can publish prompt injections. You enable what you want, disable what you don't, add your own.

**Sandboxed by default.** Agents run in containers (Apple Containers on macOS, namespace/seccomp on Linux) with explicit mount points and network controls. Isolation level is per-skill, not an afterthought you toggle on.

**Agent-agnostic.** `claude`, `ollama`, `gemini` -- and whatever ships next. One skill works with any backend. Use ollama for free, claude when you need it.

**Local-first.** Your machine, your keys, your data. No cloud dependency. Works offline with ollama.

## Install

Requires Go 1.21+.

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make build     # produces ./wt binary
```

## Quick start

```bash
# 1. Initialize config, memory, and database
wt init

# 2. Run a task
wt "summarize my git log for the last week"

# 3. Run a skill
wt --skill compress

# 4. Check the timeline
wt timeline

# 5. View today's thread
wt thread
```

## CLI

```
wt "do the thing"           # one-shot task
wt --skill jira             # run a skill
wt --agent ollama "prompt"  # use specific agent
wt timeline                 # recent tasks
wt thread                   # today's daily thread
wt status                   # task counts + token usage
wt log --last               # most recent task log
wt log --last --context     # full prompt audit
wt retry <task-id>          # retry a failed task
wt schedule list            # recurring tasks
wt agent list               # configured agents
wt skill list               # installed skills
wt skill add file.md        # install a skill from file
wt skill list --available   # browse the registry
wt doctor                   # scan for agents, keys, services
wt serve                    # start the relay/social server
wt init                     # initialize ~/.wingthing/
wt login / logout           # device auth with relay
```

## Skills

Skills are the product. Markdown files with YAML frontmatter, checked into the repo, installable with `wt skill add`.

```markdown
---
name: compress
description: Fetch RSS feeds and compress articles to 1024 chars
memory:
  - feeds
tags: [rss, compress, content]
---
Read the RSS feed URLs listed below. For each feed, extract the 5
most recent articles. Compress each to max 1024 characters.

## Feeds
{{memory.feeds}}
```

No `agent:` declared -- falls through to your default, overridable with `--agent`. Skills declare what memory they need, what isolation level to run at, and optionally a cron schedule for recurring execution. The skill body is a prompt template with interpolation.

Install: `wt skill add skills/compress.md`
Run: `wt --skill compress`
Override agent: `wt --skill compress --agent ollama`

## Memory

Text files in `~/.wingthing/memory/`. Human-readable, git-diffable. Layered retrieval: always-loaded index, skill-declared deps, keyword matching. Pure Go, no model call needed.

```
~/.wingthing/memory/
  index.md       # always loaded into every prompt
  identity.md    # who you are
  feeds.md       # RSS feeds for the compress skill
  projects.md    # active work context
```

## Agents

`wt doctor` detects what you have installed:

| Agent | CLI | Context | Cost |
|-------|-----|---------|------|
| claude | `claude` | 200k tokens | API key |
| ollama | `ollama` | model-dependent | free (local) |
| gemini | `gemini` | 1M tokens | API key |

Resolution precedence: **`--agent` flag > skill frontmatter > config default**

## Structured output

Agents can schedule follow-up tasks and write to memory:

```html
<!-- wt:schedule delay="10m" memory="deploy-log" -->
check if the deploy succeeded
<!-- /wt:schedule -->

<!-- wt:memory file="deploy-log" -->
Deployed v2.3.1 to production at 14:30 UTC.
<!-- /wt:memory -->
```

## Architecture

```
~/.wingthing/
  config.yaml      # agent defaults, machine ID
  wt.db            # SQLite -- tasks, thread, agents, logs
  memory/          # text files, always human-readable
  skills/          # installed skill files
```

Single binary, no daemon, no socket. `wt` reads/writes SQLite directly and invokes agents as child processes. Scheduled tasks use OS-level scheduling (cron, launchd, systemd timers).

`wt serve` runs the relay server for the social feed and web UI.

## wt social

Link aggregator with embedding-based space assignment. RSS feeds in, compressed summaries scored and posted, natural time decay, voting, comments. 159 semantic spaces from physics to poetry.

Not a bot network. Content is curated by skills you control, scored by heuristics you can tune, posted to a feed where humans vote.

## Status

Active development. See [TODO.md](TODO.md) for the roadmap.
