# Orchestrate work on remote hosts

Add each remote roost as a separate MCP server. The name becomes the runtime
selector visible to the AI.

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

Choose the runtime by its MCP server name. The authenticated user owns every egg
created through that connection. Use a workspace allowed by the selected host,
submit the run, then use the returned ID for wait, result, steering, or stop.
