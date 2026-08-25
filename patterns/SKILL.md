---
name: patterns
description: Set up a supported Wingthing workflow for local agents, remote machines, or a private shared roost.
---

# Wingthing patterns

Choose the outcome the user wants. Only offer the supported setups below.

- Run a durable sandboxed agent on this computer: read
  [local sandbox](local-sandbox/INSTRUCTIONS.md).
- Let the current Claude or Codex session launch local sub-agents: read
  [local sub-agents](local-subagents/INSTRUCTIONS.md).
- Let one AI manage agents on several computers: read
  [remote orchestration](remote-orchestration/INSTRUCTIONS.md).
- Control a remote agent from a localhost browser through a self-hosted roost and
  SSH tunnel: read
  [personal remote wing](personal-remote-wing/INSTRUCTIONS.md).
- Give a team a private browser-based agent host: read
  [shared web roost](shared-web-roost/INSTRUCTIONS.md).
- Let an AI control agents on a private roost: read
  [shared roost agents](shared-roost-agents/INSTRUCTIONS.md).

Do not present planned features as patterns. In particular, Wingthing does not
currently merge independent roosts into one inventory, provide browser-direct free
terminals, prepare Git worktrees atomically, or expose remote schedules and delivery
targets.

For every pattern, state these six placements before changing the host:

1. Driver: human or LLM.
2. Execution: the wing that owns the process.
3. Workspace: the path and replica available on that wing.
4. Display: terminal, browser terminal, or preview route.
5. Credentials: the owner-scoped agent home or delegated credential.
6. Memory: the durable project record and where it is replicated.

Current Wingthing routes execution. It does not synchronize a workspace or
durable memory between wings. A `cwd` must already exist on the selected wing.
Say this plainly when a pattern crosses machines.

Run `wt doctor` before changing a host. Use `wt egg explain <agent> --json` to
inspect the effective sandbox. Ask MCP clients to call
`wingthing_capabilities` before starting work.

Keep identities separate on shared hosts. The Wingthing login selects the egg
owner. Claude, Codex, and other provider logins live in that owner's agent home.
Never place provider tokens in MCP arguments, checked-in config, logs, or copied
instructions. Preserve existing web-roost users while changing programmatic
access unless the user has agreed to a migration.
