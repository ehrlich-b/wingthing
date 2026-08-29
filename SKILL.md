---
name: wingthing
description: Use Wingthing local-first to start and supervise coding agents through typed MCP, then add remote machines or human-visible terminals only when needed.
---

# Use Wingthing

## Choose the smallest route

Use this order. Do not start with a hosted service when a local route satisfies
the task.

1. **Local agent control:** register `wt mcp stdio --client NAME` with the parent
   agent. It manages agents on this computer using existing workspaces and the
   current OS user's existing provider logins. No Wingthing account or daemon is
   required.
2. **Local human terminal:** run `wt egg AGENT` from an existing project and
   resume it with `wt attach`. This is the local sandboxed agent-terminal route.
3. **Direct remote MCP:** use `wt mcp connect` when parent and child execution are
   on different machines. The connector machine must run `wt login`; every execution machine must run `wt login` and `wt start`, already contain the
   workspace, and have the provider CLI authenticated. Calls select an explicit
   `wing_id`, travel over direct WebRTC, and never fall back to hosted relay.
4. **Self-hosted browser:** when a person needs browser visibility, start with
   `wt roost start --https`. A self-hosted roost does not require a wingthing.ai hosted-relay entitlement. For a remote wing, use the documented localhost
   roost plus SSH tunnel; for a shared roost, require OAuth, HTTPS, and explicit
   enrollment.
5. **Optional hosted browser:** use `app.wingthing.ai` only when the account has
   hosted-relay access and the selected wing's effective `hosted_relay` policy is
   `allow`. This path requires both account-level hosted-relay access and wing
   permission; `hosted_relay: deny` wins.

An authenticated self-hosted roost's HTTP MCP endpoint controls that roost's
embedded wing. It does not join independent roosts into one inventory.

## Place the work first

Before launching, identify:

- the execution wing;
- an existing `cwd` on that wing;
- whether the human display is a terminal, browser terminal, or no live display;
- which Wingthing owner and provider-agent home supply credentials; and
- which wing holds the task record, provider history, and optional Wingthing memory.

Wingthing routes control. It does not create or synchronize workspaces, credentials,
or memory across wings.

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
