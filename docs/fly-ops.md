# Fly Operations Guide

Wingthing is a typed agent manager for durable agent runs and terminals. The Fly
fleet hosts its public coordinator: identity, the authorized wing directory, key
exchange, WebRTC signaling, the portal, and the optional encrypted relay. Agents
still execute on a user's selected wing; neither Fly process group is a general
agent host.

## Placement and durable state

| Decision | Public Fly deployment |
| --- | --- |
| **Execution wing** | The access-filtered wing explicitly selected by `wing_id`. Login and edge machines coordinate connections but do not substitute themselves as execution targets. |
| **Workspace** | An existing `cwd` on the selected wing. No Fly service clones or synchronizes user repositories or untracked files. |
| **Display** | `agent_run` returns semantic state over direct MCP without a browser view. `agent_start` creates a persistent PTY; hosted browser/control relay is entitlement-gated, while CLI, SSH, and self-hosted displays remain compatible. |
| **Provider credentials** | The execution owner's agent home on the selected wing. Fly secrets are only service credentials such as JWT, OAuth, billing, or internal-node keys—not Claude, Codex, SSH, or other user provider credentials. |
| **Durable memory** | The login volume stores gateway account, organization, auth, entitlement, and routing records. Each wing remains authoritative for its task database, eggs, optional Wingthing memory, provider history, and workspaces. Edge machines are stateless. |

The public `direct-free` relay policy changes transport entitlement, not ownership
or organization authorization. Existing personal and organization directories,
roles, configured path scopes, grants, and bounds continue to apply at the wing.

## Architecture

Two process groups, one image:

- **login** — has the SQLite volume at `/data`. Handles auth, API, social pages, WebSocket relay. There is exactly one.
- **edge** — stateless. Handles WebSocket relay only. Proxies API/auth to login. There can be zero or many.

Role is auto-detected: if `/data` exists (volume mounted), it's login. Otherwise edge. No env vars to set per machine.

Edge nodes discover the login node via Fly internal DNS: `login.process.wingthing.internal:8080`.
The `/internal/*` API accepts an unauthenticated network caller only when its
source is cluster-private and the receiving process is configured as a Fly app
machine. That path trusts the Fly organization's 6PN boundary; it does not prove a
cryptographic caller identity. Set the same separate `WT_INTERNAL_SECRET` on every
Fly process if other applications in the Fly organization are not equally trusted.
A standalone or non-Fly split deployment must set that secret. The JWT signing key
is never accepted as an HTTP credential.

## One-time setup

Generate an EC P-256 signing key so wings can auth against any node:

```
fly secrets set WT_JWT_KEY=$(wt keygen)
```

## Deploy

The public website and installer are one versioned contract. Publish and verify the
matching GitHub release before deploying the site that documents it. From the exact
commit being promoted:

```
git tag vX.Y.Z
git push origin vX.Y.Z
# wait for the release workflow to pass and publish all five assets
gh release view vX.Y.Z
curl -fsSL https://wingthing.ai/install.sh | sh
make deploy
```

`make deploy` runs the local build/tests, release command-surface contract, and
real N-1/current rolling-upgrade compatibility gate before `fly deploy`; it does
not publish a GitHub release. The compatibility gate requires the published
baseline tag, so deploy from a full clone with tags. Deploying first creates an
installation outage: the newer site serves an installer that correctly refuses an
older binary whose command surface does not match the documentation. Verify that
the installed binary reports `vX.Y.Z` before continuing.

Fly may roll the login and edge processes independently. The wire changes in this
release are additive, so mixed versions preserve the historical relay behavior, but
the new `direct-free` restriction is not a completed security boundary until every
gateway process is current. Check every machine and its image digest before declaring
the policy active.

Current edges proxy the portal HTML and hashed static assets to the login process,
so one page load cannot mix bundles from two releases. The release that introduced
that rule needs one special split-fleet order: update every edge process first, then
update login. An older edge serves its own assets, so updating login first can pair a
new index with an old missing asset during that first rollout. The checked-in Fly
configuration currently exposes only the login process; this ordering applies when
the optional edge group is enabled. After every edge runs this release, ordinary
rolling order is safe for static assets.

For that one split-fleet transition, run the gates above and deploy the same
checked-out commit in this order instead of using the all-groups `make deploy`:

```bash
fly deploy --process-groups edge
# verify every edge is healthy and running the new image
fly deploy --process-groups login
```

### Hosted relay policy

The `wt serve` gateway defaults to the backward-compatible `legacy` policy so an
upgrade cannot silently change an existing private gateway. The checked-in Fly
configuration explicitly sets `WT_RELAY_POLICY=direct-free`: free accounts may
use login, the wing directory, key exchange, bounded discovery/passkey messages,
and WebRTC signaling, but PTY and general control payload relay is denied. Pro
users retain relay access.

On `direct-free`, the historical billing-free personal and organization upgrade
endpoints are disabled, and the account UI does not offer plan mutation. Existing
Pro/team entitlements and cancellation paths remain valid; new relay entitlements
must be provisioned by the deployment's billing or operator workflow. Legacy and
self-hosted gateways retain their previous self-service behavior. This does not
remove organization membership or wing sharing; it only prevents those endpoints
from granting new hosted-relay entitlement.

The public deployment also sets an explicit temporary migration boundary in
`fly.toml`. Accounts created on or before that instant retain relay parity while
the transition is active. If the boundary changes, update the same RFC3339 value
on every login and edge process:

```text
WT_RELAY_MIGRATION_BEFORE=2026-08-26T00:00:00Z
```

`WT_RELAY_GRANDFATHER_BEFORE` remains a deprecated compatibility alias. Startup
fails if both names are set to different values. The logged-in API reports
`relay_allowed` and `relay_reason`, and edge entitlement sync carries the same
decision made by the login node.

## Scale

### Add edge nodes to a region

```
make deploy-edge REGIONS=nrt COUNT=1      # 1 edge in Tokyo
make deploy-edge REGIONS=lhr COUNT=1      # 1 edge in London
make deploy-edge REGIONS=nrt,lhr COUNT=2  # 2 edges split across Tokyo + London
```

### Set total counts

```
make scale LOGIN=1 EDGE=3
```

### Check what's running

```
make status
```

## Middle-of-the-night playbook

Only use this after the matching release has passed the promotion sequence above:

```
make deploy
make deploy-edge REGIONS=nrt,lhr,cdg COUNT=1
```

That's it. One login in ewr, one edge each in Tokyo, London, Paris. Wings and browsers auto-route to nearest via Fly anycast.

## Region codes

| Code | City |
|------|------|
| ewr | Newark (login node) |
| nrt | Tokyo |
| lhr | London |
| cdg | Paris |
| sin | Singapore |
| syd | Sydney |
| gru | São Paulo |
| sea | Seattle |
| ord | Chicago |
| iad | Ashburn |

Full list: `fly platform regions`

## How it works

1. `fly deploy` builds the Docker image and deploys to all machines
2. Login machine has the `wt_data` volume → auto-detects as login
3. Edge machines have no volume → auto-detect as edge
4. Edges proxy API/auth requests to login over Fly's private 6PN network
5. Edges cache entitlements (polled every 60s) and sessions (cached 5min)
6. Login drives gossip: pushes wing online/offline events to edges every 2s
7. If a browser on an edge needs a wing on another node, `fly-replay` header redirects the WebSocket upgrade transparently

## Removing edge nodes

```
fly scale count edge=0 --region nrt    # remove Tokyo edges
```

Or remove all edges:

```
make scale LOGIN=1 EDGE=0
```

## Self-hosted

The simplest self-hosted deployment is a single node with no `WT_NODE_ROLE`, no
`FLY_MACHINE_ID`, no gossip, and no `fly-replay`: use `wt roost start` for the
portal, gateway, and embedded wing, or `wt serve` for the gateway alone. An
OAuth gateway or roost should set `WT_ROOST_ALLOWED_EMAILS`; OAuth identifies an
account but does not by itself enroll that account in a private service. All
multi-node code paths are gated on Fly environment variables being present.
If you deliberately build a split non-Fly deployment, set the same high-entropy
`WT_INTERNAL_SECRET` on every node. Wingthing's built-in node clients send it as
`X-Internal-Secret`; keep the node transport private (and encrypted when it can
cross an untrusted network). Do not reuse `WT_JWT_KEY` for that purpose.
