---
name: patterns
description: Select and set up a Wingthing pattern by choosing the driver, execution wing, workspace, display, credentials, and durable memory.
---

# Wingthing patterns

Choose a pattern first by who drives the work, then by where it executes.

- Human, current machine: read
  [local sandbox](local-sandbox/INSTRUCTIONS.md).
- LLM, current machine: read
  [local sub-agents](local-subagents/INSTRUCTIONS.md).
- Human, personal remote machine: read
  [personal remote wing](personal-remote-wing/INSTRUCTIONS.md).
- Human, shared host: read
  [shared web roost](shared-web-roost/INSTRUCTIONS.md).
- LLM, shared host: read
  [shared roost agents](shared-roost-agents/INSTRUCTIONS.md).
- Human or LLM, several independent remote roosts: read
  [remote orchestration](remote-orchestration/INSTRUCTIONS.md).

The missing pattern is deliberate: an HTTP MCP client connected to a portal
cannot yet select one of that portal's external wings. Do not present personal
remote LLM control through `wingthing.ai` as shipped.

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
