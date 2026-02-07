# wingthing

Local-first AI task runner. Orchestrates LLM agents on your behalf so you never write a prompt again.

OpenClaw is a brain you talk to. Wingthing is a hand that directs brains.

## What it is

A Go daemon (`wt`) that runs on your machine. It knows about you (text-file memory), constructs context-rich prompts, fires them at LLM CLIs (`claude -p`, `codex exec`, `gemini`, `ollama`), runs them in sandboxes, and manages a task timeline. Works offline.

```
wt daemon
├── timeline engine     ← tasks with a when, agents schedule follow-ups
├── memory store        ← text files in ~/.wingthing/memory/, no embeddings
├── daily thread        ← running markdown log of today, injected into every prompt
├── orchestrator        ← pure Go context builder, not an LLM
├── sandbox runtime     ← containers per task (Apple Containers, Docker/OCI)
├── agent adapters      ← claude, codex, gemini, ollama, opencode, goose...
└── sync client         ← optional WebSocket to wingthing.ai
```

## Four pillars

**Timeline** — Everything is a task with a `when`. Agents schedule follow-up tasks ("check the build in 10 min"). One abstraction replaces heartbeats, crons, and agent-to-agent communication.

**Daily thread** — Running markdown log of everything that happened today. Injected into every task's prompt. Makes stateless invocations feel like a continuous conversation. Syncs across machines.

**Memory** — Text files in `~/.wingthing/memory/`. Human-readable, git-diffable, no embeddings. Layered retrieval: always-loaded index, skill-declared deps, keyword grep, two-pass loading. Pure Go, no model call needed.

**Orchestrator** — Go code that assembles prompts: identity + memory + daily thread + skill template + task. Not an LLM. Rule-based context routing covers 80%+ of tasks.

## wingthing.ai

Not a hosted agent. A sync and relay layer.

- Memory sync across machines (encrypted)
- Remote relay: phone/web → WebSocket → your daemon → agent runs locally
- Web UI for timeline, thread, task submission
- Credentials never leave your machine

```
Phone → wingthing.ai (relay) → WebSocket → wt daemon (your machine) → claude -p
```

## CLI

```
wt "do the thing"           # one-shot task
wt --skill jira             # run a skill
wt timeline                 # upcoming + recent tasks
wt thread                   # today's daily thread
wt status                   # daemon health
wt log --last --context     # full prompt audit
wt daemon --install         # system service
wt login                    # connect to wingthing.ai
```

## Status

Early. Private. Designing the protocol. See [DRAFT.md](DRAFT.md) for the full design.
