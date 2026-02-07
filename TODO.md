# TODO — wingthing

Reference: [DRAFT.md](DRAFT.md) for full design. This file is the build plan.

## Parallelization

Tasks marked **[parallel]** within a phase can be developed simultaneously using git worktrees. Each gets its own branch, merged to main when the phase completes.

```bash
# Create worktrees for a phase
git worktree add ../wt-<name> -b wt/<name>

# Merge when phase completes
git checkout main && git merge wt/<name1> wt/<name2> ...

# Clean up
git worktree remove ../wt-<name>
```

---

## v0.1 — Core Pipeline ✅

**Tagged: v0.1.0** — 12 packages, ~129 tests, ~5500 lines Go.

### Phase 1: Foundation ✅
- [x] go mod, deps, directory structure, CLI skeleton
- [x] config loading, store (SQLite + WAL + migrations), all CRUD

### Phase 2: Independent Packages ✅ (5 parallel worktrees)
- [x] memory/ — load, index, retrieval layers 1-4, frontmatter parsing
- [x] agent/ — interface, stream, claude adapter (claude -p + stream-json)
- [x] parse/ — wt:schedule + wt:memory marker extraction
- [x] skill/ — load, interpolate templates, config var resolution
- [x] sandbox/ — fallback isolation, apple/linux stubs

### Phase 3: Integration Packages ✅ (2 parallel worktrees)
- [x] thread/ — daily thread rendering, budget truncation
- [x] orchestrator/ — prompt assembly pipeline, config precedence, format docs

### Phase 4: Runtime ✅ (2 parallel worktrees)
- [x] timeline/ — execution loop, task dispatch, error handling taxonomy
- [x] transport/ — HTTP-over-Unix-socket server + typed client

### Phase 5: CLI + Daemon ✅
- [x] daemon wiring, signal handling, crash recovery
- [x] All CLI commands: init, submit, timeline, thread, status, log, agent, skill, daemon

### Phase 6: Integration Test + Ship ✅
- [x] 3 end-to-end integration tests (lifecycle, skill, follow-up scheduling)
- [x] README with install + usage instructions
- [x] Tag v0.1.0

---

## v0.2 — Production Runtime ✅

**Tagged: v0.2.0** — 15 packages, 7 integration tests, ~8500 lines Go.

### Dependency Graph

```
Phase 7:  ollama/    apple-sandbox/    linux-sandbox/    cron/    retry/    cost/    schedule-mem/    health-fix/
          ↑          ↑                 ↑                 ↑        ↑         ↑        ↑                ↑
          agent/     sandbox/          sandbox/          store/   store/    agent/   parse/           daemon/
                                                         timeline timeline          timeline
```

All 8 are independent — **8 parallel worktrees.**

### [parallel] Ollama Adapter

**Branch:** `wt/ollama` | **Package:** `internal/agent/`
**DRAFT.md ref:** "Tier 3 Adapters", offline execution

- [x] `ollama.go` — Ollama adapter implementing Agent interface
  - `Run`: shell out to `ollama run <model> "<prompt>"`, stream stdout
  - `Health`: run `ollama list`, check exit code
  - `ContextWindow`: return model-specific value (default 128000)
- [ ] Support model selection via agent config_json (e.g. `{"model": "llama3.2"}`)
- [ ] Register ollama agent(s) in `wt init` when ollama detected
- [ ] Tests: mock ollama output parsing, health check

**Done when:** `wt --agent ollama "prompt"` works. Fully offline execution path.

---

### [parallel] Apple Containers Sandbox

**Branch:** `wt/apple-sandbox` | **Package:** `internal/sandbox/`
**DRAFT.md ref:** "Sandbox Model", "macOS: Apple Containers"

- [ ] `apple.go` — Real Apple Containers implementation
  - Detect via presence of `container` CLI or macOS 26+ version check
  - Create lightweight Linux VM per task
  - Configure mounts per isolation level (strict=read-only, standard=rw mounted dirs, network=+net, privileged=full)
  - Network: expose ollama port (localhost:11434) into container for local model tasks
  - TTL enforcement: kill container at timeout
  - `Destroy()`: stop + remove container
- [ ] Mount resolution: skill `$VAR` paths resolved before mount
- [ ] Tests: container lifecycle (create, exec, destroy), mount validation, isolation level enforcement

**Done when:** On macOS 26+, `sandbox.New()` returns an Apple Container instead of fallback. Tasks execute inside real VMs.

---

### [parallel] Linux Sandbox

**Branch:** `wt/linux-sandbox` | **Package:** `internal/sandbox/`
**DRAFT.md ref:** "Linux: Namespace + seccomp"

- [ ] `linux.go` — Namespace + seccomp sandbox
  - `clone()` with CLONE_NEWNS, CLONE_NEWPID, CLONE_NEWNET (based on isolation level)
  - Seccomp filter: restrict syscalls per isolation level
  - Landlock (where available): filesystem access control
  - Network namespace: allow localhost:11434 for ollama, block all else (standard), or allow all (network level)
  - Mount namespace: bind-mount skill-declared paths
  - Rlimits: CPU time, memory, file descriptors
- [ ] Tests: namespace creation, seccomp filter application, build-tagged for linux only

**Done when:** On Linux, `sandbox.New()` returns a namespace sandbox. Agents run in isolated namespaces with restricted syscalls.

---

### [parallel] Recurring Tasks (Cron)

**Branch:** `wt/cron` | **Packages:** `internal/store/`, `internal/timeline/`, `internal/skill/`
**DRAFT.md ref:** "Timeline", cron expressions, skill `schedule:` field

- [ ] Add cron expression parsing (use `github.com/robfig/cron/v3` or lightweight pure-Go parser)
- [ ] `store/tasks.go` — After task with `cron` completes, compute next `run_at` from cron expression, insert new pending task
- [ ] `timeline/cron.go` — Post-completion hook: check if completed task has cron, schedule next occurrence
- [ ] `skill/` — When skill has `schedule:` frontmatter, `wt skill add` creates a recurring task in the timeline
- [ ] CLI: `wt schedule list` — show recurring tasks. `wt schedule remove <id>` — cancel recurring.
- [ ] Tests: cron expression parsing, next occurrence calculation, recurring task chain

**Done when:** A skill with `schedule: "0 8 * * 1-5"` creates a task that fires at 8am weekdays, re-schedules itself after each execution.

---

### [parallel] Retry Policies

**Branch:** `wt/retry` | **Packages:** `internal/store/`, `internal/timeline/`, `internal/transport/`, `cmd/wt/`
**DRAFT.md ref:** "Error Handling", "No automatic retries in v0.1"

- [ ] Migration: add `retry_count INTEGER DEFAULT 0`, `max_retries INTEGER DEFAULT 0` to tasks table
- [ ] `timeline/dispatch.go` — On task failure: if `retry_count < max_retries`, re-queue with exponential backoff (1s, 2s, 4s, 8s, cap at 5min)
- [ ] `transport/server.go` — `POST /tasks/:id/retry` endpoint: re-queue a failed task manually
- [ ] `transport/client.go` — `RetryTask(id)` method
- [ ] `cmd/wt/main.go` — `wt retry <task-id>` command
- [ ] Config: default max_retries in config.yaml, overridable per skill
- [ ] Tests: retry count increments, backoff timing, max retry exhaustion, manual retry

**Done when:** Failed tasks auto-retry with backoff. `wt retry <id>` re-queues manually. Skills can set their own retry policies.

---

### [parallel] Cost Tracking

**Branch:** `wt/cost` | **Packages:** `internal/agent/`, `internal/store/`, `cmd/wt/`
**DRAFT.md ref:** "Cost tracking", `tokens_used` column

- [ ] `agent/adapter.go` — Extend Stream to report token usage (input tokens, output tokens) from agent response
- [ ] `agent/claude.go` — Parse `stream-json` for token usage stats (in `result` event)
- [ ] `store/thread.go` — Populate `tokens_used` on thread entries
- [ ] `store/` — Add query: `SumTokensByDateRange(start, end)` for cost aggregation
- [ ] `cmd/wt/main.go` — Enhance `wt status` to show daily/weekly token totals
- [ ] Tests: token extraction from claude stream-json, aggregation queries

**Done when:** `wt status` shows "tokens today: 12,345 | this week: 89,012". Thread entries track per-task token usage.

---

### [parallel] Schedule Memory Declarations

**Branch:** `wt/schedule-mem` | **Packages:** `internal/parse/`, `internal/timeline/`
**DRAFT.md ref:** "wt:schedule does not support memory declarations — v0.2 feature"

- [ ] `parse/schedule.go` — Extend ScheduleDirective to support `memory="file1,file2"` attribute
- [ ] `timeline/dispatch.go` — When creating follow-up tasks from schedule directives, populate task's `memory` column with declared files
- [ ] `orchestrator/build.go` — When building prompt for a task with explicit memory column, use those files as Layer 2 (in addition to skill deps)
- [ ] Tests: parse schedule with memory attr, follow-up task gets memory column, orchestrator loads declared memory

**Done when:** `<!-- wt:schedule delay="10m" memory="deploy-log,projects" -->check deploy<!-- /wt:schedule -->` creates a follow-up task that loads those specific memory files.

---

### [parallel] Agent Health Fix

**Branch:** `wt/health-fix` | **Packages:** `internal/daemon/`, `internal/timeline/`

- [ ] `daemon.go` — On startup, probe all registered agents and update health in store
- [ ] `timeline/dispatch.go` — Cache health check results with 60s TTL (per DRAFT.md), refresh on miss
- [ ] `transport/server.go` — `/agents` endpoint returns fresh health status (probe if stale)
- [ ] Tests: health cache TTL, stale cache refresh

**Done when:** `wt agent list` shows accurate health. Agent health is probed on daemon start and cached with TTL.

---

### Phase 8: v0.2 Integration + Ship ✅

- [x] Integration test: recurring task fires on schedule, re-schedules
- [x] Integration test: failed task retries with backoff
- [x] Integration test: multi-agent dispatch
- [x] Integration test: unknown agent health check blocks dispatch
- [x] `go test ./...` passes (15 packages, 7 integration tests)
- [x] Update README with v0.2 features
- [x] Tag v0.2.0

---

## v0.3 — wingthing.ai (Sync + Relay) ✅

**Tagged: v0.3.0** — 21 packages, ~12000 lines Go, two binaries (wt + wtd).

### Dependency Graph

```
Phase 9:  sync/          websocket/       auth/
          ↑              ↑                ↑
          memory/        transport/       config/
          store/                          store/

Phase 10: relay-server/                                ← separate binary: wtd (wingthing.ai server)
          ↑
          websocket/  auth/  sync/

Phase 11: web-ui/                                      ← PWA, served by relay-server
          ↑
          relay-server/
```

### Phase 9: Client-Side Infrastructure (3 parallel worktrees)

#### [parallel] Memory Sync

**Branch:** `wt/sync` | **Package:** `internal/sync/`
**DRAFT.md ref:** "Memory sync across machines", "wingthing.ai is three things"

- [ ] `sync.go` — Sync engine for `~/.wingthing/memory/` directory
  - File-level diffing: hash each file, compare with remote manifest
  - Bidirectional: push local changes, pull remote changes
  - Conflict resolution: last-write-wins with conflict log (conflicts written to `memory/.conflicts/`)
  - Incremental: only transfer changed files
- [ ] `manifest.go` — File manifest: path, sha256, mtime, machine_id. JSON format.
- [ ] `sqlite_sync.go` — Row-level sync for thread_entries and tasks tables
  - Merge by timestamp + machine_id (no conflicts — entries are append-only per machine)
  - Configurable: sync thread (yes), sync tasks (configurable), sync agents (yes)
- [ ] Encryption: all data encrypted with device key before transmission
- [ ] Tests: manifest generation, diff calculation, conflict detection, merge resolution

**Done when:** Given two `~/.wingthing/` directories, sync engine produces the correct merged state with conflicts logged.

---

#### [parallel] WebSocket Client

**Branch:** `wt/websocket` | **Package:** `internal/ws/`
**DRAFT.md ref:** "Transport: Outbound WebSocket", connection states

- [ ] `client.go` — Outbound WebSocket connection to wingthing.ai
  - Auto-reconnect with exponential backoff
  - Heartbeat/ping-pong for connection health
  - Message types: task_submit, task_result, sync_request, sync_response, status
  - Graceful shutdown on daemon stop
- [ ] `protocol.go` — Wire protocol: JSON messages with type field, request/response correlation IDs
- [ ] Connection state machine: connecting → connected → authenticated → syncing → ready
- [ ] Offline mode: queue outbound messages when disconnected, flush on reconnect
- [ ] Tests: protocol serialization, reconnect behavior, message queuing

**Done when:** Daemon maintains a persistent outbound WebSocket to wingthing.ai. Tasks submitted remotely arrive at the daemon. Results stream back.

---

#### [parallel] Device Auth

**Branch:** `wt/auth` | **Packages:** `internal/auth/`, `cmd/wt/`
**DRAFT.md ref:** "Authentication", device-specific tokens

- [ ] `auth.go` — Device authentication flow
  - `wt login` → opens browser to wingthing.ai/auth → device code flow
  - Receive device token, store in config.yaml (encrypted at rest)
  - Token refresh: automatic before expiry
- [ ] `token.go` — Token storage, refresh, validation
- [ ] `cmd/wt/main.go` — `wt login` and `wt logout` commands
- [ ] Tests: token lifecycle, refresh flow

**Done when:** `wt login` authenticates with wingthing.ai. Device token stored securely. WebSocket client uses token for auth.

---

### Phase 10: Relay Server

**New binary:** `cmd/wtd/` (wingthing.ai daemon — the server side)

This is the server that runs at wingthing.ai. Separate deployment from the `wt` binary.

#### Relay Server

**Branch:** `wt/relay` | **Package:** `cmd/wtd/`, `internal/relay/`
**DRAFT.md ref:** "wingthing.ai", relay architecture

- [ ] `cmd/wtd/main.go` — Server binary entrypoint
- [ ] `internal/relay/server.go` — WebSocket server
  - Accept daemon connections (outbound WS from user machines)
  - Accept client connections (web, phone, API)
  - Route tasks: client → relay → daemon → relay → client
  - Stream results in real-time
- [ ] `internal/relay/sessions.go` — Session management
  - Map device tokens to WebSocket connections
  - Handle multi-device (one user, multiple daemons)
  - Route to correct daemon based on machine_id or "any available"
- [ ] `internal/relay/sync.go` — Memory sync relay
  - Store encrypted manifests
  - Relay sync diffs between machines
  - Never decrypt — relay sees ciphertext only
- [ ] `internal/relay/api.go` — REST API for web/phone clients
  - `POST /tasks` — submit task (relayed to daemon)
  - `GET /tasks` — list recent tasks
  - `GET /thread` — today's thread
  - `GET /status` — daemon connection status
- [ ] `internal/relay/auth.go` — User auth (OAuth or API key), device token issuance
- [ ] `internal/relay/ratelimit.go` — Per-user rate limiting (free tier: N tasks/day, pro: unlimited)
- [ ] PostgreSQL or SQLite for relay state (user accounts, device tokens, audit log)
- [ ] Tests: routing, multi-device, rate limiting

**Done when:** `wtd` runs as a server. Daemons connect via WebSocket. Tasks submitted through the API reach the daemon and results come back.

---

### Phase 11: Web UI (PWA)

**Branch:** `wt/web` | **Package:** `web/`
**DRAFT.md ref:** "Remote UI", "Thread viewer"

- [ ] PWA shell: installable on phone, works offline (shows cached data)
- [ ] Task submission: text input → POST to relay API
- [ ] Timeline view: list of tasks with status, expandable output
- [ ] Thread view: rendered daily thread markdown
- [ ] Status dashboard: daemon connection, agent health, daily token usage
- [ ] Real-time updates: WebSocket from relay for live task progress
- [ ] Mobile-first responsive design
- [ ] Served by `wtd` at wingthing.ai/app

**Done when:** Open wingthing.ai on your phone, submit a task, see it execute on your machine, read today's thread.

---

### Phase 12: v0.3 Integration + Ship ✅

- [x] End-to-end: phone → relay → daemon → agent → relay → phone (TestMessageRouting)
- [x] End-to-end: memory sync between two machines (TestExportImportThreadEntries)
- [x] End-to-end: daemon reconnects after relay restart (TestClientReconnect)
- [x] Concurrent session management (TestSessionManagerConcurrency)
- [ ] Deploy `wtd` to hosting (fly.io / railway / VPS) — deferred to v0.6
- [ ] DNS: wingthing.ai pointing at relay server — deferred to v0.6
- [ ] TLS: Let's Encrypt for wss:// and https:// — deferred to v0.6
- [x] Update README
- [x] Tag v0.3.0

---

## v0.4 — Skill Registry + More Agents

**Goal:** 128 curated skills at wingthing.ai/skills. Gemini adapter. E2E encryption. Task dependencies.

### Dependency Graph

```
Phase 13: gemini/    skill-registry/    e2e-encrypt/    task-deps/    thread-merge/
          ↑          ↑                  ↑               ↑             ↑
          agent/     relay/             sync/           store/        sync/
                     web/               auth/           timeline/     store/
```

All 5 are independent — **5 parallel worktrees.**

### [parallel] Gemini Adapter

**Branch:** `wt/gemini` | **Package:** `internal/agent/`

- [x] `gemini.go` — Gemini CLI adapter
  - `Run`: shell out to `gemini -p "<prompt>"`, parse output
  - `Health`: `gemini --version`
  - `ContextWindow`: model-dependent (default 1M for Gemini 2.5)
- [ ] Register in `wt init` when gemini CLI detected
- [x] Tests: output parsing, health check

---

### [parallel] Skill Registry

**Branch:** `wt/skill-registry` | **Packages:** `internal/relay/`, `web/`, `cmd/wt/`
**DRAFT.md ref:** "128 curated skills at wingthing.ai/skills"

- [x] `internal/relay/skills.go` — Skill hosting
  - Serve skill files from curated collection
  - Metadata API: list skills, search, filter by category
  - Download endpoint: `GET /skills/:name`
  - Signing: each skill file has SHA256 + publisher signature
- [x] `cmd/wt/main.go` — Enhance `wt skill add <name>` to fetch from wingthing.ai/skills
  - Verify signature on download
  - Warn if required config vars are unset
- [x] `wt skill list --available` — Browse registry
- [ ] `web/` — Skills browser page at wingthing.ai/skills
  - Category navigation, search, one-click install (via CLI command copy)
- [x] Seed initial skills (target: 128 across 9 categories per DRAFT.md)
  - Dev workflow (~20): jira-briefing, pr-review, deploy-check, test-runner, branch-cleanup, ci-status, release-notes, code-review, lint-fix, migration-check, dependency-audit, git-summary, standup-prep, sprint-review, tech-debt-scan, hotfix-deploy, env-check, api-health, db-migration, feature-flag
  - Code (~20): refactor, debug, explain, migrate, generate-tests, add-types, extract-function, dead-code, performance-profile, security-scan, api-endpoint, database-query, error-handling, logging, documentation, code-search, regex-builder, data-model, schema-design, algorithm
  - Research (~15): web-research, competitor-analysis, paper-summary, market-research, tech-evaluation, architecture-review, api-comparison, license-check, vulnerability-scan, trend-analysis, pricing-research, user-research, benchmark, spec-review, rfc-summary
  - Writing (~15): blog-draft, email-compose, meeting-notes, changelog, readme, proposal, postmortem, runbook, adr, tutorial, announcement, documentation, pitch, newsletter, social-post
  - Ops (~15): server-health, log-analysis, incident-response, backup-check, disk-usage, process-monitor, cert-check, dns-check, uptime-report, latency-check, error-rate, queue-depth, memory-usage, cpu-profile, network-check
  - Data (~10): csv-analysis, sql-query, dashboard-summary, report, data-clean, pivot-table, chart, export, schema-compare, data-migration
  - Personal (~15): calendar-briefing, todo-review, reading-list, habit-tracker, journal-prompt, weekly-review, goal-check, meal-plan, workout-log, budget-check, travel-plan, gift-ideas, recipe-finder, book-notes, learning-path
  - Meta (~8): memory-maintenance, thread-cleanup, skill-test, cost-report, memory-index-rebuild, agent-benchmark, prompt-audit, config-validate
  - System (~10): agent-install, sandbox-test, sync-check, config-validate, health-check, disk-cleanup, log-rotate, backup-memory, export-data, import-data
- [x] Tests: registry API, signature verification, download + install flow

**Done when:** `wt skill add jira-briefing` fetches from wingthing.ai/skills, verifies signature, installs. 128 skills available.

---

### [parallel] E2E Encryption

**Branch:** `wt/e2e-encrypt` | **Packages:** `internal/sync/`, `internal/auth/`
**DRAFT.md ref:** "E2E encryption for sync", "never sees plaintext memory"

- [x] Key management: device keypair generated on `wt login`, public key registered with relay
  - X25519 for key exchange, XChaCha20-Poly1305 for symmetric encryption
- [x] `sync/encrypt.go` — Encrypt memory files and sync diffs before transmission
- [x] `sync/decrypt.go` — Decrypt on receiving end (via encrypted_engine.go)
- [x] Multi-device: shared symmetric key derived from user passphrase (Argon2id)
- [x] Relay sees only ciphertext — cannot read memory or thread content
- [ ] Key rotation: periodic re-key, old keys kept for decrypting old data
- [x] Tests: encrypt/decrypt round-trip, multi-device key derivation, key rotation

**Done when:** All data in transit and at rest on relay is encrypted. Relay operator cannot read user data.

---

### [parallel] Task Dependencies

**Branch:** `wt/task-deps` | **Packages:** `internal/store/`, `internal/timeline/`
**DRAFT.md ref:** "Task dependencies — v0.4"

- [x] Migration: add `depends_on TEXT` to tasks table (JSON array of task IDs)
- [x] `store/tasks.go` — `ListReady()`: pending tasks where all depends_on are done
- [x] `timeline/loop.go` — Use `ListReady()` instead of `ListPending()` for task selection
- [x] `parse/schedule.go` — Extend wt:schedule with `after="<task-id>"` attribute
- [x] CLI: `wt "task B" --after <task-A-id>` flag
- [x] Tests: dependency chain resolution, diamond dependencies, failed dependency blocks downstream

**Done when:** Tasks can declare dependencies. A task won't execute until all its dependencies are done. Failed dependencies block downstream.

---

### [parallel] Multi-Machine Thread Merge

**Branch:** `wt/thread-merge` | **Packages:** `internal/sync/`, `internal/store/`
**DRAFT.md ref:** "Multi-machine thread merge"

- [x] Thread entries from multiple machines interleave by timestamp (already designed for this — thread_entries have machine_id)
- [x] `sync/thread.go` — Merge thread entries from remote: insert missing entries, skip duplicates (by task_id + machine_id)
- [x] `thread/render.go` — Show machine origin in rendered thread when entries come from multiple machines
- [x] Tests: merge entries from 2 machines, correct timestamp ordering, duplicate detection

**Done when:** Two machines syncing via relay see each other's thread entries interleaved correctly by timestamp.

---

### Phase 14: v0.4 Integration + Ship

- [x] End-to-end: install skill from registry with signature verification
- [x] End-to-end: gemini adapter task execution
- [x] End-to-end: encrypted sync between two machines
- [x] End-to-end: task dependency chain executes in order
- [x] End-to-end: thread merge from two machines
- [x] Populate registry with initial 128 skills
- [x] Update README
- [x] Tag v0.4.0

---

## v0.5 — More Adapters + Advanced Orchestration

**Goal:** Tier 1+2 agent adapters. Smart budget management. Two-pass loading. LLM orchestrator boost. Interactive sessions.

### Dependency Graph

```
Phase 15: opencode/    codex/    goose/    amp/    budget/    two-pass/    llm-triage/    interactive/
          ↑            ↑         ↑         ↑       ↑          ↑            ↑              ↑
          agent/       agent/    agent/    agent/  orchestr/  orchestr/    orchestr/      agent/
                                                   thread/    memory/      memory/        timeline/
```

All 8 independent — **8 parallel worktrees.**

### [parallel] OpenCode Adapter

**Branch:** `wt/opencode` | **Package:** `internal/agent/`
**DRAFT.md ref:** "Tier 1: OpenCode"

- [ ] `opencode.go` — OpenCode adapter
  - `Run`: `opencode run "prompt"` or HTTP to `opencode serve`
  - Prefer `serve` mode when available (persistent server, HTTP API)
  - `Health`: check for `opencode` binary or running serve instance
  - `ContextWindow`: provider-dependent
- [ ] Support session resume via `--continue` / `--session`
- [ ] Tests: output parsing, serve mode API

---

### [parallel] Codex Adapter

**Branch:** `wt/codex` | **Package:** `internal/agent/`
**DRAFT.md ref:** "Tier 2: Codex CLI"

- [ ] `codex.go` — Codex CLI adapter
  - `Run`: `codex exec "prompt" --json` for NDJSON streaming
  - `Health`: check for `codex` binary
  - `ContextWindow`: model-dependent
- [ ] Parse NDJSON stream events
- [ ] Tests: NDJSON parsing, output collection

---

### [parallel] Goose Adapter

**Branch:** `wt/goose` | **Package:** `internal/agent/`
**DRAFT.md ref:** "Tier 2: Goose"

- [ ] `goose.go` — Goose adapter
  - `Run`: `goose run --with-builtin developer -t "prompt"`
  - `Health`: check for `goose` binary
  - `ContextWindow`: provider-dependent
- [ ] Tests: output parsing

---

### [parallel] Amp Adapter

**Branch:** `wt/amp` | **Package:** `internal/agent/`
**DRAFT.md ref:** "Tier 2: Amp"

- [ ] `amp.go` — Amp adapter
  - `Run`: `amp -x "prompt"` or piped `echo "prompt" | amp -x`
  - `--stream-json` for streaming output
  - `Health`: check for `amp` binary
  - `ContextWindow`: provider-dependent
- [ ] MCP support: inject `--mcp-config` for tool servers
- [ ] Tests: stream-json parsing

---

### [parallel] Smart Budget Management

**Branch:** `wt/budget` | **Packages:** `internal/orchestrator/`, `internal/thread/`
**DRAFT.md ref:** "Budget management (pre-1.0)", token budget priority

- [ ] `orchestrator/budget.go` — Priority-based allocation:
  - P0 (must include): task prompt, identity
  - P1 (must include): skill-declared memory
  - P2 (important): recent thread entries (last 3 verbatim)
  - P3 (nice to have): keyword-matched memory
  - P4 (expendable): older thread entries (one-line summaries)
  - Allocate budget top-down: P0 first, remaining to P1, etc.
- [ ] `thread/budget.go` — Tiered thread rendering:
  - Last 3 entries: full verbatim
  - Older entries within budget: one-line summaries
  - Entries beyond budget: dropped
- [ ] Per-section caps: thread max 40% of budget, memory max 40%, skill template max 20%
- [ ] Tests: priority allocation, budget overflow handling, section caps

**Done when:** Orchestrator intelligently allocates context window budget. Long threads don't crowd out memory. Memory doesn't crowd out the thread.

---

### [parallel] Two-Pass Loading (Layer 5)

**Branch:** `wt/two-pass` | **Packages:** `internal/orchestrator/`, `internal/timeline/`
**DRAFT.md ref:** "Layer 5: Two-pass loading"

- [ ] `orchestrator/twopass.go` — Detect "needs more context" signal in agent response
  - Signal: agent says something like "I don't have enough context about X" or uses a `<!-- wt:need-context file="X" -->` marker
- [ ] `timeline/dispatch.go` — If two-pass needed: re-run orchestrator with additional memory, call agent again
  - Max 1 retry (prevent infinite loops)
  - Log both passes to task_log
- [ ] `parse/parse.go` — Add `wt:need-context` marker parsing
- [ ] Tests: two-pass detection, additional memory loading, loop prevention

**Done when:** If an agent indicates it needs more context, wingthing automatically does a second pass with additional memory loaded. One extra agent call, only when needed.

---

### [parallel] LLM Triage (Layer 6)

**Branch:** `wt/llm-triage` | **Packages:** `internal/orchestrator/`, `internal/agent/`
**DRAFT.md ref:** "Layer 6: LLM triage (optional, online only)"

- [ ] `orchestrator/triage.go` — For ambiguous tasks where layers 1-4 don't produce confident memory matches:
  - Send task prompt + index.md to a fast/cheap model
  - Ask: "which of these topic files are relevant?"
  - Load the model's recommended files
- [ ] Configurable: which agent to use for triage (default: ollama if available, else skip)
- [ ] Triage only when keyword match returns 0 results and task is ad-hoc (not skill)
- [ ] Cache triage results for similar prompts (fuzzy)
- [ ] Tests: triage prompt construction, response parsing, cache hits

**Done when:** Ambiguous ad-hoc tasks get smarter memory selection via a cheap LLM call. Falls back gracefully when no triage model available.

---

### [parallel] Interactive Sessions

**Branch:** `wt/interactive` | **Packages:** `internal/agent/`, `internal/timeline/`, `cmd/wt/`
**DRAFT.md ref:** "Session resume", "Interactive mode"

- [ ] `agent/claude.go` — Support `--resume $session_id` for multi-turn tasks
- [ ] `agent/session.go` — Session tracking: create, resume, list sessions
- [ ] `store/` — Migration: add `session_id TEXT` to tasks table
- [ ] `timeline/` — Interactive task mode: keep sandbox alive, allow follow-up messages within same session
- [ ] `cmd/wt/main.go` — `wt chat` command: interactive REPL that submits follow-up messages to same session
- [ ] `wt chat --resume` — Resume most recent session
- [ ] Tests: session create/resume, multi-turn execution

**Done when:** `wt chat` opens an interactive session. Follow-up messages share context. `wt chat --resume` picks up where you left off.

---

### Phase 16: v0.5 Integration + Ship

- [ ] Integration test: each new adapter end-to-end
- [ ] Integration test: smart budget with large thread + many memory files
- [ ] Integration test: two-pass loading triggers and resolves
- [ ] Integration test: interactive session multi-turn
- [ ] Update README with adapter list, budget docs, interactive mode
- [ ] Tag v0.5.0

---

## v0.6 — Agent-Driven Install + Service Management

**Goal:** Any coding agent can install wingthing. Daemon runs as a system service.

### Phase 17: Install + Service (2 parallel)

#### [parallel] Agent-Driven Install

**Branch:** `wt/install` | **New files:** `install.sh`, `install.md`
**DRAFT.md ref:** "Bootstrap: Agent-Driven Install"

- [ ] `install.sh` — Shell script: download binary, create ~/.wingthing/, run `wt init`
  - Platform detection (macOS arm64, macOS x86_64, Linux amd64, Linux arm64)
  - Download from GitHub releases
  - Verify checksum
  - Add to PATH (suggest shell rc modification)
- [ ] `install.md` — Agent-readable instruction set for wingthing.ai/install.md
  - Explains what wingthing is (for the agent)
  - Step-by-step: run install.sh, run wt init, configure identity, test with `wt "hello"`
  - Agent personalizes setup: fills in identity.md, detects agents, suggests skills
- [ ] GitHub Actions: build + release binaries for all platforms on tag push
- [ ] Tests: install.sh runs in clean container (Docker-based CI test)

**Done when:** `curl -fsSL wingthing.ai/install.sh | sh` installs wingthing. An agent reading install.md can bootstrap + personalize the full setup.

---

#### [parallel] Service Installation

**Branch:** `wt/service` | **Packages:** `cmd/wt/`, `internal/daemon/`
**DRAFT.md ref:** "wt daemon --install"

- [ ] `daemon/launchd.go` — Generate macOS launchd plist
  - `~/Library/LaunchAgents/ai.wingthing.wt.plist`
  - RunAtLoad, KeepAlive, StandardOutPath, StandardErrorPath
  - `launchctl load/unload`
- [ ] `daemon/systemd.go` — Generate Linux systemd unit
  - `~/.config/systemd/user/wingthing.service`
  - `systemctl --user enable/start/stop`
- [ ] `cmd/wt/main.go` — `wt daemon --install` generates + installs service file
- [ ] `wt daemon --uninstall` — Remove service file, stop service
- [ ] Tests: plist generation, unit file generation

**Done when:** `wt daemon --install` makes wingthing start on login. `wt daemon --uninstall` removes it.

---

### Phase 18: v0.6 Integration + Ship

- [ ] End-to-end: install.sh on clean macOS + Linux (CI matrix)
- [ ] End-to-end: launchd install/uninstall on macOS
- [ ] End-to-end: systemd install/uninstall on Linux
- [ ] Publish install.sh + install.md to wingthing.ai
- [ ] GitHub Actions release pipeline
- [ ] Update README
- [ ] Tag v0.6.0

---

## v1.0 — Production Release

**Goal:** Everything works. Everything is documented. Ship it.

### Phase 19: Hardening

- [ ] Full error handling audit: every code path has proper error propagation
- [ ] Graceful degradation: daemon stays up when individual components fail
- [ ] Connection resilience: WebSocket auto-reconnect tested under network flap
- [ ] Memory safety: no goroutine leaks, no unbounded channels, no unclosed resources
- [ ] Security audit: sandbox escape paths, auth token handling, input validation
- [ ] Performance: profile under load (100 tasks/min, 50 memory files, 1000 thread entries)
- [ ] SQLite: WAL checkpoint strategy, vacuum schedule, db size monitoring
- [ ] Logging: structured logging (slog), log levels, log rotation

### Phase 20: Documentation

- [ ] wingthing.ai landing page
- [ ] Getting started guide
- [ ] Skill authoring guide
- [ ] API reference (relay endpoints)
- [ ] Architecture overview
- [ ] FAQ
- [ ] Troubleshooting guide
- [ ] Contributing guide

### Phase 21: Ship

- [ ] All integration tests pass on macOS + Linux
- [ ] All 128 skills verified
- [ ] Performance benchmarks documented
- [ ] Security model documented
- [ ] Tag v1.0.0
- [ ] Launch
