# The AI API surface

Status: inventory and target design
Reviewed: 2026-08-09

The goal from `CLAUDE.md`: **an AI must be able to orchestrate wingthing as easily
as a human can.** This doc answers three questions honestly — what surfaces exist
today, why they do not add up to that goal, and what the target shape is.

## What exists today

There are four surfaces. They do not share a vocabulary, an auth model, or a
transport, and only one of them can actually drive an agent.

| # | Surface | Transport | Auth | What it can do |
|---|---------|-----------|------|----------------|
| 1 | `wt mcp stdio` (`cmd/wt/mcp_local.go`) | stdio, local only | none — inherits the launching user | **All agent orchestration**: terminals, prompts, loops, swarms |
| 2 | `POST /mcp` (`internal/relay/mcp.go`) | HTTP | OAuth 2.0, dynamic client registration, role-scoped policy, audit observer | **Privileged tools only** (`egg.ToolRunner`) — the Slide shim pattern |
| 3 | REST `/api/...` (`internal/relay/`) | HTTP | session cookie / bearer | Account, usage, passkeys, ntfy, orgs, and a list of wing IDs |
| 4 | Encrypted tunnel (`internal/ws/`) | WebSocket, E2E encrypted | passkey + device token | `dir.list`, `sessions.list`, `sessions.history`, `pty.*`, `egg.config_update`, … |

### The 14 local MCP tools (surface 1)

`wingthing_capabilities`, `terminal_list`, `terminal_read`, `terminal_send`,
`terminal_wait`, `agent_start`, `terminal_stop`, `prompt_list`, `prompt_get`,
`prompt_save`, `prompt_run`, `task_get`, `prompt_loop`, `swarm_run`.

### The problems

1. **Two MCP servers with disjoint tool sets.** Surface 1 orchestrates agents but
   has no auth and no remote transport. Surface 2 has real auth and a remote
   transport but exposes privileged tools, not terminals or tasks. Neither can do
   the other's job.
2. **There is no REST API for agent orchestration at all.** Surface 3 is account
   plumbing. `GET /api/app/wings` deliberately returns only wing IDs, because the
   relay is a dumb pipe and knows nothing else.
3. **Surface 4 is the real wing API, and it has exactly one client.** Every
   capability a human uses in the browser — directory listing, session history,
   config, kill — is defined there, encrypted, undocumented, and reachable only
   by the web UI. That is the definition of a UI that is not an API.
4. **The local MCP cannot be reached remotely, and the remote surfaces cannot
   orchestrate.** So a model can drive wingthing only when it is already running
   on the same machine as the user.

The net effect: a human with a browser can do strictly more than any model can.
That fails the bar in `CLAUDE.md`.

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
