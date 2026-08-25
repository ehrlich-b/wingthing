# Give a team a private browser-based agent host

Use this setup when several people should run agents on one shared server through a
private web UI. A **roost** is Wingthing's self-hosted portal, gateway, and agent
runtime in one service.

Each signed-in person gets their own sessions and provider-agent home. Project files
and all agent processes remain on the shared server.

## Before you start

You need:

- a server with Wingthing installed;
- a public or VPN-resolvable HTTPS hostname;
- an OAuth application for GitHub or Google login; and
- one or more project directories on the server.

The TLS reverse proxy may be nginx, Caddy, or your tailnet/VPN's HTTPS proxy.

## Start the roost

Configure the public URL and one login provider:

```sh
export WT_BASE_URL=https://roost.example.com
export GITHUB_CLIENT_ID=<github-oauth-client-id>
export GITHUB_CLIENT_SECRET=<github-oauth-client-secret>
wt roost start --addr :8080
```

Terminate HTTPS at the reverse proxy and forward the hostname to port 8080. Users
then open `WT_BASE_URL` and sign in.

Declare the project roots the roost may browse in `~/.wingthing/wing.yaml`:

```yaml
paths:
  - path: /srv/workspaces
```

## User and administrator boundaries

Each user completes Claude, Codex, or other provider login inside one of their own
sessions. Later sessions reuse only that user's agent home.

Wingthing isolates users from one another at its application and sandbox layers. It
does not protect their credentials from the server or hypervisor administrator; the
team must trust whoever operates the host.
