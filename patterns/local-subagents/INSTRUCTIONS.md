# Let your current AI launch local sub-agents

Start here when a parent Claude or Codex session should delegate work to agents on
the same computer. Local stdio MCP uses the code and provider logins already on
this machine; it needs no Wingthing account, daemon, roost, or hosted relay.
Wingthing gives the parent typed tools to start, wait for, inspect, steer, and stop
those child agents.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | The same computer as the parent agent. `wt mcp stdio` uses the current OS user's local authority and does not require an account or daemon. |
| **Workspace** | An existing local project directory passed as `cwd`. If omitted, the MCP server's current directory is used; Wingthing does not prepare or synchronize a workspace. |
| **Display** | Use `agent_run` for semantic status and a final result with no live browser view. Use `agent_start` for a persistent PTY that a person or the parent agent can inspect and that a person can resume with `wt attach`. |
| **Provider credentials** | Child CLIs use the current OS user's provider logins on this computer. Authenticate each provider locally and never put its token in prompts or MCP arguments. |
| **Durable memory** | Run, result, message, and thread records stay in this computer's `~/.wingthing/wt.db`; PTYs stay under `~/.wingthing/eggs`, Wingthing prompt memory under `~/.wingthing/memory`, and provider history in the local agent home. `WINGTHING_DIR` changes the Wingthing state root. |

## Add Wingthing to the parent AI

Install Wingthing, then add its local MCP server.

Codex:

```sh
codex mcp add wingthing -- wt mcp stdio --client codex
```

Claude Code:

```sh
claude mcp add --scope user wingthing -- wt mcp stdio --client claude
```

Restart the parent client after adding the MCP server.

## Try it

Give the parent a concrete first request:

```text
Use Wingthing to run a Codex agent with cwd=/absolute/path/to/this/existing/project.
Return the run ID, wait for it to finish, read its result, and summarize it.
```

The parent should call `wingthing_capabilities` before starting work. It can use:

- `agent_run` for a headless task with a final result; or
- `agent_start` for a persistent terminal that a person can attach to.

Each launch names an installed agent, optional model, existing working directory,
prompt, and label. `agent_run` returns a run ID for status, wait, result, events,
steering, and stop. `agent_start` instead returns a session ID for terminal read,
send, wait, rename, and stop operations; terminal text is not semantic completion.

## Sandbox and storage

Child agents use the local project's `egg.yaml` policy. Check the effective boundary
with:

```sh
wt doctor
wt egg explain codex --json
```

Wingthing does not copy the project, provider history, or durable Wingthing memory
to another computer. A terminal survives client detachment; a run's durable record
and result survive, but its active headless process still depends on the supervising
Wingthing process. Headless runs do not yet appear in the human browser session list.
