# Give a local LLM sandboxed sub-agents

This pattern keeps execution, workspace, credentials, and memory on the current
machine. The parent LLM drives Wingthing through a local stdio MCP server.

Install Wingthing, then add its MCP server to the client.

Codex:

```sh
codex mcp add wingthing -- wt mcp stdio --client codex
```

Claude Code:

```sh
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

Restart the client after adding the server. Ask it to call
`wingthing_capabilities` before starting work. A useful first request is:

```text
Use Wingthing to run a Codex agent in this project. Return the run ID, wait for
completion, and summarize its result.
```

Agent runs accept a provider name, model, existing working directory, prompt,
and label. The returned run ID supports status, wait, result, steering, and
stop. Wingthing does not copy the workspace or durable memory to another host.

Keep project policy in `egg.yaml`. Check the resolved boundary with:

```sh
wt doctor
wt egg explain codex --json
```

Use `agent_run` for a headless task. Use `agent_start` when a person should be
able to attach to the persistent terminal. If the local wing is connected to a
portal, that terminal appears in the browser; a headless run does not yet.
