# The AI API surface

Status: implemented local slice and target design
Reviewed: 2026-08-21

The goal from `CLAUDE.md`: **an AI must be able to orchestrate wingthing as easily
as a human can.** This doc answers three questions honestly — what surfaces exist
today, why they do not add up to that goal, and what the target shape is.

## What exists today

There are four surfaces. Local stdio MCP and authenticated shared-roost HTTP MCP
now share the typed terminal, agent-run, sandbox, and message vocabulary. The
REST and encrypted external-wing surfaces still use separate contracts.

| # | Surface | Transport | Auth | What it can do |
|---|---------|-----------|------|----------------|
| 1 | `wt mcp stdio` (`cmd/wt/mcp_local.go`) | stdio, local only | OS user plus optional owner, actor, grants, and bounds | Agent orchestration, terminals, messages, prompts, loops, swarms |
| 2 | `POST /mcp` (`internal/relay/mcp.go`) | HTTP | OAuth 2.0, dynamic client registration, owner-scoped native controls, role-scoped executable tools, audit observer | Shared-roost terminals, agent runs, messages, sandbox explanation, and configured privileged tools |
| 3 | REST `/api/...` (`internal/relay/`) | HTTP | session cookie / bearer | Account, usage, passkeys, ntfy, orgs, and an authorized online-wing roster |
| 4 | Encrypted tunnel (`internal/ws/`) | WebSocket, application-encrypted through relay | passkey + device token | `dir.list`, `sessions.list`, `sessions.history`, `pty.*`, `egg.config_update`, … |

For pre-isolated VMs, the CLI and local MCP adapters share an explicit trusted
outer-boundary mode. It is selected at CLI/MCP-server startup, reported through
capabilities and session JSON, and included in the MCP audit trail; a model
cannot toggle it per call.

### The 27 local MCP tools (surface 1)

`wingthing_capabilities`, `message_send`, `message_list`, `message_wait`,
`sandbox_explain`, `terminal_list`, `terminal_read`,
`terminal_send`, `terminal_wait`, `terminal_start`, `agent_start`,
`agent_run`, `agent_status`, `agent_wait`, `agent_result`, `agent_events`,
`agent_steer`, `agent_stop`, `terminal_rename`, `terminal_stop`, `prompt_list`,
`prompt_get`, `prompt_save`, `prompt_run`, `task_get`, `prompt_loop`, `swarm_run`.

### The problems

1. **Control semantics still live in the stdio adapter.** Surface 2 wraps the
   same typed operations in-process and supplies authenticated owner/actor
   identity, which proves shared-roost parity. Extracting `internal/control`
   remains the maintainability step that gives CLI, stdio, HTTP, and future REST
   one implementation.
2. **There is no REST API for agent orchestration at all.** Surface 3 is account
   plumbing. `GET /api/app/wings` deliberately returns routing identity rather
   than host/project details, because the relay is a dumb pipe and knows nothing
   else. `wt wings` composes that roster with encrypted `wing.info` probes.
3. **Surface 4 is the real wing API, but only the browser is a general client.**
   The native CLI has a pinned-key tunnel client for discovery probes and session
   sync; browser capabilities such as directory listing, remote terminal
   lifecycle, session history, and configuration are still bespoke encrypted
   messages rather than a supported general CLI/API surface. That is still a
   UI-shaped API.
4. **External wings still lack the typed control transport.** Shared roosts call
   the embedded runtime directly. A hosted relay connected to a separate wing
   still needs these operations carried through the encrypted tunnel.

The remaining parity gap is external-wing reachability plus extraction of the
shared semantics into a transport-independent package.

## Target shape

One control plane, three adapters, one vocabulary.

```text
                   ┌──────────────────────────────────┐
   CLI (--json) ──▶│                                  │
   MCP (stdio+HTTP)│   wing control plane (semantics) │──▶ eggs / tasks / prompts
   REST (/api/v1) ─▶│   principals · grants · bounds   │
                   └──────────────────────────────────┘
```

The rule: **a capability is defined once in the control plane and exposed by all
three adapters.** Adding a verb means adding it to one place and getting a CLI
subcommand, an MCP tool, and a REST route from the same definition. If an adapter
cannot express a capability, that is a bug in the adapter, not a reason to have
three vocabularies.

### Why both MCP and REST

They are not redundant; they serve different callers.

- **MCP** is for a model in a loop. Tools carry closed JSON Schemas and
  read-only/mutating/destructive annotations, so a model can discover and reason
  about them. It is the right shape for "an agent is deciding what to do next."
- **REST** is for everything that is not a model in a loop: scripts, CI, the web
  UI, a mobile client, a future TUI, curl at 2am. It needs stable resource URLs,
  ordinary status codes, pagination, and no session handshake.

The web UI should migrate onto the same REST surface it exposes to everyone else.
That is the forcing function that keeps the two honest — surface 4's tunnel
messages become REST resources carried over the encrypted transport, rather than
a private protocol.

### One auth model

Surface 2 already has the right ingredients: OAuth bearer, role-scoped policy,
audit observer, per-call identity env. That model should extend to the whole
control plane rather than living only on the privileged-tool path.

Every model-reachable action needs a principal, a grant, a bound (time,
iterations, concurrency), and a log line. Local stdio keeps its
local-user-authority shortcut, but it must be the same *authorization* code path
with the principal pre-resolved — not a separate ungoverned door.

## Sequencing

1. Extract the control-plane semantics out of `cmd/wt/mcp_local.go` into a package
   both the CLI and the servers call.
2. Put it behind the wing-owned local socket (P1 in `local-first-architecture.md`),
   so clients stop doing per-egg filesystem discovery.
3. Add `/api/v1` over the same core; migrate one tunnel message at a time.
4. Serve MCP over HTTP from the same core, reusing surface 2's OAuth and policy.
5. Retire the bespoke tunnel inner-message vocabulary as REST covers it.

Nothing here requires new product surface. It is the same capabilities reachable
by callers who are not a browser.
