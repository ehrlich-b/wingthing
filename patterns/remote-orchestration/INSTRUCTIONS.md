# Let one AI manage agents on several computers

Use this setup when one parent Claude or Codex session should run durable agents on
several machines—for example, a home computer and an office VM.

After setup, the parent AI can:

- list the computers currently online;
- choose exactly which computer should do each task;
- start, inspect, steer, and stop agents there; and
- reconnect to work that kept running after the parent disconnected.

Agent-control payloads travel over a direct encrypted connection to the selected
computer. `wingthing.ai` handles login, the authorized computer list, and connection
setup; it does not proxy free-tier MCP payloads. No execution computer needs an
inbound port.

## 1. Connect every execution computer

On each computer that should run agents:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
wt wing status
```

Log every computer into the same Wingthing account. Install and authenticate the
agent CLIs you intend to use on each computer. Projects must already exist on the
computer where they will run.

## 2. Connect the parent AI

On the computer where you run the parent Claude or Codex session, install Wingthing
and log into the same account. Then add the native connector.

Codex:

```sh
codex mcp add wingthing -- wt mcp connect --client codex
```

Claude Code:

```sh
claude mcp add --scope user wingthing -- wt mcp connect --client claude
```

Restart the parent client after adding the MCP server.

## 3. Use it

Ask the parent AI:

```text
Use Wingthing to list my online computers. Show me their IDs and installed agents,
then ask which computer and existing project directory should run the task.
```

The AI first calls `wing_list`. Every later operation names the returned `wing_id`,
so work cannot silently drift to a different machine. Use `agent_run` for a headless
result or `agent_start` for a persistent terminal a person can attach to later.

## Current limits

- Wingthing does not copy projects, untracked files, credentials, or memory between
  computers.
- The requested `cwd` must already exist on the selected computer.
- Direct connections currently work best on a shared LAN or tailnet unless ICE
  servers are configured.
- Locked or per-user-passkey-protected wings reject native control until the native
  connector supports their passkey ceremony.
- The connector does not silently fall back to hosted payload relay.

To require this computer to accept direct control only:

```sh
wt wing config set hosted_relay=deny
wt stop
wt start
```
