---
name: wingthing
description: Use Wingthing's typed control plane to start and supervise coding agents in durable runs or terminals on local and remote wings.
---

# Use Wingthing

## Place the work first

Before launching, identify:

- the execution wing;
- an existing `cwd` on that wing;
- whether the human display is a terminal, browser terminal, or no live display;
- which Wingthing owner and provider-agent home supply credentials; and
- which wing holds the task record, provider history, and optional Wingthing memory.

Wingthing routes control. It does not create or synchronize workspaces, credentials,
or memory across wings.

## Choose the remote transport deliberately

- Native remote MCP uses `wt mcp connect`. The connector machine must run
  `wt login`; every execution machine must run `wt login` and `wt start`, have the
  requested provider CLI installed and authenticated for the execution owner, and
  already contain the requested workspace. This path is direct WebRTC and never
  falls back to the hosted relay.
- A hosted browser terminal requires both account-level hosted-relay access and an
  execution wing whose effective `hosted_relay` policy is `allow` (the compatible
  default). `hosted_relay: deny` overrides an otherwise entitled account until the
  wing is restarted with the policy changed.
- A self-hosted roost does not require a wingthing.ai hosted-relay entitlement. Its
  operator controls enrollment and relay policy, and its HTTP MCP endpoint controls
  that roost's embedded wing rather than joining independent roosts into one
  inventory.

## Run safely

1. Call `wingthing_capabilities` before relying on an operation or agent.
2. On `wt mcp connect`, call `wing_list` and pass the chosen `wing_id` to every
   wing-owned operation. Never rely on a current or default remote wing.
3. Call `sandbox_explain` for the selected agent and `cwd` before starting work.
4. Use `agent_run` when the caller needs semantic status and a final result. Keep
   its `run_id`, wait with `agent_wait`, and read completion with `agent_result`.
5. Use `agent_start` only when an interactive PTY and later human attachment are
   part of the task. Keep its session ID; do not infer semantic completion from
   terminal text.
6. Stop or steer only the exact run or session ID returned by the selected wing.

## Boundaries

- Put no provider tokens, SSH keys, or other secrets in prompts or MCP arguments.
  Authenticate the provider CLI in the execution owner's agent home.
- A terminal survives client detachment. A run's record and result persist, but
  active headless execution still requires its supervising Wingthing process.
- Headless runs do not currently have a browser view. Start a terminal when human
  takeover is required.
- Direct remote MCP rejects locked wings and never silently switches to the hosted
  relay. Use an unlocked authorized wing, SSH, or a self-hosted roost as appropriate.
- Treat `~/.wingthing/wt.db`, `~/.wingthing/memory/`, provider history, and the
  workspace as wing-local state unless the operator has arranged replication.
