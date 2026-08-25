# Share a self-hosted roost over the web

A roost is the bundle started by `wt roost start`: a portal and gateway with an
embedded wing. In this pattern, several people use that embedded wing through
the browser. Workspaces, provider credentials, and durable memory stay on the
shared host.

Configure at least one login provider and the public URL, then start the roost:

```sh
export WT_BASE_URL=https://roost.example.com
export GITHUB_CLIENT_ID=<github-oauth-client-id>
export GITHUB_CLIENT_SECRET=<github-oauth-client-secret>
wt roost start --addr :8080
```

Terminate TLS at the reverse proxy and forward the public host to port 8080.
Users sign in at `WT_BASE_URL` and receive owner-scoped sessions on the embedded
wing.

Set allowed project roots in `~/.wingthing/wing.yaml`:

```yaml
paths:
  - path: /srv/workspaces
```

Each user completes Claude or Codex login inside one of their sessions. Later
sessions reuse that user's agent home. This protects users from one another at
the Wingthing layer; it does not protect credentials from the host
administrator or hypervisor administrator.
