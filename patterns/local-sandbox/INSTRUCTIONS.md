# Use Wingthing as a local sandbox

This pattern places the human, wing, workspace, display, credentials, and
memory on the current machine. Wingthing isolates the agent process and keeps
its terminal alive.

Install Wingthing once:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
```

From the project directory, inspect the sandbox that will apply:

```sh
wt doctor
wt egg explain claude --json
```

Start the agent:

```sh
wt egg claude
```

Model selection passes through to the provider CLI:

```sh
wt egg claude -- --model opus
wt egg codex -- -m gpt-5.6-terra
```

The session survives a dropped terminal. List and reattach later:

```sh
wt attach
wt attach <session-id-or-name>
```

If this wing is connected to a portal, the same live session appears in its
browser session list. Headless `agent_run` tasks do not yet appear there.
