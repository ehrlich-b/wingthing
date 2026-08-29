# Run a durable, sandboxed agent terminal on this computer

Use this setup when a person wants a local sandboxed Claude, Codex, or other agent
terminal. If an existing parent agent should drive the work instead, use the
[local stdio MCP setup](../local-subagents/INSTRUCTIONS.md). No Wingthing account
or server is required.

Wingthing applies the project's sandbox policy and keeps the terminal alive if you
close the window or lose the connection.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | This computer. The local CLI starts the egg directly; a wing daemon, portal, and hosted account are optional. |
| **Workspace** | An existing project directory on this computer. Run the command from that directory; Wingthing does not create, clone, or synchronize it. |
| **Display** | A persistent terminal opened by `wt egg` and resumed with `wt attach`. If this computer is also connected to a relay-entitled hosted portal or a self-hosted roost, the same terminal can appear there. |
| **Provider credentials** | The selected agent CLI uses the current OS user's provider login on this computer. Authenticate that CLI here; do not pass provider tokens on the command line or in a prompt. |
| **Durable memory** | Egg and terminal state stay under this computer's `~/.wingthing/eggs` (or `WINGTHING_DIR`). Provider-native history stays in its local agent home, and optional Wingthing prompt memory stays under `~/.wingthing/memory`. None of it is copied elsewhere automatically. |

## Set it up

Install Wingthing once:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
```

Authenticate the provider CLI if needed, then open the existing project directory
and inspect the sandbox before starting an agent:

```sh
cd /path/to/project
wt doctor
wt egg explain claude --json
```

Start Claude or Codex:

```sh
wt egg claude
wt egg codex
```

Arguments after `--` go to the provider CLI, including model selection:

```sh
wt egg claude -- --model sonnet
wt egg codex -- -m gpt-5.6-terra
```

## Reconnect later

Closing the terminal does not stop the agent. List running sessions and reattach:

```sh
wt attach
wt attach <session-id-or-name>
```

Keep project-specific filesystem, network, and resource rules in `egg.yaml`. If this
computer is also connected to an entitled hosted portal or a self-hosted roost, its
persistent terminal sessions appear there too.
