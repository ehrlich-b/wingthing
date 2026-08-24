---
name: patterns
description: Select and set up a Wingthing pattern based on who drives the work, where eggs run, and whether the host is personal or shared.
---

# Wingthing patterns

Choose one pattern from the user's actor and runtime. Read only that pattern's
instructions, then follow them using the current Wingthing version.

- Human driving an egg on the current machine: read
  [local sandbox](local-sandbox/INSTRUCTIONS.md).
- Codex or Claude launching sub-agents on the current machine: read
  [local sub-agents](local-subagents/INSTRUCTIONS.md).
- Human reaching a personal machine through a browser: read
  [personal remote wing](personal-remote-wing/INSTRUCTIONS.md).
- Several people using one host through the web UI: read
  [shared web roost](shared-web-roost/INSTRUCTIONS.md).
- Codex or Claude launching owner-scoped work on a shared host: read
  [shared-roost agents](shared-roost-agents/INSTRUCTIONS.md).
- A person or agent selecting among remote runtimes: read
  [remote orchestration](remote-orchestration/INSTRUCTIONS.md).

Keep identities separate on shared hosts. The Wingthing login selects the egg
owner. Claude, Codex, and other provider logins live in that owner's agent home.
Never place provider tokens in MCP arguments, checked-in config, logs, or copied
instructions.

Run `wt doctor` before changing a host. Use `wt egg explain <agent> --json` to
inspect the effective sandbox. Preserve existing web-roost users while changing
programmatic access unless the user has agreed to a migration.
