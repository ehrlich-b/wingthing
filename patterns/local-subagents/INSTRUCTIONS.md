# Let your current AI launch local sub-agents

Use this setup when a parent Claude or Codex session should delegate work to other
agents on the same computer. Wingthing gives the parent typed tools to start, wait
for, inspect, steer, and stop those child agents.

## Add Wingthing to the parent AI

Install Wingthing, then add its local MCP server.

Codex:

```sh
codex mcp add wingthing -- wt mcp stdio --client codex
```

Claude Code:

```sh
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

Restart the parent client after adding the MCP server.

## Try it

Give the parent a concrete first request:

```text
Use Wingthing to run a Codex agent in this project. Return the run ID, wait for it
to finish, and summarize its result.
```

The parent should call `wingthing_capabilities` before starting work. It can use:

- `agent_run` for a headless task with a final result; or
- `agent_start` for a persistent terminal that a person can attach to.

Each launch names an installed agent, optional model, existing working directory,
prompt, and label. The returned run ID supports status, wait, result, events,
steering, and stop.

## Sandbox and storage

Child agents use the local project's `egg.yaml` policy. Check the effective boundary
with:

```sh
wt doctor
wt egg explain codex --json
```

Wingthing does not copy the project or durable agent memory to another computer.
Headless runs do not yet appear in the human browser session list.
