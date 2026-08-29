# Fly Operations Guide

Wingthing's Fly app is the public coordinator: identity, the authorized wing
directory, key exchange, WebRTC signaling, the portal, and the optional encrypted
relay. Agents execute only on a user's selected wing. A Fly machine is not an
execution wing and does not receive a user's workspace or provider credentials.

## Checked-in topology

The active `fly.toml` is deliberately login-only:

- `[processes].login` is enabled;
- `[processes].edge` is commented out;
- `[http_service].processes` contains only `"login"`;
- the `wt_data` volume and the configured VM stanza apply only to `login`; and
- the service keeps at least one login machine running.

The active service attachment is therefore:

```toml
processes = ["login"]
```

That means an ordinary `fly deploy` of the checked-in file does not create or route
traffic to an edge process. Verify the live machine count with `make status`; the
configuration is the deployment target, not evidence of what an earlier manual
scale command left running.

The login process owns the SQLite volume at `/data` and serves the public HTTP and
WebSocket surface. The binary also contains an optional edge role. An edge has no
durable volume, skips the relay database, proxies login-owned HTTP/API work to the
login process, and keeps synchronized session, wing, and entitlement caches.

On Fly, an unset `WT_NODE_ROLE` is inferred before configuration loading: an
already-mounted `/data` directory means `login`; its absence means `edge`. This
ordering matters because configuration initialization itself may create state below
`/data`. An edge with `FLY_APP_NAME` and no explicit `WT_LOGIN_ADDR` derives
`http://login.process.<app>.internal:8080`. An explicit `WT_NODE_ROLE` or
`WT_LOGIN_ADDR` takes precedence.

## Placement and durable state

| Decision | Public Fly deployment |
| --- | --- |
| **Execution wing** | The access-filtered wing explicitly selected by `wing_id`. The coordinator never substitutes a Fly process as the execution target. |
| **Workspace** | An existing `cwd` on the selected wing. The service does not clone or synchronize repositories or untracked files. |
| **Display** | `agent_run` returns semantic state over direct MCP. `agent_start` creates a persistent PTY; hosted browser/control relay is entitlement-gated, while CLI, SSH, and self-hosted displays remain separate paths. |
| **Provider credentials** | The execution owner's agent home on the selected wing. Fly secrets are service credentials, not Claude, Codex, SSH, or other user provider credentials. |
| **Durable memory** | The login volume stores gateway account, organization, auth, entitlement, and routing records. Each wing remains authoritative for its task database, sessions, provider history, optional Wingthing memory, and workspaces. |

The public `direct-free` relay policy changes transport entitlement, not ownership
or organization authorization. Wing-side roles, paths, grants, and bounds still
apply.

## Internal-node trust

The `/internal/*` API accepts a network caller without `WT_INTERNAL_SECRET` only
when the receiving process is configured as a Fly app machine and the request
arrives from a cluster-private address. That trusts the Fly organization's private
network boundary; it is not cryptographic caller authentication. Set the same
separate `WT_INTERNAL_SECRET` on every process if other applications in that Fly
organization are not equally trusted. A split non-Fly deployment must set the
secret and keep the node transport private and encrypted where it can cross an
untrusted network. Do not reuse `WT_JWT_KEY` as the internal secret.

## One-time setup

Generate an EC P-256 signing key so wings can authenticate against any public
process:

```sh
fly secrets set WT_JWT_KEY=$(wt keygen)
```

## Release and deploy the login-only configuration

The public website and installer are one versioned contract. Publish and verify the
matching GitHub release before deploying a site that documents it. From the exact
commit being promoted:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
# wait for the release workflow to publish all five assets
gh release view vX.Y.Z
curl -fsSL https://wingthing.ai/install.sh | sh
make deploy
```

`make deploy` runs the web build, Go tests, binary build, release command-surface
contract, and the configured historical-baseline compatibility gate before
`fly deploy`; it does not publish a GitHub release. The compatibility script uses
`WT_COMPAT_BASELINE_REF` when set and otherwise uses the pinned default declared in
`scripts/test-backward-compat.sh`. It exercises that baseline and the candidate in
both gateway/wing orders; it is not a claim that the pin is always the immediately
previous release, and it does not exercise a mixed Fly login/edge fleet.

Deploying the site before the matching release creates an installation outage: the
newer installer refuses an older binary whose command surface does not match the
site. Confirm that the installed binary reports `vX.Y.Z` before continuing.

The new `direct-free` restriction is not a completed security boundary until every
public gateway process is current. Check every live machine and image digest before
declaring the policy active.

## Hosted relay policy

`wt serve` defaults to the backward-compatible `legacy` policy so an upgrade does
not silently change a private gateway. The checked-in Fly configuration explicitly
sets `WT_RELAY_POLICY=direct-free`: free accounts can use login, the authorized wing
directory, key exchange, bounded discovery/passkey messages, and WebRTC signaling,
but the gateway denies PTY and general control payload relay. Accounts with relay
access retain that hosted browser transport.

On `direct-free`, the historical billing-free personal and organization upgrade
endpoints are disabled, and the account UI does not grant relay access. Existing
entitlements and cancellation paths remain valid; new relay access must come from
the deployment's billing or operator workflow. Private legacy gateways and
self-hosted roosts retain their operator-controlled relay behavior. A wing with
`hosted_relay: deny` refuses relayed payloads even when the account or self-hosted
gateway would otherwise permit them.

The checked-in configuration also sets the migration cutoff explicitly:

```text
WT_RELAY_MIGRATION_BEFORE=2026-08-26T00:00:00Z
```

Accounts created on or before that instant retain temporary relay parity while the
migration rule is active. If the value changes, deploy the same RFC3339 value to all
gateway processes. Startup fails when the current and deprecated cutoff variable
names are both present with different values. The logged-in API publishes
`relay_allowed` and `relay_reason`; an enabled edge synchronizes the login node's
decision.

## Enable the optional edge group

Do this only when edge capacity is intentionally part of the deployment. Two edits
are required in `fly.toml` before scaling:

1. Uncomment `[processes].edge`.
2. Change the HTTP service attachment to:

   ```toml
   processes = ["login", "edge"]
   ```

Then deploy that configuration before creating edge machines:

```sh
fly deploy
make status
make deploy-edge REGIONS=nrt COUNT=1
make deploy-edge REGIONS=lhr COUNT=1
```

`make deploy-edge` only changes the edge count in the named regions. It now refuses
to run while the process command is commented out or the HTTP service excludes
`edge`. After scaling, use `make status`, check `/health` through the public service,
and inspect each process's startup log. Edge logs must say `auto-detected node role:
edge` and name the derived or configured login address; login logs must say
`auto-detected node role: login`.

Current edges proxy portal HTML and hashed static assets to login, so those assets
come from the login release. When introducing that proxy rule to a fleet containing
older edges, update and verify edges before updating login; after that transition,
keep login and edge on the same promoted image rather than assuming the historical
compatibility pin covers mixed edge releases.

To set total counts across the process groups after edge is enabled:

```sh
make scale LOGIN=1 EDGE=3
```

To remove Tokyo edges, or every edge respectively:

```sh
fly scale count edge=0 --region nrt
make scale LOGIN=1 EDGE=0
```

After the last edge is removed, return `fly.toml` to its checked-in login-only form
if edge is no longer an intended deployment option: remove `"edge"` from
`http_service.processes`, comment the edge command, and deploy that configuration.

## Optional edge request path

When the edge group is enabled and attached to the HTTP service:

1. the login machine is the only process with `wt_data`;
2. an edge starts without `/data`, detects `edge`, and skips SQLite;
3. login-owned HTTP and API work is proxied over the private Fly address;
4. the edge synchronizes login-owned session, entitlement, and wing state; and
5. a WebSocket for a wing connected elsewhere can receive a `fly-replay` response
   that asks Fly to replay the upgrade on the owning machine.

These are optional code paths, not a description of the active checked-in topology.

## Self-hosted contrast

The simplest self-hosted deployment is one process with no `WT_NODE_ROLE`,
`FLY_MACHINE_ID`, gossip, or `fly-replay`: use `wt roost start` for a portal,
gateway, and embedded wing, or `wt serve` for the gateway alone. A private OAuth
gateway or roost should set `WT_ROOST_ALLOWED_EMAILS`; OAuth authenticates an
account but does not enroll it in a private service. Self-hosted relay policy is
operator-controlled and does not depend on a wingthing.ai hosted-relay entitlement.
