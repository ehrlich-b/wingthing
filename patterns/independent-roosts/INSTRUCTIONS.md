# Compose several independent roosts

Independent self-hosted roosts do not federate directories or identities yet.
Add each roost as a separately named HTTP MCP server. The MCP server name becomes
the explicit execution target visible to the parent LLM.

```sh
# Codex
codex mcp add office-roost --url https://office.example.com/mcp
codex mcp add home-roost --url https://home.example.com/mcp
codex mcp login office-roost
codex mcp login home-roost

# Claude Code
claude mcp add --scope user --transport http office-roost https://office.example.com/mcp
claude mcp add --scope user --transport http home-roost https://home.example.com/mcp
```

Call `wingthing_capabilities` on the chosen server before starting work. Each
connection has its own OAuth identity, policy, embedded wing, and resource-ID
namespace. The authenticated person owns work created through that connection.

This produces one parent-agent view through client-side composition, not one
human browser inventory. Roost peering still requires shared directory,
authorization, revocation, and conflict rules that are not implemented.
