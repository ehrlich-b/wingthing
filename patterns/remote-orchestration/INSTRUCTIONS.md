# Let one AI manage agents on several computers

Use this setup only when the parent agent and execution wing are on different
machines—for example, a home computer and an office VM. On one machine, prefer
local `wt mcp stdio`.

After setup, the parent AI can:

- list the computers currently online;
- choose exactly which computer should do each task;
- start, inspect, steer, and stop agents there; and
- reconnect to work that kept running after the parent disconnected.

Agent-control payloads travel over a direct encrypted connection to the selected
computer. `wingthing.ai` handles login, the authorized computer list, and
connection setup; it does not proxy direct MCP payloads or silently switch the
connector to hosted relay. No execution computer needs an inbound port.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | The exact online wing selected from `wing_list`. Every wing-owned operation carries its `wing_id`; Wingthing does not substitute a default wing. |
| **Workspace** | An existing absolute `cwd` on that wing. The repository, revision, and untracked files must already be there because Wingthing does not clone or synchronize them. |
| **Display** | Use `agent_run` for a semantic result without a live browser view. Use `agent_start` for a persistent PTY; a person can attach on the execution wing or over SSH, and can use the hosted browser only when the account has relay access. |
| **Provider credentials** | The selected wing resolves the authenticated owner's agent home. Install and authenticate each provider CLI on every wing where it may run; credentials are not copied from the parent machine. |
| **Durable memory** | Each wing keeps its own tasks, results, messages, sessions, optional Wingthing memory, and provider history under that wing's state and agent home. The hosted coordinator supplies identity, an access-filtered directory, keys, and signaling; it does not replicate that agent state. |

## 1. Connect every execution computer

On each computer that should run agents:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
wt wing status
```

For personal wings, log every computer into the same Wingthing account. An
organization wing can instead be visible to authorized organization members; the
wing still enforces owner, role, path, grant, and spawn bounds. Install and
authenticate the agent CLIs you intend to use on each computer. Projects must
already exist on the computer where they will run.

## 2. Connect the parent AI

On the computer where you run the parent Claude or Codex session, install Wingthing
and log in using an account authorized for the execution wings, personally or
through organization membership. Then add the native connector.

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
Keep the returned run or session ID together with its owning `wing_id` for every
later operation.

## Current limits

- Wingthing does not copy projects, untracked files, credentials, or memory between
  computers.
- The requested `cwd` must already exist on the selected computer.
- Direct connections currently work best on a shared LAN or tailnet unless ICE
  servers are configured.
- Locked or per-user-passkey-protected wings reject native control until the native
  connector supports their passkey ceremony.
- The connector does not silently fall back to hosted payload relay.

If the two computers cannot reach one another on a LAN or tailnet, add your STUN
or TURN service to `~/.wingthing/wing.yaml` on each execution computer and restart
the wing. The coordinator returns this configuration only to an authorized direct
client; use short-lived TURN credentials where your provider supports them.

```yaml
ice_servers:
  - urls: ["stun:stun.example.com:3478"]
  - urls: ["turns:turn.example.com:5349"]
    username: "<turn-username>"
    credential: "<turn-credential>"
```

To require this computer to accept direct control only:

```sh
wt wing config set hosted_relay=deny
wt stop
wt start
```
