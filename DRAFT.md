# Wingthing — Design Draft

> Local-first AI task runner. Orchestrates LLM agents on your behalf so you never write a prompt again.

---

## The Problem

The AI agent landscape in early 2026:

- **OpenClaw** (68k+ stars) is a personal AI assistant accessed via messaging apps (Signal, Telegram, WhatsApp). It IS the agent. You talk to it. It runs on your machine with broad, unsandboxed permissions. Security researchers call it "a data-breach scenario waiting to happen." 430k+ lines of Node.js. 12% of skills on ClawHub are malware.
- **NanoClaw** is the secure reaction — 500 lines of TypeScript, Apple Container isolation, but opinionated (one LLM, one messaging platform, one database, one machine).
- **Gastown** (Steve Yegge) is Kubernetes for coding agents — 75k lines of Go, Mayor/Polecat/Witness architecture, $100/hr burns. Multi-agent orchestration for codebases.
- **MCP** is the tool protocol (how agents call tools). **A2A** is the inter-agent protocol (how agents talk to each other). **ACP** is the messaging format.
- **25+ skill registries** exist (ClawHub, Skills.sh, SkillsMP). Most are unverified. Nobody has signing, publisher verification, or supply chain security.

What nobody has built: **a local-first orchestrator that wields LLM CLIs on your behalf, with your full context, in sandboxes, that works offline and syncs when connected.**

OpenClaw IS the agent. Wingthing WIELDS agents.

---

## What Wingthing Is

Wingthing is a Go binary — a daemon that runs on your machine. It:

1. **Knows about you** — your identity, projects, machines, preferences, stored in human-readable text files you can edit by hand
2. **Fully manages context** — constructs every prompt from scratch, injecting exactly the memory and state each task needs. Agents are stateless. Wingthing is stateful.
3. **Fires tasks at LLM CLIs** — `claude -p "..."`, `gemini`, local models via `ollama`. Wingthing doesn't care which brain it uses.
4. **Runs agents in sandboxes** — every agent execution is isolated. Container-first, not bolted on.
5. **Drives a task timeline** — tasks schedule future tasks. An agent can say "check the build in 10 minutes" and wingthing wakes up a new agent invocation at that time.
6. **Maintains a daily thread** — a running context of everything that's happened today, carried across tasks and machines so agents always know what you've been doing.
7. **Works offline** — no cloud dependency. wingthing.ai is a sync and relay layer, not a requirement.

### The Core Insight

Every agent framework treats memory, context, and human preferences as implementation details — afterthoughts stuffed into system prompts. Wingthing says: **the relationship between human and AI is the product.** The memory format, the context injection, the orchestration of which brain to task with what — that's the hard problem. The LLMs themselves are commodity backends.

---

## Architecture

### The daemon is the product

```
wt daemon (always local, always works)
├── timeline engine        ← pure Go, no network needed
├── memory store           ← local text files, always available
├── daily thread           ← running context of today's activity
├── context builder        ← orchestrator that assembles prompts
├── sandbox runtime        ← local containers
├── agent adapters         ← claude -p, gemini, ollama, whatever is installed
└── sync client (optional) ← WebSocket to wingthing.ai when available
```

`wt daemon` is fully autonomous. It works on a plane. It works air-gapped with just ollama. wingthing.ai is an enhancement, never a dependency.

### wingthing.ai is three things

```
┌─────────────────────────┐
│     wingthing.ai        │
│                         │
│  1. Memory sync         │  ← rsync for ~/.wingthing/ across machines
│  2. Remote UI           │  ← web + phone access to your daemon
│  3. Orchestrator boost  │  ← cheap LLM for context routing (optional)
│                         │
└────────┬────────────────┘
         │ WebSocket (outbound from user's machine)
         │ no port forwarding, punches through NAT
         │
┌────────▼────────────────┐
│   wt daemon             │
│   (user's machine)      │  ← runs agents with user's credentials
│                         │     in user's sandboxes
└─────────────────────────┘
```

**Why outbound WebSocket?** The user's machine connects out to wingthing.ai. No port forwarding. No tunneling. No static IP. Works behind any NAT/firewall. Same pattern as Cloudflare Tunnel / Tailscale.

**Why this dodges TOS issues:** The user's machine runs `claude -p` with the user's own Claude Code install and credentials. wingthing.ai never touches an LLM directly. We're a sync/relay layer, not an API proxy.

### Connection states

| State | Orchestrator | Execution | UI | Memory |
|---|---|---|---|---|
| **Online + wingthing.ai** | Cheap cloud LLM (optional) or local rules | User's machine | Web, phone, CLI | Synced across machines |
| **Online, no wt.ai** | Local rules or local model | User's machine | CLI only | Local only |
| **Fully offline** | Local rules or ollama | Ollama / local models | CLI only | Local only |

The orchestrator (context routing) doesn't even need a model for most tasks. Rule-based retrieval — always-loaded index, skill-declared deps, keyword grep — is pure Go. No model call. That covers 80%+ of tasks.

---

## The Four Pillars

### 1. Timeline (The Task Engine)

The timeline is the core abstraction. Everything is a task on a timeline.

A task is:

```yaml
id: "t-20260206-001"
what: "Check if PR #892 has been approved"    # or a skill reference
when: "2026-02-06T14:30:00Z"                  # when to execute
agent: claude                                  # which LLM to use
isolation: standard                            # sandbox level
memory: [identity, projects]                   # which memory to load
parent: "t-20260206-000"                       # task that spawned this one (if any)
status: pending                                # pending | running | done | failed
```

**Task lifecycle:**

```
Human submits task (via CLI, API, phone)
    or
Cron fires a scheduled task
    or
Running agent schedules a follow-up task
        ↓
Task enters the timeline at its `when` time
        ↓
When time arrives, wingthing:
  1. Reads the daily thread (what's happened so far today)
  2. Runs the orchestrator (selects memory, builds context)
  3. Constructs the full prompt
  4. Spawns a sandbox
  5. Fires the agent CLI
        ↓
Agent runs, produces output
Agent MAY schedule follow-up tasks
        ↓
Wingthing captures output
Wingthing appends to daily thread
Wingthing updates memory if task produced durable state
Wingthing marks task done
Wingthing checks timeline for next task
```

**Agents schedule follow-up tasks** via structured output:

```
Agent running a deploy check:
  "Build started. I'll check back in 10 minutes."
  → output includes: <!-- wt:schedule delay=10m -->Check build status for PR #892<!-- /wt:schedule -->
  → wingthing parses it, adds a new task at now+10m
  → agent exits
  → 10 minutes later, wingthing fires a new agent with fresh context
```

This replaces heartbeats, crons, and agent-to-agent communication with one unified abstraction: **tasks on a timeline.**

**Scheduled/recurring tasks** are just tasks with cron expressions in their `when` field:

```yaml
what: !skill jira-briefing
when: "0 8 * * 1-5"    # weekdays at 8am
agent: claude
```

### 2. Daily Thread (Running Context)

The daily thread is a running log of everything that's happened today. It follows you across tasks and across machines (via sync).

```
~/.wingthing/threads/
├── 2026-02-06.md    # today
├── 2026-02-05.md    # yesterday
└── ...              # rolling window (configurable retention)
```

**Format:**

```markdown
# 2026-02-06 (Thursday)

## 08:02 — Jira Briefing [claude, jira-briefing]
- Sprint SLIDE-4521: rate engine migration in progress
- PR #892 awaiting review from Sarah
- No blockers. Light day.

## 09:15 — PR Status Check [claude, ad-hoc]
> User: "has sarah reviewed my PR yet?"
- PR #892: Sarah approved 12 minutes ago
- CI passing, ready to merge
- Scheduled: merge check in 30m

## 09:47 — Merge Check [claude, scheduled]
- PR #892 merged to main
- Staging deploy triggered
- Scheduled: staging health check in 15m
```

**How it's used:**

The daily thread is injected into every task's prompt (or a summary of it, if it's getting long). This gives each agent awareness of what's already happened today — without persistent sessions. Every `claude -p` invocation is fresh, but it reads the thread and feels continuous.

**Why this matters:**

The user sends a message from their phone at 2pm: "did that deploy land?" They don't say which deploy. They don't say which PR. The orchestrator reads the daily thread, sees the merge at 09:47, the staging deploy, the health check — and constructs a prompt with all that context. The agent answers coherently. To the user, it feels like a continuous conversation. Under the hood, it's a stateless invocation with injected context.

**Thread management:**
- Each task appends its summary to the thread
- Long threads get summarized (by the orchestrator or a cheap LLM call) to stay within token budgets
- Threads sync across machines via wingthing.ai
- Yesterday's thread is available but not loaded by default (only if the task seems to reference it)
- Configurable retention (default: 7 days of full threads, 30 days of summaries)

### 3. Memory (Text-Based Persistence)

Memory is **the** differentiator. Not a database. Not embeddings. Not a vector store. **Text files.**

```
~/.wingthing/
├── memory/
│   ├── index.md          # Master index — always loaded, TOC with one-line summaries
│   ├── identity.md       # Who the human is, how they work
│   ├── projects.md       # Active projects, status, context
│   ├── machines.md       # Machine registry (hostname, role, capabilities)
│   ├── relationships.md  # People, teams, how to interact
│   └── [topic].md        # Any topic — grows organically
├── threads/
│   ├── 2026-02-06.md     # Today's daily thread
│   └── ...
├── timeline/
│   ├── pending.yaml      # Tasks waiting to execute
│   ├── history.yaml      # Completed tasks (rolling window)
│   └── recurring.yaml    # Cron-scheduled tasks
├── agents/
│   ├── claude.yaml       # Claude agent config (CLI path, model, permissions)
│   ├── gemini.yaml       # Gemini agent config
│   └── ollama.yaml       # Local model config
├── skills/
│   ├── jira-briefing.md  # Skill definitions (prompt templates + metadata)
│   ├── pr-review.md
│   └── [skill].md
└── config.yaml           # Global config (default agent, sync settings)
```

**Design principles:**

- **Human-readable.** You can open any file in a text editor and understand it.
- **Human-editable.** You can modify memory by hand. No migration, no schema, no rebuild.
- **Git-diffable.** Memory changes are visible in `git diff`. You can version your memory.
- **Portable.** Copy `~/.wingthing/` to another machine. Done.
- **Syncable.** wingthing.ai syncs this directory across machines. Or use git. Or rsync. It's just files.

**Memory format spec:**

```markdown
---
topic: projects
updated: 2026-02-06
tags: [work, slide, side-projects, jira, pr]
---

# Active Projects

## Slide (Work)
- Current sprint: SLIDE-4521 (rate engine migration)
- PR open: #892 (awaiting review from Sarah)
- Blocked: waiting on cloud team for API changes

## Lang (Side Project)
- Status: bootstrapping phase 3
- Last session: fixed LLVM IR codegen for string literals
- Next: implement trait resolution
```

Frontmatter is optional but helps with retrieval. The body is freeform markdown.

#### Memory Retrieval (Without RAG)

RAG is overengineered for this. Wingthing uses a layered retrieval strategy, all implemented in pure Go:

**Layer 1: Always loaded.** `index.md` is included in every task. It's a table of contents — one line per topic file with a summary. Gives the LLM a map of what's available. (~50 lines, trivial token cost.)

**Layer 2: Skill-declared deps.** Each skill specifies which memory files it needs. The Jira briefing skill declares `memory: [identity, projects]`. Covers 80% of tasks.

**Layer 3: Keyword match.** Task prompt mentions "PR"? Wingthing greps memory file frontmatter tags and headings. Matches `projects.md` (has tag `pr`). Loads it. Fast, no model call.

**Layer 4: Daily thread.** Today's thread (or a summary of it) is always available. Gives temporal context without the agent needing to ask.

**Layer 5: Two-pass loading.** If the agent's response indicates it needs more context, wingthing can do a second pass with additional memory loaded. Costs one extra agent call, but only when needed.

**Layer 6: LLM triage (optional, online only).** For ambiguous tasks, send the task prompt + `index.md` to a fast/cheap model and ask which topic files are relevant. Not RAG — just a routing call. Only used when layers 1-4 don't produce a confident match. This is the "orchestrator boost" that wingthing.ai can provide.

**Why this works better than RAG:**
- No embedding pipeline. No vector database. No re-indexing.
- Deterministic. Skill-declared deps always load the same files. Reproducible.
- Human-debuggable. `wt log --last --context` shows exactly which memory was loaded and why.
- Works offline. Layers 1-5 are pure Go. No network, no model call.
- The LLM reads the actual text, not a similarity-scored chunk. Full document context.

#### Inter-Agent Communication Through Memory

Agents don't talk to each other. They communicate through structured memory.

```
Task A (Claude): "Research competitors in the backup space"
  → Agent runs, produces output
  → Wingthing writes results to memory/research-backup-competitors.md

Task B (Gemini, scheduled 5 min later): "Draft a positioning doc"
  → Skill declares: memory: [identity, projects, research-backup-competitors]
  → Agent sees Task A's output as memory context
  → Agent produces positioning doc
```

No message passing. No shared state. No coordination protocol. Just files. Wingthing mediates everything.

### 4. Orchestrator (The Context Builder)

The orchestrator is the brain that assembles each task's prompt. It runs locally, in pure Go, with optional LLM boost.

**What the orchestrator does for every task:**

1. Read the task (what the human asked, or what skill to run)
2. Read the daily thread (what's happened today)
3. Read the memory index (what's available)
4. Select relevant memory files (layers 1-5, rule-based)
5. Interpolate the skill template (if using a skill)
6. Construct the full prompt with: identity + memory + daily thread + task + tools
7. Select the agent (from skill config, or default)
8. Select the isolation level (from skill config, or default)
9. Hand off to the sandbox for execution

**The orchestrator is NOT an LLM by default.** It's Go code that follows rules. The LLM triage (Layer 6) is an optional enhancement when connected to wingthing.ai or when a local model is available.

**Each message = new sandbox.** The user sees what looks like a threaded conversation. Under the hood, every message is a fresh `claude -p` invocation in a new sandbox. The orchestrator + daily thread make it coherent. The user never knows.

```
User (phone, 2pm): "did that deploy land?"
        ↓
Orchestrator reads daily thread:
  - 09:47: PR #892 merged, staging deploy triggered
  - 10:02: staging health check passed
        ↓
Orchestrator builds prompt:
  "You are helping Bryan Ehrlich. Here's what happened today:
   [daily thread excerpt]
   He's asking about the deploy from this morning.
   Check current staging status and respond concisely."
        ↓
New sandbox → claude -p "<prompt>" → response → displayed on phone
        ↓
Appended to daily thread:
  ## 14:00 — Deploy Status [claude, ad-hoc]
  > User: "did that deploy land?"
  - Staging deploy from 09:47 is live and healthy
  - No errors in last 4 hours
```

---

## Sandbox Model

Learned from OpenClaw's mistakes. Every agent execution is isolated.

### Architecture

```
┌──────────────────────────────┐
│         wt daemon            │  ← trusted orchestrator, runs on host
│  (Go binary)                 │
└──────────┬───────────────────┘
           │ spawns per task
           ▼
┌──────────────────────────────┐
│       sandbox container       │  ← untrusted agent execution
│  ┌─────────────────────────┐ │
│  │  claude -p "prompt..."  │ │
│  │  (or gemini, ollama)    │ │
│  └─────────────────────────┘ │
│                               │
│  Mounts: per skill config    │
│  Network: per isolation level │
│  TTL: per task timeout        │
│  Resources: capped            │
└──────────────────────────────┘
```

### Isolation levels

| Level | What | Use case |
|-------|------|----------|
| **strict** | Read-only fs, no network, short TTL | Research, summarization, analysis |
| **standard** | Read-write to mounted dirs, no network | Code editing, file ops |
| **network** | Read-write + network access | API calls, git operations, package install |
| **privileged** | Full host access (requires explicit opt-in per task) | System admin, troubleshooting |

### How `wt.schedule()` works inside a sandbox

Agents schedule follow-up tasks via structured output convention:

```
<!-- wt:schedule delay=10m -->Check build status for PR #892<!-- /wt:schedule -->
<!-- wt:schedule at=2026-02-06T17:00:00Z -->Send EOD summary<!-- /wt:schedule -->
```

Wingthing parses this from the agent's output post-execution. No socket needed. Works with any LLM. For Claude specifically, can also be exposed as an MCP tool for cleaner ergonomics.

The parser must be defensive — agents hallucinate. Malformed markers are logged and ignored, never create garbage tasks.

### Implementation

- **macOS:** Apple Containers (lightweight Linux VMs on Apple Silicon)
- **Linux:** OCI containers (Docker, Podman, or native runc)
- **Hosted execution (future):** Firecracker microVMs
- **Fallback:** If no container runtime is available, wingthing warns loudly and runs with process-level isolation (restricted PATH, tmpdir jail, rlimits). Not recommended.

### Local models in sandboxes

Sandboxed agents need to reach a local ollama instance. Solution: expose ollama's port into the container. Ollama is trusted infrastructure (like a database), the agent is not. The container gets network access to `host.docker.internal:11434` and nothing else.

---

## Agent Adapters

Wingthing treats agent CLIs as black boxes. Each adapter knows how to:
1. Construct the CLI invocation
2. Stream output back
3. Parse structured markers from output
4. Report token usage (if available)

The adapter interface is trivial:

```go
type Agent interface {
    Run(ctx context.Context, prompt string, opts RunOpts) (Stream, error)
}
```

### Auth Model: It's the User's CLI

**Wingthing runs the user's own installed CLIs with the user's own credentials.** It doesn't extract tokens, spoof client IDs, or make API calls. It shells out to `claude -p`, `codex exec`, `gemini -p` — same as a CI pipeline or a shell script.

This is important because of what happened in Jan 2026: Anthropic blocked tools like OpenCode that were **extracting Claude Code's OAuth token and making their own API calls with it** — spoofing Claude Code's client identity to get subscription pricing. That's token extraction, not CLI orchestration.

Running `claude -p` as a subprocess is what CI/CD pipelines, GitHub Actions, and pre-commit hooks do every day. Anthropic actively promotes this. The user's subscription, API key, or Bedrock/Vertex credentials — whatever they've configured — works. Wingthing never touches auth.

The restriction that matters: **wingthing.ai cannot offer "Log in with Claude" or broker credentials.** The hosted relay routes tasks to the user's daemon, which runs the user's CLI. Credentials never leave the user's machine.

### Tier 1 Adapters (build first)

**Claude Code** (`claude -p`) — Primary adapter. Richest capabilities.

```bash
claude -p "<constructed prompt>" \
  --output-format stream-json \
  --verbose \
  --allowedTools "Bash,Read,Edit,Glob,Grep" \
  --append-system-prompt "<wingthing context injection>"
```

- Streaming: `stream-json` gives real-time events (text deltas, tool use, completion)
- Session resume: `--resume $session_id` for multi-turn tasks (interactive debugging, pair programming)
- Interactive mode: Agent SDK `ClaudeSDKClient` for bidirectional streaming sessions — long-lived, send follow-up messages, handle permissions programmatically
- Auth: user's own install (subscription, API key, Bedrock, Vertex — whatever they have)

**OpenCode** (`opencode run` / `opencode serve`) — MIT licensed, 75+ LLM providers.

```bash
opencode run "your prompt"           # one-shot
opencode serve                       # HTTP server for programmatic access
opencode attach                      # connect to running server
```

- Streaming: via `serve` HTTP API
- Sessions: `--continue`, `--session`, import/export
- Interactive: `serve` mode accepts requests to a running instance, `attach` connects new clients
- Auth: BYOK — any provider via env vars
- Why it matters: `serve` mode is almost exactly wingthing's architecture. MIT license. No restrictions.

### Tier 2 Adapters (strong candidates)

**Codex CLI** (`codex exec`) — Apache 2.0, OpenAI's coding agent.

- `codex exec "prompt"` with `--json` for NDJSON streaming
- Session resume: `codex resume --last` or by session ID
- Device-code auth for headless environments
- No official SDK yet but event stream is well-defined

**Copilot SDK** — MIT licensed, 4 languages (TS, Python, Go, .NET).

- JSON-RPC communication with Copilot CLI server
- BYOK mode bypasses GitHub auth entirely
- Technical preview, actively developing

**Goose** (`goose run`) — Apache 2.0, Block explicitly won't monetize.

- `goose run --with-builtin developer -t "instruction"`
- Recipe system for multi-step workflows (YAML-based)
- Adopting Agent Client Protocol (ACP) — designed for external orchestration
- BYOK for any provider

**Cline Core** — Apache 2.0, gRPC API.

- Standalone node process with gRPC interface
- Multiple frontends can attach simultaneously
- Multi-instance orchestration supported
- Docs immature but architecture is right

### Tier 3 Adapters (usable, limited)

| Tool | Command | License | Notes |
|------|---------|---------|-------|
| **Aider** | `aider --message "..." file.py` | Apache 2.0 | No sessions, fragile Python API |
| **Continue.dev** | `cn -p "prompt"` | Apache 2.0 | No streaming in headless |
| **Gemini CLI** | `gemini -p "prompt"` | Apache 2.0 | No sessions, clean headless |
| **Ollama** | `ollama run model "prompt"` | MIT | Local models, fully offline |
| **Kiro CLI** | `kiro-cli chat --no-interactive` | AWS IP License | Proprietary, needs legal review |

### Do Not Target

| Tool | Why |
|------|-----|
| **Cursor** | TOS prohibits derivative works, distribution, wrapping |
| **Windsurf** | No headless mode. TOS prohibits derivative works. |
| **Devin** | TOS prohibits making service available to non-authorized users |
| **Tabnine** | No headless coding agent |
| **Cody** | Enterprise-only, unstable protocol |

### Session Modes

Wingthing supports two execution modes per task:

**Task mode (default):** Fresh `claude -p` (or equivalent) per task. Orchestrator injects context. Agent is stateless. Wingthing controls the full prompt. Best for dispatch-and-forget tasks.

**Session mode (opt-in):** Spin up a persistent interactive session (Claude SDK `ClaudeSDKClient`, OpenCode `serve`, etc.). Proxy bidirectional stream through WebSocket to wingthing.ai. User interacts live from phone/web. Best for pair programming, debugging, exploration.

A task can **escalate** from task mode to session mode: user dispatches a task from their phone, sees the result, decides they want to jump in interactively. Wingthing spins up a session with the daily thread + task context pre-loaded.

For session mode in the browser: **xterm.js** (18k+ stars, powers VS Code's terminal) or a chat-style UI. Both relay through the existing WebSocket.

---

## Talk to Wingthing from Anywhere

### Transport: Outbound WebSocket

The daemon connects **outbound** to wingthing.ai. No port forwarding. No tunneling. No static IP.

```
┌──────────┐    ┌──────────┐    ┌──────────┐
│  Phone   │    │  Laptop  │    │  CI/CD   │
│  (PWA)   │    │  (CLI)   │    │ (webhook)│
└────┬─────┘    └────┬─────┘    └────┬─────┘
     │               │               │
     └───────┬───────┴───────┬───────┘
             │  HTTPS / WSS  │
             ▼               ▼
      ┌─────────────────────────┐
      │     wingthing.ai        │  ← relay + sync + UI
      │  (control plane)        │
      └────────┬────────────────┘
               │ WebSocket (outbound from daemon)
               ▼
      ┌─────────────────────────┐
      │     wt daemon           │  ← execution plane
      │  (user's machine)       │
      └─────────────────────────┘
```

**Flow:**
1. User submits task from phone via wingthing.ai
2. wingthing.ai relays task down the WebSocket to the daemon
3. Daemon runs the task locally (claude -p in sandbox, user's credentials)
4. Output streams back up the WebSocket to wingthing.ai
5. wingthing.ai displays result to the user's phone
6. Daily thread updated, memory synced

**Local CLI bypasses the cloud entirely:**
```bash
wt "what's my PR status?"   # goes straight to local daemon, no wingthing.ai involved
```

### Authentication

- **Local CLI → daemon:** Unix socket, no auth needed
- **wingthing.ai → daemon:** WebSocket with device-specific token (established during `wt login`)
- **User → wingthing.ai:** API key or OAuth (standard web auth)

### Offline behavior

If the WebSocket drops (laptop closes, network goes down):
- Daemon keeps running. Timeline keeps executing. Tasks fire on schedule.
- If the task needs a cloud LLM (Claude, Gemini) and there's no network: task marked `failed`, retried when connection returns.
- If the task can use a local model (ollama): executes normally. Fully air-gapped.
- Daily thread and memory keep updating locally.
- When connection returns: memory syncs, thread syncs, pending results delivered.

---

## wingthing.ai

Not a hosted agent. A **sync and relay layer.**

| What it does | How |
|---|---|
| **Memory sync** | Encrypted sync of `~/.wingthing/` across machines. Like iCloud for your AI context. |
| **Remote relay** | Routes tasks from phone/web to your daemon via WebSocket. |
| **Remote UI** | Web + PWA for submitting tasks, viewing timeline, reading daily thread. |
| **Orchestrator boost** | Optional cheap LLM calls for context routing when rule-based isn't enough. |
| **Thread viewer** | Read your daily thread from your phone. See what your AI has been doing. |

**What it does NOT do:**
- Does not run agents. Your machine does that.
- Does not store API keys. Your machine has those.
- Does not call LLM APIs on your behalf (except optional orchestrator boost with its own cheap model).
- Does not see your plaintext memory (E2E encrypted in transit and at rest).

### Pricing model

- **Free tier:** Sync + relay for one machine. Rate-limited.
- **Pro tier:** Multi-machine sync, priority relay, orchestrator boost, longer thread retention.
- **Self-relay:** Run your own relay server. Open source. wingthing.ai is convenience, not lock-in.

---

## Skills System

### 128 curated skills at wingthing.ai/skills

Not a marketplace. Not an open registry. **A hand-curated collection of 128 high-quality skills.**

Why 128, not 10,000:
- ClawHub has 3,000+ skills. 12% are malware. 28% are duplicates.
- Skills.sh has 45,000+. No verification. Popularity is the only signal.
- We hand-test every skill against wingthing's sandbox, memory, and timeline system.
- If it's on wingthing.ai/skills, it works and it's safe.

**Categories (target):**

| Category | Examples | Count |
|----------|---------|-------|
| Dev workflow | Jira briefing, PR review, deploy check, test runner | ~20 |
| Code | Refactor, debug, explain, migrate, generate tests | ~20 |
| Research | Web research, competitor analysis, paper summary | ~15 |
| Writing | Blog draft, email compose, meeting notes, changelog | ~15 |
| Ops | Server health, log analysis, incident response, backup check | ~15 |
| Data | CSV analysis, SQL query, dashboard summary, report | ~10 |
| Personal | Calendar briefing, todo review, reading list, habit tracker | ~15 |
| Meta | Memory maintenance, thread cleanup, skill test, cost report | ~8 |
| System | Agent install, sandbox test, sync check, config validate | ~10 |

### Skill format

Skills are markdown files with YAML frontmatter. Superset of the Agent Skills spec (SKILL.md) — compatible with the open standard, with wingthing-specific extensions.

```markdown
---
name: jira-briefing
description: Summarize current Jira sprint
agent: claude
isolation: network
mounts:
  - ~/repos/jira:ro
timeout: 120s
memory:
  - identity
  - projects
schedule: "0 8 * * 1-5"   # weekdays at 8am (becomes a recurring task)
tags: [work, jira, sprint]
thread: true               # append results to daily thread
---

# Jira Sprint Briefing

You are briefing {{identity.name}} on their current Jira sprint.

## Context
{{memory.projects}}

## Today So Far
{{thread.summary}}

## Instructions
1. Run `~/repos/jira/bin/sprint-current` to get the current sprint
2. Run `~/repos/jira/bin/my-tickets` to get assigned tickets
3. Summarize:
   - What's in progress
   - What's blocked
   - What needs attention today
4. Be concise. No headers. Most urgent first.

## Follow-ups
If there are blocked tickets, schedule a re-check:
<!-- wt:schedule delay=4h -->Re-check blocked tickets this afternoon<!-- /wt:schedule -->
```

**Key design:**
- Skills are files. Copy them, share them, version them.
- `{{memory.X}}` interpolates from memory files.
- `{{identity.X}}` interpolates from identity.md.
- `{{thread.summary}}` injects the daily thread summary.
- `agent:` which LLM to use (falls back to default if unavailable).
- `isolation:` sandbox level.
- `mounts:` what the sandbox can see.
- `schedule:` makes it a recurring task on the timeline.
- `memory:` declares which memory files to load (Layer 2 retrieval).
- `tags:` helps keyword matching for ad-hoc tasks (Layer 3 retrieval).
- `thread: true` appends a summary of the output to the daily thread.

### Installing skills

```bash
wt skill add jira-briefing                      # from wingthing.ai/skills
wt skill add ./my-custom-skill.md               # from local file
wt skill add https://example.com/skill.md       # from URL
wt skill list                                   # show installed skills
wt skill list --available                       # browse wingthing.ai/skills
```

---

## Why Not Gastown?

Gastown and wingthing look similar on the surface (Go binary, orchestrates agents) but solve different problems:

| | Gastown | Wingthing |
|---|---|---|
| **Scope** | One codebase | Your entire life |
| **Agents** | Persistent workers with roles (Mayor, Polecats, Witness, Deacon) | Stateless invocations. No roles. No hierarchy. |
| **Coordination** | Agents talk to each other | No agent-to-agent communication. Agents coordinate through structured memory. |
| **Session model** | Long-running multi-agent sessions ($100/hr) | Short, focused task executions. Pay per task. |
| **Multi-model** | Claude only | Claude, Gemini, Ollama, whatever has a CLI |
| **Offline** | No | Yes. Full functionality with local models. |
| **Interface** | IDE-like workspace | CLI + phone + web |
| **Memory** | Git-backed hooks, workspace state | Text files, human-readable, portable, synced |
| **Target user** | Developer managing a complex codebase | Anyone managing an AI-assisted life |

**The sharp difference:** Gastown coordinates agents working *together* on the same thing. Wingthing dispatches agents working *independently* on different things. Gastown agents know about each other. Wingthing agents only know about you.

---

## What Makes This Not OpenClaw

| | OpenClaw | Wingthing |
|---|---|---|
| **What it is** | AI assistant you chat with | AI daemon that runs tasks on your behalf |
| **Core abstraction** | Conversation | Task timeline + daily thread |
| **Interface** | Messaging apps (Telegram, WhatsApp) | CLI + HTTP API + phone (via relay) |
| **Agent model** | One agent, one session, long-running | Stateless task invocations, many agents, short-lived |
| **Multi-model** | Supports multiple, uses one at a time | Orchestrates across models per task |
| **Security** | Runs on host, broad permissions | Sandbox-first, container isolation |
| **Memory** | SQLite + embeddings | Text files, human-readable, git-diffable |
| **Continuations** | Heartbeat (30min timer, single session) | Timeline (agents schedule follow-ups at arbitrary times) |
| **Offline** | No | Yes. Local models, local memory, local execution. |
| **Cloud dependency** | None (but also no remote access) | Optional sync + relay. Never required. |
| **Skills** | ClawHub (3,000+, 12% malware) | 128 hand-curated, tested, signed |
| **Language** | Node.js (430k+ lines) | Go (single binary) |
| **Codebase** | 430k lines, 52 modules | Target: <10k lines, minimal deps |

**The one-liner:** OpenClaw is a brain you talk to. Wingthing is a hand that directs brains.

---

## What This Is NOT

- **Not a chat app.** You don't have conversations with wingthing. You submit tasks. It orchestrates agents. The daily thread makes it feel conversational, but every invocation is stateless.
- **Not an LLM.** Wingthing has no model. It's the harness.
- **Not an agent framework.** It doesn't define how agents work internally. It uses existing CLIs as black boxes.
- **Not a cloud service.** The daemon is local. wingthing.ai is optional sync + relay.
- **Not MCP.** MCP is about tools. Wingthing is about the human-agent relationship.
- **Not Gastown.** Gastown coordinates agents on a codebase. Wingthing dispatches agents across your life.
- **Not OpenClaw.** OpenClaw is the brain. Wingthing directs brains.

---

## Go Package Structure

```
wingthing/
├── cmd/
│   └── wt/
│       └── main.go              # CLI entrypoint
├── internal/
│   ├── daemon/                  # Daemon lifecycle, signal handling
│   ├── timeline/                # Task queue, scheduling, cron, execution loop
│   ├── thread/                  # Daily thread management, summarization
│   ├── memory/                  # Memory loading, indexing, retrieval layers
│   ├── orchestrator/            # Context builder: memory + thread + skill → prompt
│   ├── sandbox/                 # Container creation, isolation levels
│   │   ├── apple.go             # Apple Container backend
│   │   ├── oci.go               # Docker/Podman/runc backend
│   │   └── fallback.go          # Process-level isolation (no containers)
│   ├── agent/                   # LLM agent adapters
│   │   ├── claude.go            # claude -p adapter (streaming, session resume)
│   │   ├── gemini.go            # gemini CLI adapter
│   │   └── ollama.go            # ollama adapter
│   ├── transport/               # HTTP API, WebSocket, Unix socket
│   ├── sync/                    # Memory sync (wingthing.ai or git)
│   ├── skill/                   # Skill loading, template interpolation
│   └── parse/                   # Structured output parser (wt:schedule markers)
├── go.mod
└── go.sum
```

---

## CLI

```
wt                              # Interactive mode — submit tasks, see timeline
wt "do the thing"               # One-shot task (default agent, auto memory)
wt --skill jira                 # Run a named skill now
wt --agent ollama "summarize"   # Use specific agent
wt timeline                     # Show upcoming and recent tasks
wt timeline --watch             # Live timeline view
wt thread                       # Print today's daily thread
wt thread --yesterday           # Print yesterday's thread
wt memory                       # Open memory dir in $EDITOR
wt memory show                  # Print memory index
wt memory sync                  # Force sync now
wt agent list                   # List configured agents
wt agent install claude         # Install + configure an agent
wt skill list                   # List installed skills
wt skill add <name|file|url>    # Install a skill
wt status                       # Daemon status, connection state, timeline summary
wt log                          # Tail the task log
wt log --last --context         # Show last task's full constructed prompt
wt daemon                       # Start daemon (foreground)
wt daemon --install             # Install as system service (launchd/systemd)
wt login                        # Authenticate with wingthing.ai
```

---

## Open Questions

1. **Thread summarization.** When the daily thread gets too long for a prompt, how do we summarize? Options: truncate to last N entries, use a cheap local model, use wingthing.ai's orchestrator boost. Probably: keep last N entries verbatim, summarize older ones.

2. **Memory write permissions.** Can agents write to memory directly, or does wingthing always mediate? Probably: agents can write to a staging area, wingthing promotes to memory. Configurable per skill.

3. **Memory conflict resolution.** Two machines edit the same memory file. How does sync resolve conflicts? Options: last-write-wins, CRDT, manual merge. Probably: last-write-wins with conflict log for review.

4. **Multi-user (teams).** Shared memory, shared skills, shared thread. But v1 is single-user.

5. **Task dependencies.** Can tasks declare "don't run until task X completes"? Probably v2.

6. **Cost tracking.** Token usage per agent, per skill, per day. Where to store? SQLite counter file in `~/.wingthing/`. Surface in `wt status`.

7. **Prompt transparency.** Core principle: `wt log --last --context` shows the complete constructed prompt. No hidden system prompts. The human can always audit what wingthing sent.

8. **E2E encryption for sync.** What key management? Device keys established during `wt login`? User passphrase? Needs design.

9. **Rate limiting on relay.** Free tier needs limits to prevent abuse. Per-device token bucket?

10. **Structured output parsing.** The `<!-- wt:schedule -->` convention is simple but agents hallucinate. Parser must be defensive — malformed markers are logged and ignored, never create garbage tasks. Need a validation layer (is this a plausible task description? is the delay reasonable?).

---

## v0.1 Scope (MVP)

What ships first:

- [ ] Go binary with `wt` CLI
- [ ] Daemon mode (foreground)
- [ ] Memory system (load, index, interpolate, Layer 1-4 retrieval)
- [ ] Daily thread (create, append, read, inject into prompts)
- [ ] Orchestrator (rule-based context builder, no LLM needed)
- [ ] Timeline (task queue, scheduled execution, task history)
- [ ] Single agent adapter (Claude via `claude -p` with streaming)
- [ ] Skill system (load, interpolate, execute)
- [ ] `wt.schedule()` via structured output parsing
- [ ] Basic sandbox (Docker)
- [ ] `wt "task"` and `wt --skill name`
- [ ] `wt timeline`, `wt thread`, `wt status`, `wt log`
- [ ] Local transport only (Unix socket)

**Not in v0.1:** wingthing.ai, sync, remote access, recurring tasks, Apple Containers, cost tracking, PWA, ollama adapter.

**v0.2:**
- Recurring tasks (cron expressions on timeline)
- Ollama adapter (offline execution)
- Apple Container sandbox backend
- Thread summarization
- Cost tracking

**v0.3:**
- wingthing.ai MVP (sync + relay + web UI)
- Outbound WebSocket connection
- `wt login`, device auth
- Memory sync across machines
- PWA for phone access

**v0.4:**
- wingthing.ai/skills (curated skill registry)
- Gemini adapter
- E2E encryption for sync
- Task dependencies
- Multi-machine thread merge

---

## Name

**wingthing** — the thing that flies alongside you. Not a wingman (gendered, implies peer). Not a copilot (taken). A wingthing. It doesn't pretend to be human. It doesn't pretend to be smart. It's a thing with wings that carries your context and directs brains on your behalf.

**wingthing.ai** — sync and relay layer. Makes your daemon reachable from anywhere.

**wt** — the CLI. Short, fast, unix-y.
