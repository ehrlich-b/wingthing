# Let an LLM use a shared roost

This pattern lets a local parent LLM control the embedded wing of a self-hosted
roost. The workspace, child-agent credentials, and durable memory must already
exist on that host.

The roost needs a public HTTPS URL and OAuth login. Add its Streamable HTTP MCP
endpoint to the local AI client.

Codex:

```sh
codex mcp add wingthing-roost --url https://roost.example.com/mcp
codex mcp login wingthing-roost
```

Claude Code:

```sh
claude mcp add --scope user --transport http wingthing-roost https://roost.example.com/mcp
```

Complete the browser login as the person who will own the work. Call
`wingthing_capabilities`, then use a `cwd` allowed by the roost. The LLM can
start and supervise that person's runs on the embedded wing.

Provider login is separate. If Claude or Codex reports `auth_required`, open
the returned login session and complete the provider login there. Select models
through the `model` field on an agent run, such as `opus` or `gpt-5.6-terra`.

Clients logged in as the same person can coordinate through durable roost
messages. Use `message_send` with a shared `channel`; omit `to_actor` to reach
the owner's other clients. The receiving client calls `message_wait`, then
passes `next_after_id` as `after_id` on its next wait. Message bodies stay out
of audit logs.

Current limit: the HTTP MCP endpoint controls the roost's embedded wing. It
cannot select external wings that also appear in the browser portal.
