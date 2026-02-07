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
├── timeline engine        ← SQLite-backed task queue, scheduling, execution loop
├── memory store           ← text files in ~/.wingthing/memory/, human-readable
├── daily thread           ← SQLite-backed, rendered to markdown for prompts
├── context builder        ← orchestrator that assembles prompts (pure Go)
├── store (wt.db)          ← single SQLite database for all runtime state
├── sandbox runtime        ← Apple Containers (macOS) / Linux sandboxing
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

A task is a row in `wt.db`:

```sql
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,        -- "t-20260206-001"
    type        TEXT NOT NULL DEFAULT 'prompt',  -- "prompt" or "skill"
    what        TEXT NOT NULL,           -- prompt text (type=prompt) or skill name (type=skill)
    run_at      DATETIME NOT NULL,       -- when to execute (UTC)
    agent       TEXT NOT NULL DEFAULT 'claude',
    isolation   TEXT NOT NULL DEFAULT 'standard',
    memory      TEXT,                    -- JSON array: ["identity", "projects"]
    parent_id   TEXT REFERENCES tasks(id),
    status      TEXT NOT NULL DEFAULT 'pending',  -- pending | running | done | failed
    cron        TEXT,                    -- cron expression for recurring tasks (NULL = one-shot)
    machine_id  TEXT,                    -- which machine created this
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at  DATETIME,
    finished_at DATETIME,
    output      TEXT,                    -- agent output (for history/audit)
    error       TEXT                     -- error message if failed
);

CREATE INDEX idx_tasks_status_run_at ON tasks(status, run_at);
```

SQLite is the runtime source of truth. The daemon loads pending tasks into memory on startup, but the database is authoritative. No file locking, no YAML parsing, concurrent reads are free, WAL mode handles the daemon's read-write pattern.

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

**Scheduled/recurring tasks** are just tasks with a `cron` column:

```sql
INSERT INTO tasks (id, what, run_at, agent, cron)
VALUES ('t-recurring-jira', '!skill jira-briefing', '2026-02-07T08:00:00Z', 'claude', '0 8 * * 1-5');
-- After each execution, daemon calculates next run_at from cron, inserts a new task
```

### 2. Daily Thread (Running Context)

The daily thread is a running log of everything that's happened today. It follows you across tasks and across machines (via sync).

**Storage:** Thread entries are rows in `wt.db`, rendered to markdown on demand:

```sql
CREATE TABLE thread_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT REFERENCES tasks(id),
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    machine_id  TEXT NOT NULL,
    agent       TEXT,
    skill       TEXT,                    -- NULL for ad-hoc tasks
    user_input  TEXT,                    -- what the user asked (NULL for scheduled)
    summary     TEXT NOT NULL,           -- the agent's output summary
    tokens_used INTEGER                  -- for cost tracking
);

CREATE INDEX idx_thread_timestamp ON thread_entries(timestamp);
```

**Rendered output** (what `wt thread` shows and what gets injected into prompts):

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

The markdown is rendered from rows, not stored as a flat file. This means:
- Entries from multiple machines interleave cleanly by timestamp (no merge conflicts)
- You can query by time range, agent, skill, or task lineage
- Summarization is a query: "give me the last N entries" or "give me entries from before noon, summarized"

**How it's used:**

The daily thread is injected into every task's prompt (or a summary of it, if it's getting long). This gives each agent awareness of what's already happened today — without persistent sessions. Every `claude -p` invocation is fresh, but it reads the thread and feels continuous.

**Why this matters:**

The user sends a message from their phone at 2pm: "did that deploy land?" They don't say which deploy. They don't say which PR. The orchestrator queries thread entries for today, sees the merge at 09:47, the staging deploy, the health check — and constructs a prompt with all that context. The agent answers coherently. To the user, it feels like a continuous conversation. Under the hood, it's a stateless invocation with injected context.

**Budget management (pre-1.0):**

The orchestrator manages a token budget when building prompts — thread entries, memory files, and skill templates all compete for space within the agent's context window. For v0.1: naive truncation (newest thread entries first, drop what doesn't fit, `len(rendered) < budget`). Real budget management — priority-based allocation, LLM-driven compaction, per-section caps — is pre-1.0 work and will be complex. Don't over-design it now.

**Retention:** Configurable (default: 7 days full entries, 30 days summaries, then deleted). Retention is a `DELETE FROM thread_entries WHERE timestamp < ?` — trivial with SQLite.

### 3. Memory (Text-Based Persistence)

Memory is **the** differentiator. Not a database. Not embeddings. Not a vector store. **Text files.**

```
~/.wingthing/
├── wt.db                 # SQLite — timeline, thread, task log, agent health, config
├── memory/
│   ├── index.md          # Master index — always loaded, TOC with one-line summaries
│   ├── identity.md       # Who the human is, how they work
│   ├── projects.md       # Active projects, status, context
│   ├── machines.md       # Machine registry (hostname, role, capabilities)
│   ├── relationships.md  # People, teams, how to interact
│   └── [topic].md        # Any topic — grows organically
├── skills/
│   ├── jira-briefing.md  # Skill definitions (prompt templates + metadata)
│   ├── pr-review.md
│   └── [skill].md
└── config.yaml           # Global config (default agent, sync settings, machine ID)
```

**Two storage layers, clear boundary:**

| What | Where | Why |
|------|-------|-----|
| **Memory** (identity, projects, etc.) | Text files in `memory/` | Human-readable, human-editable, git-diffable |
| **Skills** (prompt templates) | Markdown files in `skills/` | Shareable, versionable, inspectable |
| **Config** | `config.yaml` | Human-editable, one file |
| **Everything else** (timeline, thread, task log, agent state) | `wt.db` (SQLite) | Concurrent access, queries, migrations, no file-locking |

**Memory design principles:**

- **Human-readable.** You can open any file in a text editor and understand it.
- **Human-editable.** You can modify memory by hand. No migration, no schema, no rebuild.
- **Git-diffable.** Memory changes are visible in `git diff`. You can version your memory.
- **Portable.** Memory + skills + config are just files. Copy them. The SQLite DB is regenerated on first run if missing (tasks are ephemeral, memory is durable).
- **Syncable.** wingthing.ai syncs memory files and the SQLite DB separately. Memory = rsync/git. DB = row-level sync with conflict resolution by timestamp + machine_id.

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

**Layer 2: Skill-declared deps.** Each skill specifies which memory files it needs. The Jira briefing skill declares `memory: [identity, projects]`. These are always loaded (the floor). Layers 3+ can add more files but never subtract skill-declared deps. Covers 80% of tasks.

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

#### Memory Writes

Agents can produce durable memory via structured output markers:

```
<!-- wt:memory file="research-backup-competitors" -->
## Competitor Analysis
- Company A: ...
- Company B: ...
<!-- /wt:memory -->
```

Wingthing parses these post-execution and writes to `memory/research-backup-competitors.md`. Same pattern as `wt:schedule` — convention-based, works with any LLM, parsed defensively.

Skills can declare `memory_write: true` to enable this. Skills without the flag have their `wt:memory` markers ignored (defense against prompt injection producing rogue memory writes).

#### Inter-Task Communication Through Memory

Agents don't talk to each other. They communicate through structured memory, mediated by wingthing.

```
Task A (Claude): "Research competitors in the backup space"
  → Agent runs, produces output with <!-- wt:memory --> markers
  → Wingthing writes results to memory/research-backup-competitors.md

Task B (Gemini, scheduled 5 min later): "Draft a positioning doc"
  → Skill declares: memory: [identity, projects, research-backup-competitors]
  → Agent sees Task A's output as memory context
  → Agent produces positioning doc
```

This is mediated communication, not direct agent-to-agent messaging. Wingthing controls what gets written, what gets read, and which tasks can see which memory. The agents never know about each other.

### 4. Orchestrator (The Context Builder)

The orchestrator is the brain that assembles each task's prompt. It runs locally, in pure Go, with optional LLM boost.

**What the orchestrator does for every task:**

1. Read the task (what the human asked, or what skill to run)
2. Resolve config: skill frontmatter > agent config > `config.yaml` defaults (precedence chain)
3. Select the agent → look up its context window size from agent config in `wt.db`
4. Compute token budget: `agent_context_window - task_prompt - overhead_margin`
5. Read the memory index (always loaded, ~50 lines)
6. Load skill-declared memory files (Layer 2, always)
7. Load keyword-matched memory files (Layer 3, additive)
8. Render daily thread entries within remaining budget (Layer 4, newest-first with truncation)
9. If using a skill: interpolate the template (`{{memory.X}}`, `{{thread.summary}}`, `{{identity.X}}`)
10. Construct the full prompt with: identity + memory + daily thread + task + `wt:schedule` format docs
11. Hand off to the sandbox for execution

**Config precedence:** skill frontmatter > agent config > `config.yaml`. A skill that says `agent: gemini` overrides the global default. A skill that says `isolation: network` overrides the agent default. This is documented once and applies everywhere.

**Token budget:** The orchestrator does naive character-based estimation (1 token ~ 4 chars). It doesn't need to be precise — the goal is staying well under the limit, not hitting it exactly. Each prompt section has a priority: task prompt (must include) > identity (must include) > skill-declared memory (must include) > recent thread (important) > keyword memory (nice to have) > older thread (expendable). If the budget is tight, lower-priority sections get truncated or dropped.

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
│         sandbox               │  ← untrusted agent execution
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

### Structured output conventions

Agents communicate back to wingthing via HTML comment markers in their output. These are universal — every agent, every LLM, every skill gets the format docs appended to the prompt automatically by the orchestrator.

**Scheduling follow-up tasks:**
```
<!-- wt:schedule delay=10m -->Check build status for PR #892<!-- /wt:schedule -->
<!-- wt:schedule at=2026-02-06T17:00:00Z -->Send EOD summary<!-- /wt:schedule -->
```

**Writing to memory:**
```
<!-- wt:memory file="research-competitors" -->Content here<!-- /wt:memory -->
```

Wingthing parses these from the agent's output post-execution. No socket, no API, no MCP needed. Works with any LLM that can follow output format instructions (all of them).

**Parsing rules:**
- Malformed markers are logged to `wt.db` task log and ignored — never create garbage tasks or corrupt memory
- `delay` values capped at configurable max (default 24h) — prevents agents from scheduling tasks years out
- `wt:memory` only honored when skill declares `memory_write: true`
- Marker format docs (~10 lines) are appended to every prompt by the orchestrator — trivial token cost
- `wt:schedule` does not support memory declarations for follow-up tasks — the follow-up inherits the default agent and gets its memory from normal orchestrator retrieval. Explicit memory control on scheduled tasks is a v0.2 feature

### Implementation

- **macOS:** Apple Containers (lightweight Linux VMs on Apple Silicon). Ships with macOS 26+, no Docker dependency.
- **Linux:** Namespace + seccomp sandboxing (landlock where available). Native kernel features, no Docker dependency.
- **Fallback:** Process-level isolation (restricted PATH, tmpdir jail, rlimits). Used when platform sandboxing is unavailable.

### Local models in sandboxes

Sandboxed agents need to reach a local ollama instance. Solution: expose ollama's port into the sandbox (port forwarding on macOS Apple Containers, network namespace allowlisting on Linux). Ollama is trusted infrastructure (like a database), the agent is not. The sandbox gets network access to `localhost:11434` and nothing else.

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
    Health() error           // probe: is this agent available right now?
    ContextWindow() int      // max tokens for prompt construction budget
}
```

**Agent health:** Before building a prompt and spinning up a sandbox, the orchestrator calls `Health()`. Claude adapter runs `claude --version`. Ollama adapter checks `ollama list`. If the selected agent is unhealthy, the task fails immediately with a clear error — no wasted sandbox startup. `wt agent list` shows health status. Agent health is cached in `wt.db` with a short TTL (default 60s).

**Agent config** lives in `wt.db`, not YAML files:

```sql
CREATE TABLE agents (
    name            TEXT PRIMARY KEY,    -- "claude", "ollama-llama3", "gemini"
    adapter         TEXT NOT NULL,       -- "claude", "ollama", "gemini" (which Go adapter)
    command         TEXT NOT NULL,       -- "claude", "ollama run llama3.2", etc.
    context_window  INTEGER NOT NULL DEFAULT 200000,
    default_isolation TEXT DEFAULT 'standard',
    healthy         BOOLEAN DEFAULT 0,
    health_checked  DATETIME,
    config_json     TEXT                 -- adapter-specific config (model, flags, etc.)
);
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

**Cursor CLI** (`cursor -p`) — Proprietary, but headless mode works.

- `cursor -p "prompt"` with `--output-format stream-json` — same interface pattern as Claude
- `--force` allows direct file changes without confirmation
- `--stream-partial-output` for incremental deltas
- Auth: user's own Cursor subscription
- TOS note: Cursor TOS prohibits "derivative works" and "distribution" — but running `cursor -p` as a subprocess is neither. Wingthing doesn't redistribute Cursor, extract tokens, or wrap their UI. It's the same as a shell script invoking `cursor -p`. The user installed it, the user runs it.

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

**Amp** (`amp -x`) — Sourcegraph's coding agent (formerly Cody). Rebranded July 2025.

- `amp -x "prompt"` for headless execute mode — sends prompt, waits for agent turn, prints result, exits
- `--stream-json` for streaming JSON output
- Piped input: `echo "prompt" | amp -x`
- API key auth (`AMP_API_KEY`) for CI/CD / headless environments
- MCP support: `--mcp-config` for tool server injection
- Active development, growing user base

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
| **Windsurf** | No headless mode. No CLI. IDE-only. Community "windsurfinabox" hack uses fake X11 — not viable. |
| **Tabnine** | IDE plugin only. Has a PR-review docker image but no headless coding agent CLI. |

**TOS clarification:** Wingthing runs the user's own CLIs as subprocesses — same as a Makefile, a CI pipeline, or a shell script. "Derivative work" means embedding, redistributing, or wrapping another product's code. Subprocess invocation is not that. The line we don't cross: never extract credentials, never spoof client identity, never redistribute binaries, never market wingthing as "Cursor/Claude/Codex inside."

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
  - $JIRA_DIR:ro             # resolved from config.yaml vars (not hardcoded paths)
timeout: 120s
memory:
  - identity
  - projects
memory_write: false           # this skill can't write to memory
schedule: "0 8 * * 1-5"      # weekdays at 8am (becomes a recurring task)
tags: [work, jira, sprint]
thread: true                  # append results to daily thread
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
- `mounts:` what the sandbox can see. **Uses config variables** (`$JIRA_DIR`, `$PROJECT_ROOT`) resolved from `config.yaml`, not hardcoded paths. This makes registry skills portable across machines.
- `schedule:` makes it a recurring task on the timeline.
- `memory:` declares which memory files to load (Layer 2 retrieval). Additive floor — orchestrator layers 3+ can add more, never subtract.
- `memory_write:` whether this skill's `wt:memory` markers are honored (default false).
- `tags:` helps keyword matching for ad-hoc tasks (Layer 3 retrieval).
- `thread: true` appends a summary of the output to the daily thread.

### Template Interpolation

Skills use `{{...}}` markers that the orchestrator resolves before prompt construction:

| Pattern | Resolves to |
|---------|-------------|
| `{{memory.X}}` | Body of `memory/X.md`, frontmatter stripped |
| `{{identity.name}}` | `name` field from `memory/identity.md` frontmatter |
| `{{identity.X}}` | Field `X` from `memory/identity.md` frontmatter |
| `{{thread.summary}}` | Rendered daily thread output (from budget function) |
| `{{task.what}}` | The task's `what` column (the original prompt or skill name) |

**Rules:**
- Missing files → empty string, logged as warning
- Missing frontmatter fields → empty string, logged as warning
- Unrecognized `{{...}}` patterns → left as-is (not stripped, not errored)
- Frontmatter = YAML between `---` fences. Body = everything after the closing `---`.

### Standard Config Variables

Skills from wingthing.ai/skills use config variables in `mounts:` for portability. These are resolved from `config.yaml`:

| Variable | Default | Example |
|----------|---------|---------|
| `$HOME` | User's home directory | `$HOME/.ssh:ro` |
| `$PROJECT_ROOT` | Current working directory | `$PROJECT_ROOT:rw` |
| `$WINGTHING_DIR` | `~/.wingthing` | `$WINGTHING_DIR/memory:ro` |

User-defined variables (like `$JIRA_DIR`) go in `config.yaml` under `vars:`. Registry skills document which variables they need; `wt skill add` warns if a required variable is unset.

### Installing skills

```bash
wt skill add jira-briefing                      # from wingthing.ai/skills
wt skill add ./my-custom-skill.md               # from local file
wt skill add https://example.com/skill.md       # from URL
wt skill list                                   # show installed skills
wt skill list --available                       # browse wingthing.ai/skills
```

---

## Bootstrap: Agent-Driven Install

**`wingthing.ai/install.md`** — Agent-readable instruction set. Any coding agent (Claude Code, Cursor, Codex, Goose) can read this and install wingthing on the user's machine.

**`wingthing.ai/install.sh`** — Shell script that does the mechanical work. The agent can run it, or the human can run it directly.

```
User tells their agent: "install wingthing"
    → Agent fetches wingthing.ai/install.md
    → install.md tells the agent:
        1. Check prerequisites (Go 1.22+, platform sandbox auto-detected)
        2. Run: curl -fsSL wingthing.ai/install.sh | sh
        3. Run: wt init (creates ~/.wingthing/, seeds identity.md template, inits wt.db)
        4. Run: wt agent add claude (detects claude CLI, adds to agents table)
        5. Edit ~/.wingthing/memory/identity.md with user's info
        6. Test: wt "hello" (one-shot task to verify everything works)
```

**Why both files?**
- `install.sh` is for humans who don't need an agent to run `curl | sh`
- `install.md` is for agents who need to understand what wingthing is, what it does, and how to configure it for this specific user. The agent reads the instruction set, runs the script, then personalizes the setup (fills in identity.md, detects installed agents, suggests skills).

**Self-install is the onboarding.** The user doesn't read docs. They tell their existing agent "install wingthing" and the agent bootstraps everything. The agent IS the installer. This is wingthing's first impression — if the agent can install it cleanly, the user trusts it.

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
| **Security** | Runs on host, broad permissions | Sandbox-first, platform isolation |
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
│       └── main.go              # CLI entrypoint (cobra or raw flag parsing)
├── internal/
│   ├── daemon/                  # Daemon lifecycle, signal handling, graceful shutdown
│   ├── store/                   # SQLite: schema, migrations, queries (wt.db)
│   │   ├── store.go             # Open, Close, WAL mode, migration runner
│   │   ├── migrations/          # Embedded SQL migration files (embed.FS)
│   │   ├── tasks.go             # Task CRUD, timeline queries
│   │   ├── thread.go            # Thread entry CRUD, rendering to markdown
│   │   ├── agents.go            # Agent config CRUD, health cache
│   │   └── log.go               # Task log append, query
│   ├── timeline/                # Execution loop: poll pending tasks, dispatch
│   ├── thread/                  # Thread rendering, token budget truncation
│   ├── memory/                  # Memory loading, indexing, retrieval layers
│   ├── orchestrator/            # Context builder: memory + thread + skill → prompt
│   ├── sandbox/                 # Platform sandboxing, isolation levels
│   │   ├── apple.go             # Apple Containers backend (macOS)
│   │   ├── linux.go             # Namespace + seccomp backend (Linux)
│   │   └── fallback.go          # Process-level isolation (no platform sandbox)
│   ├── agent/                   # LLM agent adapters
│   │   ├── adapter.go           # Agent interface definition
│   │   └── claude.go            # claude -p adapter (streaming, health, context window)
│   ├── transport/               # Unix socket API (HTTP over UDS)
│   ├── skill/                   # Skill loading, frontmatter parsing, template interpolation
│   └── parse/                   # Structured output parser (wt:schedule, wt:memory markers)
├── go.mod
└── go.sum
```

**v0.1 packages only.** Ollama/gemini adapters, sync client, and WebSocket transport are added in later versions. Start lean.

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

## Resolved Design Decisions

These were open questions, now resolved:

1. **Thread summarization.** → Token budget model in orchestrator. Last 3 entries verbatim, fill remaining budget newest-first, older entries become one-line summaries. Pure Go string accounting, no model call in v0.1.

2. **Memory write permissions.** → `wt:memory` structured output markers, gated by `memory_write: true` in skill frontmatter. Default false. Wingthing always mediates — agents never write to disk directly.

3. **Structured output parsing.** → Defensive parser: malformed markers logged and ignored, `delay` capped at 24h, `wt:memory` requires skill opt-in. Validation is structural (parseable delay, non-empty content), not semantic.

4. **Config precedence.** → Skill frontmatter > agent config > `config.yaml` defaults. Documented once, applied everywhere.

5. **Cost tracking.** → `tokens_used` column on `thread_entries` table. Agents report via adapter's token reporting. `wt status` shows daily/weekly totals. SQLite makes aggregation trivial.

6. **Prompt transparency.** → Core principle, not a question. `wt log --last --context` shows the complete constructed prompt. Logged to `wt.db` task log.

## Open Questions

1. **Memory conflict resolution.** Two machines edit the same memory file. How does sync resolve conflicts? Options: last-write-wins, CRDT, manual merge. Probably: last-write-wins with conflict log. v0.3 problem — memory sync is not in v0.1.

2. **Multi-user (teams).** Shared memory, shared skills, shared thread. But v1 is single-user.

3. **Task dependencies.** Can tasks declare "don't run until task X completes"? Probably v0.4. The `tasks` table has `parent_id` for lineage but no blocking semantics yet.

4. **E2E encryption for sync.** What key management? Device keys established during `wt login`? User passphrase? Needs design. v0.3 problem.

5. **Rate limiting on relay.** Free tier needs limits to prevent abuse. Per-device token bucket? v0.3 problem.

---

## Error Handling

**Task failure taxonomy:**

| Error | What happens |
|-------|-------------|
| Agent unhealthy (Health() fails) | Task fails immediately, logged. `wt status` shows agent down. |
| Sandbox fails to start | Task fails, logged with sandbox error. Suggest fallback in log. |
| Agent times out | Container killed at TTL. Task marked `failed`, error = "timeout after Xs". |
| Agent returns empty | Task marked `failed`, error = "empty output". |
| Agent returns garbage (no parseable content) | Task marked `done` (the output IS the result, even if bad). Garbage `wt:schedule` markers ignored. |
| Network error mid-stream | Partial output captured. Task marked `failed`, error = "stream interrupted". |
| Disk full | Daemon logs error, stops accepting new tasks. `wt status` shows disk warning. |

**No automatic retries in v0.1.** Failed tasks are logged. The human can re-run with `wt retry <task-id>`. Automatic retry policies are v0.2.

## Graceful Shutdown

On SIGTERM or SIGINT:
1. Stop accepting new tasks from the timeline
2. Wait for running task to complete (up to 30s grace period)
3. If grace period exceeded, kill the sandbox
4. Flush any pending thread entries to `wt.db`
5. Close SQLite connection cleanly (WAL checkpoint)
6. Exit 0

The timeline persists in SQLite. Pending tasks survive daemon restart. A task that was `running` when the daemon died is marked `failed` with error = "daemon shutdown" on next startup.

## Task Log

Every task execution is logged to `wt.db` for full auditability:

```sql
CREATE TABLE task_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT REFERENCES tasks(id),
    timestamp   DATETIME DEFAULT CURRENT_TIMESTAMP,
    event       TEXT NOT NULL,   -- "started", "prompt_built", "agent_called", "output_received",
                                 -- "markers_parsed", "thread_appended", "completed", "failed"
    detail      TEXT             -- full prompt (for prompt_built), error message (for failed), etc.
);
```

`wt log --last --context` queries this: find the most recent task, pull the `prompt_built` event, display the full constructed prompt. Complete transparency.

---

## Trace: `wt "did that deploy land?"`

End-to-end code path from CLI to output. Every package touches exactly one step.

```
cmd/wt/main.go
  → parse args: no --skill flag, bare string = type "prompt"
  → connect to daemon via Unix socket (transport/)
  → submit task

internal/transport/
  → receive task submission over UDS
  → INSERT INTO tasks (id, type, what, run_at, agent, status)
     VALUES ('t-...', 'prompt', 'did that deploy land?', now(), 'claude', 'pending')

internal/timeline/
  → execution loop: SELECT * FROM tasks WHERE status='pending' AND run_at <= now()
  → picks up task, UPDATE status='running', started_at=now()

internal/agent/
  → Health() on claude adapter (claude --version, cached 60s in wt.db)
  → ContextWindow() → 200000

internal/orchestrator/
  → resolve config: no skill → agent config → config.yaml defaults
  → compute budget: 200000 - len(task.what) - overhead
  → load memory/index.md (always)
  → load memory/identity.md (always for ad-hoc)
  → keyword match "deploy" → memory/projects.md has tag "deploy" → load it
  → render daily thread (newest entries first, within remaining budget)
  → assemble: identity + memory + thread + task.what + wt:schedule/wt:memory format docs

internal/sandbox/
  → create sandbox (Apple Container on macOS, namespace sandbox on Linux)
  → isolation=standard (default), network=yes (claude needs API access)
  → no mounts (ad-hoc task, no skill mounts)

internal/agent/claude.go
  → claude -p "<assembled prompt>" --output-format stream-json
  → stream output back to daemon

internal/parse/
  → scan output for <!-- wt:schedule --> → create follow-up tasks in wt.db
  → scan output for <!-- wt:memory --> → ignored (no skill, no memory_write flag)

internal/store/thread.go
  → INSERT INTO thread_entries (task_id, timestamp, machine_id, agent, user_input, summary)

internal/store/log.go
  → INSERT INTO task_log for each event (started, prompt_built, agent_called, completed)

internal/store/tasks.go
  → UPDATE tasks SET status='done', finished_at=now(), output='...'

internal/timeline/
  → loop: check for next pending task
```

---

## v0.1 Scope (MVP)

What ships first:

- [ ] Go binary with `wt` CLI
- [ ] `wt.db` SQLite store (tasks, thread_entries, agents, task_log tables + migrations)
- [ ] Daemon mode (foreground, graceful shutdown)
- [ ] Memory system (load from `memory/`, index, interpolate, Layer 1-4 retrieval)
- [ ] Daily thread (SQLite-backed, render to markdown, inject into prompts with token budget)
- [ ] Orchestrator (rule-based context builder, token budget, config precedence)
- [ ] Timeline (SQLite task queue, scheduled execution, execution loop)
- [ ] Single agent adapter (Claude via `claude -p` with streaming + health check)
- [ ] Skill system (load, interpolate, execute, `memory_write` gating)
- [ ] Structured output parsing (`wt:schedule`, `wt:memory`)
- [ ] `wt init` (create `~/.wingthing/`, seed templates, init `wt.db`)
- [ ] Platform sandbox (Apple Containers on macOS, namespace+seccomp on Linux, process fallback)
- [ ] `wt "task"` and `wt --skill name`
- [ ] `wt timeline`, `wt thread`, `wt status`, `wt log`, `wt agent list`
- [ ] Local transport only (Unix socket)
- [ ] Task log with full prompt audit

**Not in v0.1:** wingthing.ai, sync, remote access, recurring tasks, PWA, ollama adapter, automatic retries, smart budget management.

**v0.2:**
- Recurring tasks (cron expressions on timeline)
- Ollama adapter (offline execution)
- Automatic retry policies
- Cost tracking via `tokens_used`
- `wt:schedule` with memory declarations for follow-up tasks

**v0.3:**
- wingthing.ai MVP (sync + relay + web UI)
- Outbound WebSocket connection
- `wt login`, device auth
- Memory sync across machines
- SQLite row-level sync for thread/timeline
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
