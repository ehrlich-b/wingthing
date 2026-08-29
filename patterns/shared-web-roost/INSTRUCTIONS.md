# Give a team a private browser-based agent host

Use this self-hosted browser setup when several people should run agents on one
shared server through a private web UI. A **roost** is Wingthing's portal, gateway,
and agent runtime in one service; it does not require a wingthing.ai hosted-relay
entitlement.

Each signed-in person gets their own sessions and provider-agent home. Project files
and all agent processes remain on the shared server.

## Placement and durable state

| Decision | This setup |
| --- | --- |
| **Execution wing** | The shared server's built-in roost wing. Browsers are displays; they do not run the provider process. |
| **Workspace** | An existing server-side project directory under a root allowed by `wing.yaml`. Wingthing does not create, clone, upload, or synchronize it. |
| **Display** | A persistent browser terminal, resumable after browser detachment. The server operator can also use the CLI; semantic headless runs are available through the roost's authenticated HTTP MCP workflow, not the browser session list. |
| **Provider credentials** | Each enrolled Wingthing account has a separate provider-agent home on the server and completes provider login inside one of its own sessions. Credentials are never borrowed from another account or from the browser computer. |
| **Durable memory** | Sessions, tasks, results, optional Wingthing memory, and provider history stay on the shared server under its `WINGTHING_DIR`; browser session snapshots are a separate local cache. Back up the server state and workspaces explicitly if they must survive host loss. |

## Before you start

You need:

- a server with Wingthing installed;
- a public or VPN-resolvable HTTPS hostname;
- an OAuth application for GitHub or Google login;
- the exact email addresses allowed to use the roost; and
- one or more project directories on the server.

The TLS reverse proxy may be nginx, Caddy, or your tailnet/VPN's HTTPS proxy.

## Start the roost

Configure the public URL and one login provider:

```sh
export WT_BASE_URL=https://roost.example.com
export GITHUB_CLIENT_ID=<github-oauth-client-id>
export GITHUB_CLIENT_SECRET=<github-oauth-client-secret>
export WT_ROOST_ALLOWED_EMAILS=alice@example.com,bob@example.com
wt roost start --addr :8080
```

Terminate HTTPS at the reverse proxy and forward the hostname to port 8080. Users
then open `WT_BASE_URL` and sign in. The email list is the roost's enrollment
boundary: other OAuth accounts receive a denial and cannot use old cookies,
tokens, the wing inventory, or MCP. Use exact comma-separated addresses.

OAuth by itself proves an account's identity; it does not make every account at
GitHub or Google a member of your roost. If `WT_ROOST_ALLOWED_EMAILS` is empty,
any account accepted by that provider can enroll. Set it on every
internet-reachable private roost unless your OAuth provider or ingress already
enforces the same membership boundary.

Do not add `--https` to this public/shared command. That flag is deliberately for
a single-user localhost roost: it creates a device-local CA and refuses public
listeners. Shared and organization deployments retain their existing externally
provisioned HTTPS and OAuth behavior. The enrollment list complements existing
organization roles and path policy; it does not merge owner identities, provider
homes, or workspace permissions.

Declare the project roots the roost may browse in `~/.wingthing/wing.yaml`:

```yaml
paths:
  - path: /srv/workspaces
```

## User and administrator boundaries

Each user completes Claude, Codex, or other provider login inside one of their own
sessions. Later sessions reuse only that exact Wingthing account's agent home. A
second Wingthing account gets a separate home even when the same person controls
both accounts; Wingthing does not infer account relationships or copy provider
credentials between them.

Wingthing isolates users from one another at its application and sandbox layers. It
does not protect their credentials from the server or hypervisor administrator; the
team must trust whoever operates the host.
