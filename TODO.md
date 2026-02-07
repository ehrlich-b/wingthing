# TODO — wingthing v0.1

Reference: [DRAFT.md](DRAFT.md) for full design. This file is the build plan.

## Parallelization

Tasks marked **[parallel]** within a phase can be developed simultaneously using git worktrees. Each gets its own branch, merged to main when the phase completes.

```bash
# Example: create Phase 2 worktrees after Phase 1 merges
git worktree add ../wt-memory -b wt/memory
git worktree add ../wt-agent -b wt/agent
git worktree add ../wt-parse -b wt/parse
git worktree add ../wt-skill -b wt/skill
git worktree add ../wt-sandbox -b wt/sandbox

# Merge when phase completes
git checkout main && git merge wt/memory wt/agent wt/parse wt/skill wt/sandbox

# Clean up
git worktree remove ../wt-memory  # etc
```

## Dependency Graph

```
Phase 1: scaffold + store/  + config loading
    |
    ├─────────┬──────────┬──────────┬──────────┐
Phase 2:  memory/    agent/     parse/     skill/     sandbox/     ← 5 parallel worktrees
    |
    ├──────────────┐
Phase 3:  thread/        orchestrator/                              ← 2 parallel worktrees
    |
    ├──────────────┐
Phase 4:  timeline/      transport/                                 ← 2 parallel worktrees
    |
Phase 5:  daemon/ + cmd/wt/ (CLI)                                  ← sequential on main
```

---

## Phase 1: Foundation

**Branch:** `main` (direct commits — everything depends on this)

- [ ] `go mod init github.com/ehrlich-b/wingthing`
- [ ] Add dependencies: `modernc.org/sqlite` (pure Go, no CGO), `github.com/spf13/cobra` (CLI)
- [ ] Create full directory structure per DRAFT.md "Go Package Structure"
- [ ] Stub all packages with package declarations
- [ ] `cmd/wt/main.go` — cobra skeleton with all subcommands defined, each returns "not implemented"
- [ ] `internal/config/config.go` — Load `~/.wingthing/config.yaml`, resolve `$WINGTHING_DIR`, `$HOME`, `$PROJECT_ROOT`, user-defined vars. Needed by skill/, orchestrator/, agent/ in Phase 2+.
- [ ] `internal/store/store.go` — Open, Close, WAL mode, `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`, embedded migration runner
- [ ] `internal/store/migrations/001_init.sql` — All 4 tables: tasks (with type column), thread_entries, agents, task_log. Indexes. Exact schemas from DRAFT.md.
- [ ] `internal/store/tasks.go` — Create, Get, ListPending(now), ListRecent(n), UpdateStatus, SetOutput, SetError
- [ ] `internal/store/thread.go` — Append, ListByDate(date), ListRecent(n), DeleteOlderThan(timestamp)
- [ ] `internal/store/agents.go` — Upsert, Get, List, UpdateHealth(name, healthy, checkedAt)
- [ ] `internal/store/log.go` — Append(taskID, event, detail), ListByTask(taskID)
- [ ] Tests for all store CRUD — table-driven, in-memory SQLite

**Done when:** `go build ./cmd/wt` compiles. `wt help` shows subcommands. Store tests pass. Migration creates all 4 tables with correct schemas.

---

## Phase 2: Independent Packages

All 5 packages have zero cross-dependencies. Each gets a worktree branched from main after Phase 1 merges.

### [parallel] memory/ — Memory System

**Branch:** `wt/memory` | **Worktree:** `../wt-memory`
**Package:** `internal/memory/`
**DRAFT.md ref:** "Memory (Text-Based Persistence)", "Memory Retrieval (Without RAG)"

- [ ] `memory.go` — Load memory directory, parse YAML frontmatter (between `---` fences), separate body from frontmatter
- [ ] `index.go` — Load and cache `index.md` content (always-loaded, Layer 1)
- [ ] `retrieval.go` — Given a task prompt and skill-declared deps, return ordered list of memory file contents:
  - Layer 1: index.md (always)
  - Layer 2: skill-declared deps by name (always, additive floor)
  - Layer 3: keyword match task prompt against frontmatter `tags` and markdown headings
  - Layer 4: placeholder — thread injection point, handled by orchestrator
- [ ] All returned content has frontmatter stripped (body only)
- [ ] Missing files → empty string + warning (never error)
- [ ] Tests: sample memory/ dir fixture, verify each retrieval layer, missing file handling

**Done when:** Given a `memory/` directory path, a task prompt string, and a list of skill-declared deps, returns the correct memory contents in priority order.

---

### [parallel] agent/ — Agent Adapters

**Branch:** `wt/agent` | **Worktree:** `../wt-agent`
**Package:** `internal/agent/`
**DRAFT.md ref:** "Agent Adapters", claude -p invocation, streaming, Health(), ContextWindow()

- [ ] `adapter.go` — Interface definition:
  ```go
  type Agent interface {
      Run(ctx context.Context, prompt string, opts RunOpts) (Stream, error)
      Health() error
      ContextWindow() int
  }
  type RunOpts struct {
      AllowedTools []string
      SystemPrompt string
      Timeout      time.Duration
  }
  ```
- [ ] `stream.go` — Stream type: iterator over output chunks. Collects full output. Supports cancellation via context.
- [ ] `claude.go` — Claude adapter:
  - `Run`: shell out to `claude -p "<prompt>" --output-format stream-json --verbose`, parse stream-json events, emit text deltas
  - `Health`: run `claude --version`, check exit code
  - `ContextWindow`: return configured value (default 200000)
- [ ] Tests: parse sample `stream-json` output (fixture), health check against mock, stream collection

**Done when:** Can construct a `claude -p` invocation, parse its streaming output into text, report health, and report context window size.

---

### [parallel] parse/ — Structured Output Parser

**Branch:** `wt/parse` | **Worktree:** `../wt-parse`
**Package:** `internal/parse/`
**DRAFT.md ref:** "Structured output conventions", parsing rules

- [ ] `parse.go` — Scan agent output string for `<!-- wt:schedule ... -->...<!-- /wt:schedule -->` and `<!-- wt:memory ... -->...<!-- /wt:memory -->` markers
- [ ] `schedule.go` — ScheduleDirective struct: `Delay time.Duration`, `At time.Time`, `Content string`. Parse `delay=10m` and `at=2026-...` attributes. Cap delay at 24h.
- [ ] `memory.go` — MemoryDirective struct: `File string`, `Content string`. Parse `file="name"` attribute.
- [ ] Return `[]ScheduleDirective`, `[]MemoryDirective`, `[]Warning` — never panic, never error on bad input
- [ ] Malformed markers: log warning, skip entirely (don't partially parse)
- [ ] Tests: valid markers, malformed markers, nested markers (ignore inner), missing attributes, empty content, delay exceeds cap, mixed valid+invalid in same output

**Done when:** Given raw agent output text, extracts all valid schedule and memory directives with warnings for anything malformed.

---

### [parallel] skill/ — Skills System

**Branch:** `wt/skill` | **Worktree:** `../wt-skill`
**Package:** `internal/skill/`
**DRAFT.md ref:** "Skills System", "Skill format", "Template Interpolation", "Standard Config Variables"

- [ ] `skill.go` — Load skill markdown file, parse YAML frontmatter into Skill struct: name, description, agent, isolation, mounts, timeout, memory ([]string), memory_write (bool), schedule, tags, thread (bool). Body is the prompt template.
- [ ] `interpolate.go` — Resolve template markers in skill body:
  - `{{memory.X}}` → body of memory file X (passed in as map[string]string)
  - `{{identity.X}}` → frontmatter field X from identity.md (passed in as map[string]string)
  - `{{thread.summary}}` → rendered thread string (passed in)
  - `{{task.what}}` → task prompt (passed in)
  - Missing → empty string + warning. Unrecognized `{{...}}` → left as-is.
- [ ] `vars.go` — Resolve `$VAR` in mount paths using config vars map
- [ ] Tests: load sample skill fixture, interpolate with mock data, missing fields, unrecognized patterns, variable resolution

**Done when:** Given a skill file path and a data map (memory contents, identity fields, thread, task), returns the fully interpolated prompt string plus the Skill metadata struct.

---

### [parallel] sandbox/ — Sandbox Runtime

**Branch:** `wt/sandbox` | **Worktree:** `../wt-sandbox`
**Package:** `internal/sandbox/`
**DRAFT.md ref:** "Sandbox Model", isolation levels, implementation

- [ ] `sandbox.go` — Interface:
  ```go
  type Sandbox interface {
      Exec(ctx context.Context, name string, args []string) (*exec.Cmd, error)
      Destroy() error
  }
  type Config struct {
      Isolation  Level  // strict, standard, network, privileged
      Mounts     []Mount
      Timeout    time.Duration
  }
  ```
  Factory function: `New(cfg Config) (Sandbox, error)` — auto-detects platform, returns appropriate backend.
- [ ] `levels.go` — Isolation level enum and what each permits (fs access, network, TTL)
- [ ] `apple.go` — Apple Containers backend (macOS). Detect via `sw_vers` or `container` CLI availability. Create lightweight VM, configure mounts + network per isolation level.
- [ ] `linux.go` — Namespace + seccomp backend. `clone()` with restricted namespaces, seccomp filter, optional landlock.
- [ ] `fallback.go` — Process-level: restricted PATH, tmpdir working dir, rlimits. Logs warning on creation ("no platform sandbox available").
- [ ] Tests: fallback sandbox executes `echo hello` in restricted env, isolation level config parsing

**Done when:** `sandbox.New()` returns a platform-appropriate sandbox. Fallback works everywhere. Apple/Linux backends compile on their respective platforms.

---

## Phase 3: Integration Packages

Depend on Phase 1 (store/) and Phase 2 package interfaces. Two parallel worktrees.

### [parallel] thread/ — Daily Thread Rendering

**Branch:** `wt/thread` | **Worktree:** `../wt-thread`
**Package:** `internal/thread/`
**DRAFT.md ref:** "Daily Thread (Running Context)", rendered output format, budget management
**Imports:** store/

- [ ] `render.go` — Query store for today's thread_entries, render to markdown matching DRAFT.md format:
  ```
  ## HH:MM — Summary [agent, skill]
  > User: "original prompt"
  - Agent output summary
  ```
- [ ] `budget.go` — Naive v0.1 truncation: include entries newest-first, drop what exceeds budget. `len(rendered) < budget` character check.
- [ ] Tests: render sample entries, budget truncation drops oldest first, empty day returns empty string

**Done when:** Given a store and a token budget (int, chars), returns rendered markdown for today's thread that fits within budget.

---

### [parallel] orchestrator/ — Context Builder

**Branch:** `wt/orchestrator` | **Worktree:** `../wt-orchestrator`
**Package:** `internal/orchestrator/`
**DRAFT.md ref:** "Orchestrator (The Context Builder)", 11-step process, config precedence, "Trace" section
**Imports:** store/, memory/, thread/, skill/, agent/ (ContextWindow), config/

- [ ] `build.go` — The full prompt assembly pipeline (see DRAFT.md Trace section for exact flow):
  1. Read task from store (type=prompt or type=skill)
  2. Resolve config: if skill → load skill frontmatter; merge with agent config; merge with config.yaml defaults
  3. Look up agent → get ContextWindow()
  4. Compute budget: context_window - len(task.what) - overhead_margin
  5. Load memory: index.md (always) + identity.md (always for ad-hoc) + skill-declared memory + keyword-matched memory
  6. If skill: interpolate template with memory, identity, thread, task data
  7. Render daily thread within remaining budget
  8. Assemble final prompt: identity + memory + thread + task/skill prompt + structured output format docs
  9. Return assembled prompt + metadata (which memory loaded, budget used, config source)
- [ ] `formatdocs.go` — The ~10 line wt:schedule + wt:memory format reference appended to every prompt
- [ ] `config.go` — Config precedence resolver: skill frontmatter fields override agent config override config.yaml defaults
- [ ] Tests: build prompt for ad-hoc task, build prompt for skill task, config precedence, memory selection

**Done when:** Given a task ID, produces a fully assembled prompt string and metadata about what was included. The Trace section in DRAFT.md should describe exactly what this produces.

---

## Phase 4: Runtime

Depend on Phase 3. Two parallel worktrees.

### [parallel] timeline/ — Execution Loop

**Branch:** `wt/timeline` | **Worktree:** `../wt-timeline`
**Package:** `internal/timeline/`
**DRAFT.md ref:** "Timeline (The Task Engine)", task lifecycle, error handling
**Imports:** store/, orchestrator/, agent/, sandbox/, parse/

- [ ] `loop.go` — Main execution loop:
  - Poll: `SELECT * FROM tasks WHERE status='pending' AND run_at <= now() ORDER BY run_at LIMIT 1`
  - Dispatch task
  - Sleep/tick when no pending tasks (configurable interval, default 1s)
  - Respect context cancellation for shutdown
- [ ] `dispatch.go` — Single task execution:
  1. Agent health check (from store cache or fresh probe)
  2. Build prompt via orchestrator
  3. Log `prompt_built` event with full prompt to task_log
  4. Create sandbox with task's isolation level + skill mounts
  5. Execute agent in sandbox
  6. Capture full output
  7. Parse structured markers (parse/)
  8. Process wt:schedule directives → insert follow-up tasks
  9. Process wt:memory directives → write to memory/ files (only if skill has memory_write: true)
  10. Append thread entry (summary from agent output)
  11. Log completion events
  12. Mark task done (or failed with error)
- [ ] `dispatch.go` — Error handling per DRAFT.md taxonomy: agent unhealthy → immediate fail, sandbox fail → fail + suggest fallback, timeout → kill sandbox + fail, empty output → fail, stream interrupted → capture partial + fail
- [ ] Tests: dispatch mock task through full pipeline, handle each failure mode

**Done when:** Execution loop picks up pending tasks, runs them through the complete pipeline, handles all failure modes from the error taxonomy, creates follow-up tasks from schedule markers.

---

### [parallel] transport/ — Local API

**Branch:** `wt/transport` | **Worktree:** `../wt-transport`
**Package:** `internal/transport/`
**DRAFT.md ref:** CLI commands, Unix socket auth
**Imports:** store/

- [ ] `server.go` — HTTP server over Unix socket (`~/.wingthing/wt.sock`):
  - `POST /tasks` — submit new task (prompt or skill)
  - `GET /tasks` — list tasks (query params: status, limit)
  - `GET /tasks/:id` — get task + output
  - `POST /tasks/:id/retry` — re-queue failed task
  - `GET /thread` — today's rendered thread (query params: date, budget)
  - `GET /agents` — list agents with health status
  - `GET /status` — daemon health summary
  - `GET /log/:taskId` — task log events
- [ ] `client.go` — Go client for CLI to call these endpoints. Used by cmd/wt/.
- [ ] Tests: round-trip each endpoint with in-process server

**Done when:** CLI can submit tasks and query all daemon state via Unix socket. Client library exposes typed methods for each endpoint.

---

## Phase 5: CLI + Daemon

Depends on all previous phases. Sequential on main.

### daemon/ — Daemon Lifecycle

**Package:** `internal/daemon/`
**DRAFT.md ref:** "Graceful Shutdown"

- [ ] `daemon.go` — Wire everything together: open store, start timeline loop, start transport server
- [ ] Signal handling: SIGTERM/SIGINT → stop accepting tasks → wait for running task (30s grace) → kill sandbox if exceeded → flush thread entries → WAL checkpoint → exit 0
- [ ] Startup recovery: find tasks with status='running' from previous crash → mark failed with error="daemon shutdown"
- [ ] Tests: shutdown sequence, interrupted task recovery

### cmd/wt/ — CLI Commands

**Package:** `cmd/wt/`

Core commands (each is a cobra subcommand calling transport/client):

- [ ] `wt init` — Create `~/.wingthing/`, `memory/`, `skills/`. Seed `memory/index.md` + `identity.md` with templates. Init `wt.db`. Detect installed agent CLIs (`claude --version`, etc) and register in agents table. Print setup summary.
- [ ] `wt "prompt"` — Submit ad-hoc task to daemon via transport client
- [ ] `wt --skill name` — Submit skill task
- [ ] `wt --agent name "prompt"` — Submit with specific agent override
- [ ] `wt timeline` — Display upcoming + recent tasks (formatted table)
- [ ] `wt thread` — Display today's daily thread
- [ ] `wt thread --yesterday` — Yesterday's thread
- [ ] `wt status` — Daemon connection, agent health, pending/running task count
- [ ] `wt log` — Tail recent task log events
- [ ] `wt log --last --context` — Find most recent task, display its `prompt_built` log entry (full prompt audit)
- [ ] `wt agent list` — Table of agents with name, adapter, healthy, last checked
- [ ] `wt skill list` — Table of installed skills
- [ ] `wt skill add <name|file|url>` — Copy skill file to `~/.wingthing/skills/`
- [ ] `wt daemon` — Start daemon foreground
- [ ] `wt daemon --install` — Generate + install launchd plist (macOS) or systemd unit (Linux)

---

## Phase 6: Integration Test + Ship

- [ ] End-to-end: `wt init` → `wt daemon` (background) → `wt "hello"` → verify task ran, thread updated, log recorded
- [ ] End-to-end: install a skill → `wt --skill name` → verify skill memory loaded, template interpolated, output parsed
- [ ] End-to-end: agent schedules follow-up via wt:schedule → verify follow-up task created and executes
- [ ] `go build -o wt ./cmd/wt` produces single binary
- [ ] README.md updated with real install + usage instructions
- [ ] Tag v0.1.0
