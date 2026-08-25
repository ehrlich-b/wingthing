# Orchestrate work across remote roosts

This is client-side composition, not roost federation. Add each independent
self-hosted roost as a separate MCP server. The MCP server name is the explicit
execution target visible to the parent LLM.

```sh
codex mcp add build-roost --url https://build.example.com/mcp
codex mcp add gpu-roost --url https://gpu.example.com/mcp
codex mcp login build-roost
codex mcp login gpu-roost
```

The same pattern works in Claude Code:

```sh
claude mcp add --scope user --transport http build-roost https://build.example.com/mcp
claude mcp add --scope user --transport http gpu-roost https://gpu.example.com/mcp
```

Choose the roost by its MCP server name. Call `wingthing_capabilities` on that
server before starting work. The authenticated user owns the work created
through that connection.

Each `cwd` must already exist on the selected roost's embedded wing. Wingthing
does not discover peer roosts, copy workspaces, reconcile durable memory, or
merge identities across portals. Use distinct names and treat each portal's
OAuth identity, capabilities, and resource IDs as a separate trust boundary.
