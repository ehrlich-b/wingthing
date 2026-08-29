# Deploy the hosted Wingthing coordinator

Wingthing is a typed agent manager for durable agent runs and terminals. This Fly
deployment hosts `wingthing.ai`: identity, the authorized wing directory, key
exchange, connection coordination, the web portal, and the optional encrypted
relay. It does not run users' agents.

## Deployment boundary

| Decision | Hosted deployment |
| --- | --- |
| **Execution wing** | A user's laptop, VM, or self-hosted machine selected by `wing_id`; the Fly login and edge processes are coordinators, not execution wings. |
| **Workspace** | An existing path on that selected wing. The hosted service does not create, clone, upload, or synchronize workspaces. |
| **Display** | Direct MCP gives a parent agent semantic runs and persistent PTYs. The hosted browser terminal and control relay require relay entitlement; self-hosted and SSH displays remain separate compatible paths. |
| **Provider credentials** | The execution owner's provider-agent home on the selected wing. Provider tokens and SSH keys do not belong in Fly secrets, MCP arguments, or prompts. |
| **Durable memory** | Gateway account, organization, auth, entitlement, and routing records persist on the login volume. Run/task records, eggs, Wingthing prompt memory, provider history, and project files remain wing-local. Edge processes are stateless. |

## One-time Fly setup

From a checkout whose `fly.toml` still targets the intended app and region:

```bash
fly apps create wingthing
fly volumes create wt_data --region ewr --size 1
fly secrets set WT_JWT_KEY=$(wt keygen)
```

Configure the OAuth, email, and optional billing secrets required by this hosted
installation. If the login, app, and WebSocket endpoints use separate hosts, set
`WT_APP_HOST` and `WT_WS_HOST` consistently and provision DNS/TLS for each one.
The checked-in production base URL is `https://wingthing.ai`; the application and
wing endpoints are `app.wingthing.ai` and `ws.wingthing.ai`.

## Promote and deploy

The public documentation and installer are a versioned contract. Publish and
verify the matching GitHub release before deploying its site:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
gh release view vX.Y.Z
curl -fsSL https://wingthing.ai/install.sh | sh
make deploy
```

`make deploy` runs the documentation/web build, Go tests, binary build, release
command-surface check, and N-1/current compatibility gate before `fly deploy`. It
does not create a GitHub release. Use a full clone with tags so the compatibility
gate can find its published baseline.

Do not replace this sequence with a bare `fly deploy`. For split login/edge
rollouts, relay policy, DNS, scaling, rollback constraints, and verification, use
the [Fly operations guide](docs/fly-ops.md).
