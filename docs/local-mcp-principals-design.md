# Local MCP principals: isolating co-resident agents

Status: implemented through grants/bounds and shared-owner actors; sandbox ceiling remains
Reviewed: 2026-08-20

Companion to [MCP service accounts](mcp-service-accounts-design.md), which covers the
**remote** surface (`POST /mcp`) and unattended cloud consumers. This document covers the
**local stdio** surface (`wt mcp stdio`), whose problem is different.

## The problem

Every AI agent on a personal machine runs as the same person. Claude Code, Codex, Cursor,
and whatever gets installed next all execute as `ehrlich`, and `wt mcp stdio` grants
whoever launched it the full local control plane. So today:

- Any agent can `terminal_list` and see every other agent's sessions, with labels and
  working directories.
- Any agent can `terminal_send` into another agent's session by label.
- Any agent can `terminal_stop` another agent's session. That tool is annotated
  `destructiveHint: true` and is one string away from killing the wrong work.
- Nothing records which client did any of it. Sessions store `id`, `kind`, `agent`, `cwd`,
  and `pid` — never a creator.

The realistic failure is not malice. It is two agents that were both told "attach to the
scribe session," or a cleanup loop that stops every session it can enumerate. Ambient
authority makes that a coin flip rather than an error.

## What is and is not achievable

State this plainly, because the alternative is a feature that oversells itself.

**Not achievable:** defending against a hostile process running as the same uid. It can
read any token file, ptrace the client that holds a credential, or skip MCP entirely and
exec `wt` directly. Same-uid separation is not a security boundary, and no amount of token
plumbing makes it one. Real isolation there needs a different uid or a sandbox around the
*client*, which is a much larger change.

**Achievable, and worth doing:**

1. **Accident prevention.** A well-behaved client cannot reach another client's sessions
   because it cannot see them.
2. **Attribution.** Every mutating call names a principal in a log the owner can read.
3. **Deliberate least privilege.** A client that should only observe can be given only
   observation, and a client that should never spawn cannot spawn.

The goal is a system where cross-agent interference requires deliberately circumventing
the design, and where you can always answer "which client did that?"

## Design

### 1. Session ownership, default-deny enumeration

This is the core, and it fixes the accident case with no configuration.

- Every session records the `principal` that created it, beside `label` and `kind`.
- `terminal_list` returns only sessions owned by the calling principal.
- `terminal_read`, `terminal_send`, `terminal_wait`, and `terminal_stop` refuse a session
  owned by a different principal, with an error that names the owner rather than
  pretending the session does not exist — the caller is trusted, just not authorized.
- The human CLI (`wt session ps`, `wt attach`) sees everything. The owner of the machine
  is not one of the principals being separated; they are the one doing the separating.

A client that never learns another client's session IDs cannot stomp them by accident,
which is the entire stated worry.

### 2. Principal establishment

`wt mcp stdio --client <name>`, or `WT_MCP_CLIENT=<name>`.

- **Named client** → its own namespace, and its own grants if configured.
- **Named clients with one configured `owner`** → shared sessions, tasks, and
  messages with distinct audit actors. This is the local Codex/Claude pair
  pattern.
- **Unnamed client** → the shared `default` principal, which is exactly today's behavior.
  Backward compatible: nothing that works now breaks.
- `require_client: true` in `~/.wingthing/clients.yaml` rejects unnamed and
  unconfigured clients outright. That is the lockdown switch, opt-in. When a
  clients file already names clients, an explicit unknown name is also rejected
  so a typo cannot bypass its grant table.

Recommended setup registers each agent's MCP client under its own name, so isolation is
the consequence of ordinary configuration rather than a security ritual:

```json
{ "mcpServers": { "wingthing": {
    "command": "wt", "args": ["mcp", "stdio", "--client", "claude-code"] } } }
```

A token alongside the name may be added later. It would raise the cost of
impersonation from "type a different flag" to "read a file you can already read" —
potentially useful for attribution integrity, but still not a same-UID security boundary.

### 3. Grants and bounds

Per client, in `~/.wingthing/clients.yaml`:

```yaml
require_client: true
clients:
  claude-code:
    owner: ehrlich
    grants: [terminal.start, terminal.read, terminal.send, terminal.stop, prompt.run, message.read, message.send]
    bounds: {max_sessions: 4, max_spawns_per_hour: 20}
  codex:
    owner: ehrlich
    grants: [terminal.read, message.read, message.send]
  cron-jobs:
    grants: [prompt.run]
```

Grant names map to tools, not to transports, so the same vocabulary works when these
capabilities reach the remote surface. With no clients file, a named client gets the full
default grant set — naming yourself buys isolation, not restriction. Once a clients file
names clients, an explicit unknown name is rejected; a present client with an empty grant
list gets no tools.

Implemented bounds are `max_sessions` and `max_spawns_per_hour`. Implemented
grant names are `capabilities.read`, `sandbox.read`, `terminal.start`,
`terminal.read`, `terminal.send`, `terminal.rename`, `terminal.stop`,
`agent.run`, `agent.read`, `agent.stop`, `message.read`, `message.send`,
`prompt.read`, `prompt.save`, and `prompt.run`. The rolling per-hour spawn count
is process-local and therefore a guardrail, not a durable quota or hostile-client
security boundary.

### 4. Sandbox ceiling

Today `agent_start` calls `DiscoverEggConfig(cwd)`, so **the caller picks its own sandbox
policy by picking its working directory**. `~/.wingthing/egg.yaml` is only a fallback when
discovery fails, never a limit. A model that wants a wider sandbox just points `cwd`
somewhere with a more permissive `egg.yaml`.

`sandbox_ceiling` names a policy that a discovered `egg.yaml` may narrow but never exceed:
mounts intersect, deny paths union, network domains intersect, and isolation level may
only increase. Without this, per-client grants are decorative — a client restricted to one
directory can escape by choosing a different one.

### 5. Audit

Append-only `~/.wingthing/mcp-audit.log`, one line per call: timestamp, principal, tool,
target session, decision, and an argument digest. The current implementation records every
tool call, including reads and authorization failures.

This is the "log line" the `CLAUDE.md` authority rule requires and the only way to answer
"what did that agent do while I was away."

## Sequencing

1. Record `principal` on sessions; scope `terminal_*` to the owner. Fixes accidents alone.
2. Add `--client` / `WT_MCP_CLIENT` and the `default` principal. Backward compatible.
3. Audit log.
4. `clients.yaml` grants and bounds, plus `mcp.require_client`.
5. Sandbox ceiling, which is also the fix for caller-chosen egg policy generally.

Steps 1–3 are additive and break nothing. Step 4 is where a machine can be locked down.

As of 2026-08-20, steps 1–4 are implemented. Session creators are recorded in
`session.principal`, named MCP enumeration/control is owner-scoped, every tool
call is attributed in `~/.wingthing/mcp-audit.log`, and `clients.yaml` enforces
grants plus spawn bounds. A configured `owner` lets distinct local client actors
share those owner-scoped resources and exchange durable messages. Step 5, the sandbox ceiling/intersection algorithm,
remains a release gap: a client with `terminal.start` can still select a working
directory whose `egg.yaml` is more permissive than an administrator intended.

## Testing

Per the repository bar, at least two tiers:

- **Unit:** ownership filtering in `terminal_list`; cross-principal `send`/`stop` refused;
  grant evaluation; ceiling intersection math, including that a permissive discovered
  config cannot widen mounts, domains, or isolation.
- **Integration:** two MCP servers with distinct `--client` names against one runtime —
  client A starts a session, client B cannot see, drive, or stop it, and the audit log
  attributes every attempt to the right principal.
- **Explicitly not claimed:** any test asserting resistance to a same-uid attacker. That
  property is not offered, so no test should imply it.
