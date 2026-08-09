# Wingthing as an agent meta-access layer

Status: first local implementation slice  
Reviewed: 2026-08-08

## Thesis

Wingthing should be the stable interface through which humans and models reach
agent runtimes. It should not become another agent, a universal prompt format,
or a cloud scheduler.

The underlying agents remain opinionated products. Claude Code, Codex, Gemini,
Hermes, OpenCode, Cursor, and Ollama choose their own models, tools, context
strategies, and interaction styles. Wingthing gives those different runtimes a
small common control plane:

- discover what is installed and what it can do
- start or reattach to a durable terminal
- execute one bounded headless prompt
- inspect durable task state and output
- repeat a prompt with an explicit stop condition and hard bound
- run a dependency graph whose independent nodes may execute concurrently
- let either a human or another model read, wait for, and steer a terminal

That is the useful meaning of **agent meta-access layer**. It is access to
agents, not an attempt to erase their differences.

## The object model

The product becomes much easier to reason about when byte streams and semantic
work are different objects:

| Object | Meaning | Lifetime | Authoritative owner |
|---|---|---|---|
| **Terminal** | A durable PTY, its process tree, snapshot, and input stream | Until explicitly stopped or process exit | egg/wing runtime |
| **Agent session** | A terminal occupied by a known agent adapter | Terminal lifetime | egg/wing runtime |
| **Prompt run** | One bounded, non-interactive invocation with structured status and working directory | Durable task record | local task store |
| **Loop** | A bounded sequence of prompt runs where result N is input to N+1 | Parent task plus child tasks | local orchestrator |
| **Swarm** | A bounded DAG of prompt runs; dependency results flow downstream | Parent task plus child tasks | local orchestrator |
| **Prompt asset** | A named, versioned prompt/template plus declared variables and runtime defaults | Persistent configuration | local prompt store |

A terminal is not a task. Terminal output is raw ANSI state and may contain a
TUI, shell, compiler, or model. A task has a semantic status and an output. An
agent session is the bridge between the two when interactive control matters.

This prevents three recurring category mistakes:

1. PTY activity is not automatically `working` or `done`.
2. A group of people is not a swarm execution namespace.
3. A hosted route is not the owner of either terminal or task state.

## Human and model parity

The same primitives should be available through several thin clients:

```text
human CLI/TUI ─┐
browser UI ────┼── local control semantics ──> wing/eggs + task store
LLM via MCP ───┤
editor/plugin ─┘
```

No client should need to scrape another client's UI. A model should be able to
list terminals, read a snapshot, wait for output, start an agent, or submit a
swarm using an explicit schema. A human should be able to inspect and interrupt
the exact same objects from a terminal or browser.

This is the deeper idea worth taking from Herdr: agent automation is not a
special dashboard feature. It is another client of the durable terminal
runtime. Wingthing can extend that idea with sandbox policy, structured prompt
runs, dependency flow, and collaboration controls.

## First interface: local MCP over stdio

This branch adds:

```bash
wt mcp stdio
```

It implements newline-delimited MCP JSON-RPC over stdin/stdout. Standard output
contains protocol messages only; diagnostics go to standard error. Every tool
has a closed JSON Schema and returns both structured content and a serialized
text fallback.

Current tools:

| Tool | Purpose |
|---|---|
| `wingthing_capabilities` | Supported/installed agents, storage/network requirements, transports, and object types |
| `terminal_list` | Discover live local terminals |
| `terminal_read` | Read the current ANSI snapshot |
| `terminal_send` | Send PTY input |
| `terminal_wait` | Wait for output text or I/O idleness without client polling |
| `agent_start` | Start a persistent sandboxed agent terminal |
| `terminal_stop` | Stop a terminal and its process tree |
| `prompt_list` | List current named prompt assets and revisions |
| `prompt_get` | Read a current or immutable historical prompt revision |
| `prompt_save` | Atomically create/update a prompt with optimistic revision checking |
| `prompt_run` | Execute one sandboxed headless prompt in an explicit working directory and return its task record |
| `task_get` | Read durable task status, output, error, timing, and dependencies |
| `prompt_loop` | Execute a bounded sequential loop with optional text stop condition |
| `swarm_run` | Execute a bounded dependency DAG with output flow |

A generic local MCP client can register it with the equivalent of:

```json
{
  "mcpServers": {
    "wingthing": {
      "command": "wt",
      "args": ["mcp", "stdio"]
    }
  }
}
```

The exact configuration file differs by client. No Wingthing account or network
service is involved; the MCP child process has the same local authority as the
user who launched it.

## Loop semantics

A loop is deliberately less magical than an “autonomous mode”:

1. Run the base prompt.
2. Store the task and output.
3. Add that output to the next iteration as a dependency result.
4. Stop if `until_contains` matches.
5. Otherwise stop at `max_iterations`, which cannot exceed 12.

The runtime guarantees dependency delivery and the hard bound. It cannot
guarantee that a weak model follows an instruction contained in the dependency.
Tests must report those separately. A model ignoring iteration context is a
model-compliance failure; missing dependency context would be an orchestrator
failure.

Future loop conditions should be typed rather than becoming an expression
language immediately: exact match, JSON Schema validation, test command exit,
human approval, evaluator task, and time/token/cost budget.

## Swarm semantics

A swarm is a DAG, not a chat room and not an organization:

```text
research-a ─┐
            ├── synthesis ──> review
research-b ─┘
```

- Nodes with satisfied dependencies are eligible to run.
- Independent nodes may run concurrently.
- Completed dependency outputs are injected into each downstream prompt under a
  clearly delimited `Dependency Results` section.
- Unknown dependencies, duplicate IDs, self-dependencies, and cycles are
  rejected before any model call.
- A failed node prevents dependent nodes from running.
- The whole graph is capped at 16 nodes and four concurrent workers.
- An agent-specific limit can be stricter. OpenCode is currently serialized
  because its CLI shares one SQLite state database and concurrent processes can
  fail with `database is locked`.
- Every parent and child is a durable task record, not an ephemeral goroutine.

This is enough to express map/reduce, debate/review, independent investigation,
and staged implementation without inventing a general workflow language.

## Safety and authority

LLM accessibility must not mean invisible ambient authority.

- MCP tool annotations identify read-only, mutating, destructive, and
  open-world operations.
- Agent invocations use the same sandbox/profile resolution as `wt run`.
- Timeouts cancel the whole agent process group, including grandchildren.
- Stderr is captured with a bound so failures are useful but cannot consume
  unbounded memory.
- Loops, swarm size, and concurrency have server-side maximums that a caller
  cannot raise.
- Task IDs include a UUID component; concurrent workers cannot collide within
  one second.
- Each prompt task persists its absolute working directory and mounts that path
  into the sandbox, so repo context is inspectable and retryable.
- SQLite writer contention has a bounded busy timeout.
- Terminal stop is explicit and separately marked destructive.

The current stdio server is intentionally local-user authority. Before exposing
these calls remotely, Wingthing needs principal-aware grants for each action:
view terminal, control terminal, start process, stop process, invoke model,
access path/profile, and use privileged tools. Transport authentication alone
is not authorization.

## Relationship to collaboration

There are two different kinds of collaboration:

- **work coordination:** tasks, dependencies, loops, evaluators, and swarms
- **authority coordination:** viewers, controller lease, takeover, approvals,
  grants, and audit attribution

They compose but must not collapse into one concept. A swarm can run entirely
for one local user. Three humans can collaborate in one terminal without a
swarm. A hosted organization can help resolve identities and billing, but it is
neither the task graph nor the terminal namespace.

This gives the old “roost” story a clean role. A shared roost is one deployment
of the runtime and policy plane. `wingthing.ai` may help a client find or reach
it. Neither one owns the abstract agent workflow.

## What is implemented versus next

Implemented on the vacation branch:

- local MCP stdio server and strict tool schemas
- terminal list/read/send/wait/start/stop
- named prompt templates with declared variables, content-addressed revisions,
  immutable local history, atomic writes, and conflict detection
- structured headless prompt runs and task inspection
- bounded sequential loops with dependency result injection
- bounded concurrent DAG swarms with failure gating
- one shared seven-agent catalog used by PTY launch, doctor, init, MCP, and
  headless adapters
- process-tree cancellation, useful stderr propagation, runtime timeout, and
  persisted resolved agent/isolation

Important next steps:

1. Put the control semantics behind one wing-owned local socket instead of
   discovering per-egg sockets in each client.
2. Add asynchronous MCP task support so long runs can be polled/cancelled
   without holding one tool call open.
3. Add structured agent events using native hooks first and terminal heuristics
   only as a fallback.
4. Extend prompt assets with output schemas, model policy, composition, and
   portable project selectors; named/versioned templates and run provenance are
   already present.
5. Add budgets for wall time, tokens, spend, retries, and tool authority.
6. Add pause/approve/resume nodes and human-visible control leases.
7. Run the same API over SSH/direct/P2P/relay transports with explicit grants.
8. Build a TUI/browser graph view only after the runtime semantics stabilize.

The key product test is simple: can an LLM safely operate Wingthing without
pretending the web UI is an API, and can a human see and take over everything it
did? This branch establishes the first honest “yes.”
