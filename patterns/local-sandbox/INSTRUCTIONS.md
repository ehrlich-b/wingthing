# Use Wingthing as a local sandbox

Install Wingthing once:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
```

From the project directory, inspect the sandbox that will apply:

```sh
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

The egg survives a dropped terminal. List and reattach later:

```sh
wt attach
wt attach <session-id-or-name>
```
