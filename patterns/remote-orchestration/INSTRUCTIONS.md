# Orchestrate work across remote wings

This pattern gives one parent LLM an access-filtered inventory of personal or
organization wings registered with the same coordinator. MCP payloads travel
over a direct WebRTC connection to the selected wing; the coordinator handles
login, directory lookup, key exchange, and signaling.

Install Wingthing on every execution host, log each wing into the same portal,
and keep the wing daemon running:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
```

On the machine running the parent agent, log in once and register the native
connector as a local MCP process.

```sh
# Codex
codex mcp add wingthing -- wt mcp connect --client codex

# Claude Code
claude mcp add --scope user wingthing -- wt mcp connect --client claude
```

Ask the parent to call `wing_list`. Every wing-owned tool call must include the
returned `wing_id`; there is no mutable current-wing state. Use `agent_run` for
a semantic headless result or `agent_start`/`terminal_start` for a persistent
session that a person may later inspect.

Each `cwd` must already exist on the selected wing. A parent can use a terminal
to run a bounded project-setup or worktree script and then launch an agent in the
resulting path, but Wingthing does not yet expose an atomic typed worktree object.
It does not synchronize workspaces or durable memory between wings.

The first native release expects a shared LAN or tailnet unless ICE servers are
configured. It never silently falls back to hosted payload relay. Locked and
per-user-passkey-protected wings reject native control until the connector has a
passkey ceremony. A wing owner can require direct-only operation with:

```sh
wt wing config set hosted_relay=deny
wt stop && wt start
```
