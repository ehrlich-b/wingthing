# The AI API surface

Status: implemented local slice and target design
Reviewed: 2026-08-28

The goal from `CLAUDE.md`: **an AI must be able to orchestrate wingthing as easily
as a human can.** This doc answers three questions: what surfaces exist today,
why they do not add up to that goal, and what the target shape is.

## What exists today

There are five surfaces. Local stdio MCP, native direct MCP, and authenticated shared-roost HTTP MCP
now share a versioned operation registry for typed terminal, agent-run,
sandbox, message, and wing-inventory vocabulary. The REST and encrypted
browser-tunnel surfaces still use separate runtime contracts.

| # | Surface | Transport | Auth | What it can do |
|---|---------|-----------|------|----------------|
| 1 | `wt mcp stdio` (`cmd/wt/mcp_local.go`) | stdio, local only | OS user plus optional owner, actor, grants, and bounds | Agent orchestration, terminals, messages, prompts, loops, swarms |
| 2 | `POST /mcp` (`internal/relay/mcp.go`) | HTTP | OAuth 2.0, dynamic client registration, owner-scoped native controls, role-scoped executable tools, audit observer | Authorized wing roster, shared-roost terminals, agent runs, messages, sandbox explanation, and configured privileged tools |
| 3 | REST `/api/...` (`internal/relay/`) | HTTP | session cookie / bearer | Account, usage, passkeys, ntfy, orgs, and an authorized online-wing roster |
| 4 | Encrypted tunnel (`internal/ws/`) | WebSocket, application-encrypted through relay | passkey + device token | `dir.list`, `sessions.list`, `sessions.history`, `pty.*`, `egg.config_update`, … |
| 5 | `wt mcp connect` (`cmd/wt/mcp_connect.go`) | stdio to the parent agent, authenticated WebRTC/DTLS to selected wings | device login, coordinator-filtered roster, wing-derived owner/role/grants/bounds | Qualified multi-wing terminal, run, message, and sandbox controls on unlocked wings |

For pre-isolated VMs, the CLI and local MCP adapters share an explicit trusted
outer-boundary mode. It is selected at CLI/MCP-server startup, reported through
capabilities and session JSON, and included in the MCP audit trail; a model
cannot toggle it per call.

### Local MCP operations (surface 1)

`wingthing_capabilities`, `message_send`, `message_list`, `message_wait`,
`sandbox_explain`, `terminal_list`, `terminal_read`,
`terminal_send`, `terminal_wait`, `terminal_start`, `agent_start`,
`agent_run`, `agent_status`, `agent_wait`, `agent_result`, `agent_events`,
`agent_steer`, `agent_stop`, `terminal_rename`, `terminal_stop`, `prompt_list`,
`prompt_get`, `prompt_save`, `prompt_run`, `task_get`, `prompt_loop`, `swarm_run`.

### The problems

1. **Control handlers still live in the stdio adapter.** `internal/control`
   now owns names, schemas, grants, annotations, transport availability,
   authority, and audit targeting. Surface 2 wraps the wing-owned handlers
   in-process and supplies authenticated owner/actor identity. Moving those
   handlers behind a transport-independent service remains the maintainability
   step that gives CLI, stdio, HTTP, and future REST one implementation.
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
4. **External wings now have a typed native control transport, but adapters still
   diverge.** `wt mcp connect` requires `wing_id` and carries registry operations
   directly over WebRTC. Shared-roost HTTP MCP still calls only its embedded runtime,
   and the browser still uses bespoke encrypted tunnel messages. Locked/passkey wings
   intentionally reject native direct control until that client has a ceremony.

5. **The portal has one wing roster but two runtime inventories.** Browser and
   HTTP MCP now share the access-filtered wing roster. Browser session lists
   aggregate those wings, while HTTP MCP has no `wing_id` target and calls only
   the embedded wing. Headless tasks have no browser inventory at all.

The remaining parity gap is one qualified session/run inventory across browser,
HTTP MCP, and native MCP, plus extraction of the shared semantics into a
transport-independent package.

## Target shape

One control plane, three adapters, one vocabulary.

```text
CLI --json --------\
MCP stdio or HTTP --+--> wing control plane --> sessions / runs / prompts
REST /api/v1 ------/       principal + grant + bound + audit
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
That is the forcing function that keeps the two honest: surface 4's tunnel
messages become REST resources carried over the encrypted transport, rather than
a private protocol.

### Qualified resources and placement

Every cross-wing object needs an immutable reference:

```json
{
  "portal_id": "lab",
  "wing_id": "01J...",
  "kind": "session",
  "id": "01K..."
}
```

Listing may aggregate across authorized wings. A mutating start operation must
require `wing_id` when a portal has more than one wing. Later operations should
accept the qualified reference rather than depend on a mutable current-wing
selection.

Execution target is not enough for a remote run. The contract must eventually
name the workspace replica, preview destination, credential reference, and
durable-memory source. Until those objects exist, `cwd` means an existing path
on the selected wing; Wingthing does not copy it there.

Several independent portals should remain explicit client targets in the first
version. Give each URL a local name, pin its identity, keep its OAuth and
capability cache separate, and fan out read-only lists in the client. Do not
merge identities by email or build peer-roost federation first.

### One auth model

Surface 2 already has the right ingredients: OAuth bearer, role-scoped policy,
audit observer, per-call identity env. That model should extend to the whole
control plane rather than living only on the privileged-tool path.

Every model-reachable action needs a principal, a grant, a bound (time,
iterations, concurrency), and a log line. Local stdio keeps its
   local-user-authority shortcut, but it must be the same *authorization* code path
   with the principal pre-resolved, not a separate ungoverned door.

## Sequencing

1. **In progress:** continue extracting the control handlers themselves from
   `cmd/wt/mcp_local.go`; operation names, schemas, grants, annotations, authority,
   transport availability, and audit targeting already live in `internal/control`.
2. Put the contract behind the wing-owned local socket (P1 in
   `local-first-architecture.md`), so clients stop doing per-egg filesystem discovery.
3. **Done for the current MCP adapters:** define the operation registry once and
   derive local, HTTP, and direct schemas from it.
4. **Done for the native remote subset:** carry the registry operations through an
   authenticated direct WebRTC channel. The browser still uses its bespoke encrypted
   tunnel contract.
5. **Partial:** direct MCP qualifies every wing-owned operation and result with
   `wing_id`; a single qualified session/run inventory shared by every adapter is
   still missing.
6. Migrate the browser to the shared session/run inventory.
7. Add `/api/v1` over the same core and retire bespoke tunnel messages as each
   resource is covered.

Nothing here requires new product surface. It is the same capabilities reachable
by callers who are not a browser.
