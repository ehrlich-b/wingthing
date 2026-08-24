# Give a local AI sandboxed sub-agents

Install Wingthing, then add its stdio MCP server to the AI client.

Codex:

```sh
codex mcp add wingthing -- wt mcp stdio --client codex
```

Claude Code:

```sh
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

Restart the client after adding the server. Ask it to call
`wingthing_capabilities` before starting work. Agent runs accept a provider name,
model, working directory, prompt, and label. The returned run ID supports status,
wait, result, steering, and stop operations.

Keep project policy in `egg.yaml`. Check the resolved boundary with:

```sh
wt egg explain codex --json
```
