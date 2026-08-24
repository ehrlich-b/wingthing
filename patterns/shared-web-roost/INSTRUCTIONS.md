# Share a roost over the web

Install Wingthing on the shared host. Configure at least one login provider and
the public URL, then start the combined relay and wing:

```sh
export WT_BASE_URL=https://roost.example.com
export GITHUB_CLIENT_ID=<github-oauth-client-id>
export GITHUB_CLIENT_SECRET=<github-oauth-client-secret>
wt roost start --addr :8080
```

Terminate TLS at the reverse proxy and forward the public host to port 8080.
Users sign in at `WT_BASE_URL` and receive owner-scoped eggs on the shared host.

Set allowed project roots in `~/.wingthing/wing.yaml`:

```yaml
paths:
  - path: /srv/workspaces
```

Each user completes Claude or Codex login inside one of their eggs. Later eggs
reuse that user's agent home.
