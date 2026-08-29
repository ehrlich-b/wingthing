# Let an AI control agents on your private roost

Use this setup when a local Claude or Codex session should manage agents on your
self-hosted Wingthing server. Workspaces, child-agent credentials, and processes stay
on the roost server.

## Before you start

The roost must have a valid HTTPS URL and OAuth login. Your account must be enrolled:
normally its exact email is listed in `WT_ROOST_ALLOWED_EMAILS`, or the OAuth
provider or ingress enforces the same membership boundary. This guide controls the
agent runtime built into that roost; it does not select unrelated external wings.

## Add the roost to the parent AI

Codex:

```sh
codex mcp add wingthing-roost --url https://roost.example.com/mcp
codex mcp login wingthing-roost
```

Claude Code:

```sh
claude mcp add --scope user --transport http wingthing-roost https://roost.example.com/mcp
```

Complete the browser login as the person who should own the work. Then ask the
parent to call `wingthing_capabilities` and use a working directory allowed by the
roost.

## Use it

The parent AI can start and supervise that person's durable runs and terminals on
the roost server. If a child agent reports `auth_required`, open the returned login
session and complete that provider's login there.

Model choice is an `agent_run` parameter. Clients logged in as the same person can
also exchange durable Wingthing messages: send with `message_send`, wait with
`message_wait`, and carry the returned cursor into the next wait. Message bodies stay
out of audit logs.

The HTTP MCP endpoint controls only this roost's built-in agent runtime. To let one
parent select several external computers registered with a coordinator, use the
[several-computer AI setup](../remote-orchestration/INSTRUCTIONS.md).
