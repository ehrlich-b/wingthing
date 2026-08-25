# Reach a personal machine from the web

This pattern places execution, the workspace, provider credentials, and durable
memory on a personal remote machine. The human uses the hosted browser portal
as the display and control surface.

Install Wingthing on the machine that will run the agents:

```sh
curl -fsSL https://wingthing.ai/install.sh | sh
wt login
wt start
```

Open `https://app.wingthing.ai`, select the wing, and start or resume a session.
The wing connects outbound to the hosted gateway, so the machine needs no
inbound port.

Check the daemon and active sessions from the machine:

```sh
wt wing status
wt attach
```

The selected project and any untracked files must already exist on the remote
machine. Wingthing does not synchronize them. The browser path is human-driven;
a parent LLM can select the same external wing through the native direct-MCP
connector described in the remote-wings pattern. The hosted HTTP MCP endpoint
still controls only its own roost's embedded wing.
