---
name: patterns
description: Choose the smallest supported local-first Wingthing route for an agent or person, then add remote or browser access only when required.
---

# Wingthing patterns

Choose the first supported route that satisfies the request:

1. **Local agent control with stdio MCP:** let the current Claude or Codex
   session manage local sub-agents on existing code and provider logins. Read
   [local sub-agents](local-subagents/INSTRUCTIONS.md).
2. **Local sandboxed agent terminal for a person:** run and later reattach to an
   agent on this computer. Read [local sandbox](local-sandbox/INSTRUCTIONS.md).
3. **Direct remote MCP when machines differ:** let one AI explicitly select and
   manage agents on several computers. Read
   [remote orchestration](remote-orchestration/INSTRUCTIONS.md).
4. **Self-hosted roost when a person needs a browser:** for one person's remote
   machine, read [personal remote wing](personal-remote-wing/INSTRUCTIONS.md). For
   a team browser host, read [shared web roost](shared-web-roost/INSTRUCTIONS.md).
   To connect an AI to that private roost, read
   [shared roost agents](shared-roost-agents/INSTRUCTIONS.md).
5. **Optional entitled hosted relay:** only when the account already has
   hosted-relay access and the selected wing allows it, read
   [hosted browser wing](hosted-browser-wing/INSTRUCTIONS.md).

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
