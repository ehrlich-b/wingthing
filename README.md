# wingthing

Local-first AI task runner. Orchestrates LLM agents on your behalf so you never write a prompt again.

## What it is

A Go daemon (`wt`) that runs on your machine. It knows about you (text-file memory), constructs context-rich prompts, fires them at LLM CLIs (`claude -p`, `codex exec`, `gemini`, `ollama`), runs them in sandboxes, and manages a task timeline. Works offline.

```
wt daemon
├── timeline engine     ← tasks with a when, agents schedule follow-ups
├── memory store        ← text files in ~/.wingthing/memory/, no embeddings
├── daily thread        ← running markdown log of today, injected into every prompt
├── orchestrator        ← pure Go context builder, not an LLM
├── sandbox runtime     ← containers per task (Apple Containers, namespace/seccomp)
├── agent adapters      ← claude, codex, gemini, ollama, opencode, goose...
└── transport           ← HTTP-over-Unix-socket CLI ↔ daemon
```

## Install

Requires Go 1.21+.

```bash
go install github.com/ehrlich-b/wingthing/cmd/wt@latest
```

Or build from source:

```bash
git clone https://github.com/ehrlich-b/wingthing.git
cd wingthing
make build     # produces ./wt binary
```

## Quick start

```bash
# 1. Initialize config, memory, and database
wt init

# 2. Start the daemon (foreground — open a second terminal for CLI)
wt daemon

# 3. Submit a task
wt "summarize my git log for the last week"

# 4. Check the timeline
wt timeline

# 5. View today's thread
wt thread
```

## CLI

```
wt "do the thing"           # one-shot task
wt --skill jira             # run a skill
wt --agent ollama "prompt"  # use specific agent (claude, ollama)
wt timeline                 # upcoming + recent tasks
wt thread                   # today's daily thread
wt thread --yesterday       # yesterday's thread
wt status                   # daemon health + token usage
wt log --last               # most recent task log
wt log --last --context     # full prompt audit
wt retry <task-id>          # retry a failed task
wt schedule list            # recurring tasks
wt schedule remove <id>     # cancel recurring task
wt agent list               # configured agents
wt skill list               # installed skills
wt skill add file.md        # install a skill
wt login                    # authenticate with wingthing.ai
wt logout                   # remove device token
wt daemon                   # start daemon foreground
wt init                     # initialize ~/.wingthing/
```

## Four pillars

**Timeline** — Everything is a task with a `when`. Agents schedule follow-up tasks ("check the build in 10 min"). One abstraction replaces heartbeats, crons, and agent-to-agent communication.

**Daily thread** — Running markdown log of everything that happened today. Injected into every task's prompt. Makes stateless invocations feel like a continuous conversation.

**Memory** — Text files in `~/.wingthing/memory/`. Human-readable, git-diffable, no embeddings. Layered retrieval: always-loaded index, skill-declared deps, keyword grep. Pure Go, no model call needed.

**Orchestrator** — Go code that assembles prompts: identity + memory + daily thread + skill template + task. Not an LLM. Rule-based context routing covers 80%+ of tasks.

## Skills

Skills are markdown files with YAML frontmatter. They declare memory deps, agent preferences, isolation level, and a prompt template.

```markdown
---
name: jira
description: Jira briefing session
agent: claude
memory:
  - identity
  - projects
memory_write: false
---
You are a Jira concierge. {{identity.name}} needs a briefing.

Today so far:
{{thread.summary}}

The user said: {{task.what}}
```

Install with `wt skill add jira.md`, run with `wt --skill jira`.

## Structured output

Agents can schedule follow-up tasks and write memory by including markers in their output:

```html
<!-- wt:schedule delay="10m" memory="deploy-log,projects" -->
check if the deploy succeeded
<!-- /wt:schedule -->

<!-- wt:memory file="deploy-log" -->
Deployed v2.3.1 to production at 14:30 UTC.
<!-- /wt:memory -->
```

Schedule directives support `memory="file1,file2"` to declare which memory files the follow-up task should load.

## Architecture

```
~/.wingthing/
├── config.yaml      # agent defaults, machine ID, custom vars
├── wt.db            # SQLite — tasks, thread, agents, logs
├── wt.sock          # Unix socket for CLI ↔ daemon
├── memory/
│   ├── index.md     # always loaded into every prompt
│   └── identity.md  # who you are
└── skills/
    └── *.md         # installed skills
```

21 packages, two binaries (`wt` daemon + `wtd` relay), no CGO, pure Go SQLite.

The relay server (`wtd`) is a separate binary:

```
wtd --addr :8080 --db wtd.db
├── WebSocket hub     ← daemon + client connections
├── session manager   ← routes messages between daemons and clients
├── auth endpoints    ← device code flow, token management
├── web UI            ← PWA served at /app
└── SQLite store      ← users, tokens, device codes, audit log
```

## v0.2 features

- **Ollama adapter** — fully offline task execution via local models
- **Apple Containers sandbox** — lightweight Linux VMs on macOS 26+ for agent isolation
- **Linux namespace/seccomp sandbox** — process isolation with restricted syscalls
- **Recurring tasks** — cron expressions in skills (`schedule: "0 8 * * 1-5"`), auto-reschedule after each run
- **Retry policies** — exponential backoff (1s, 2s, 4s... capped at 5min), configurable per task
- **Cost tracking** — token usage from claude stream-json, `wt status` shows daily/weekly totals
- **Schedule memory declarations** — follow-up tasks can declare which memory files to load
- **Agent health** — startup probes, 60s TTL cache, unhealthy agents blocked from dispatch

## v0.3 features

- **Memory sync** — file-level diffing with SHA-256 manifests, conflict logging, additive-only merges
- **WebSocket client** — outbound connection to relay with auto-reconnect, exponential backoff, offline message queuing
- **Device auth** — device code flow, token storage (0600 perms), `wt login` / `wt logout`
- **Relay server** (`wtd`) — WebSocket routing between daemons and clients, session management, auth endpoints, audit log
- **Web UI** — PWA at /app with task submission, timeline, thread view, real-time WebSocket updates, offline caching via service worker

## v0.4 features

- **Gemini adapter** — third agent backend, shells out to `gemini` CLI with 1M token context window
- **Skill registry** — relay-hosted skill catalog with categories, search, SHA-256 checksums; `wt skill add --available` to browse; seeded with 128 skills
- **E2E encryption** — Argon2id key derivation + XChaCha20-Poly1305 symmetric encryption for memory sync; relay sees only ciphertext
- **Task dependencies** — `wt "task B" --after <task-A-id>` or `<!-- wt:schedule after="<id>" -->` in agent output; blocked tasks wait until deps complete
- **Thread merge** — multi-machine thread entries interleave by timestamp with dedup; renders with machine origin when entries come from multiple machines

## wingthing.ai

Not a hosted agent. A sync and relay layer.

- Memory sync across machines (encrypted at rest, relay sees ciphertext only)
- Remote relay: phone/web -> WebSocket -> your daemon -> agent runs locally
- Web UI for timeline, thread, task submission at wingthing.ai/app
- Credentials never leave your machine

## Status

v0.4 — Skill registry, E2E encryption, task dependencies, multi-machine thread merge. See [DRAFT.md](DRAFT.md) for the full design and [TODO.md](TODO.md) for the roadmap through v1.0.
