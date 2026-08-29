# Let an AI control agents on your private roost

Use this setup when a local Claude or Codex session should manage agents on your
self-hosted Wingthing server. Workspaces, child-agent credentials, and processes stay
on the roost server.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | The private roost's built-in wing. Its HTTP MCP endpoint does not select unrelated external wings. |
| **Workspace** | An existing server-side directory allowed by the roost's path policy and passed as `cwd`. The roost does not clone or synchronize it from the parent machine. |
| **Display** | Use `agent_run` for semantic status and a final result with no browser view. Use `agent_start` for a persistent PTY that the parent can control and an enrolled person can open in the self-hosted browser. |
| **Provider credentials** | The provider-agent home belonging to the Wingthing account that authenticated the HTTP MCP client. Each account must complete its own provider login on this roost. |
| **Durable memory** | The roost server keeps that owner's tasks, results, messages, sessions, optional Wingthing prompt memory, and provider history. None of it follows the parent client to another machine or another independent roost. |

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
parent to call `wingthing_capabilities`, inspect the selected agent and existing
`cwd` with `sandbox_explain`, and use a working directory allowed by the roost. Use
the same Wingthing account that completed the child provider login on this roost.
Separate Wingthing accounts deliberately have separate provider homes.

## Use it

The parent AI can start and supervise that person's durable runs and terminals on
the roost server. If a child agent reports `auth_required`, open the returned login
session and complete that provider's login there.

Model choice is an `agent_run` parameter. Keep its returned run ID, wait with
`agent_wait`, and read the final result with `agent_result`. Clients logged in as the
same person can also exchange durable Wingthing messages: send with `message_send`,
wait with `message_wait`, and carry the returned cursor into the next wait. Message
bodies stay out of audit logs.

The HTTP MCP endpoint controls only this roost's built-in agent runtime. To let one
parent select several external computers registered with a coordinator, use the
[several-computer AI setup](../remote-orchestration/INSTRUCTIONS.md).
