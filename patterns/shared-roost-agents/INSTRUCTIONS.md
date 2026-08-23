# Let an AI use a shared roost

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

Complete the browser login as the person who will own the eggs. The AI can then
start and supervise that person's runs on the shared host. Provider login is
separate: if Claude or Codex reports `auth_required`, open the returned login egg
and complete the provider login there.

Select models through the `model` field on an agent run, such as `opus` or
`gpt-5.6-terra`.

Clients logged in as the same person can coordinate through durable roost
messages. Use `message_send` with a shared `channel`; omit `to_actor` to reach
the owner's other clients. The receiving client calls `message_wait`, then
passes `next_after_id` as `after_id` on its next wait. `wingthing_capabilities`
returns the current owner and actor IDs. Message bodies stay out of audit logs;
the audit records actor, operation, target message ID, and an argument digest.
