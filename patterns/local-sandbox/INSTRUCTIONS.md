# Run a durable, sandboxed agent on this computer

Use this setup when you want to run Claude, Codex, or another agent in a local
project. No Wingthing account or server is required.

Wingthing applies the project's sandbox policy and keeps the terminal alive if you
close the window or lose the connection.

## Set it up

Install Wingthing once:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
```

Open the project directory and inspect the sandbox before starting an agent:

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
