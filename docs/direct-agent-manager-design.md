# Direct Agent Manager and Coordination-Only Free Tier

Status: implementation design for `feature/direct-control-free-tier`

Reviewed: 2026-08-27

The broader product contract, Slack-derived use cases, gap audit, and ordered
roadmap live in [Agent Manager Product Brief and Gap Audit](agent-manager-product-brief.md).
This document remains the design for the direct transport and entitlement slice.

## Product thesis

Wingthing is an agent manager for agents. It gives an agent one inventory of durable agent runs and terminals across every machine its owner can access, then lets that agent start, inspect, message, wait for, and take over those sessions. A browser and a human terminal are clients of the same control plane, not the center of the product.

`wingthing.ai` is the coordination service, analogous to a tailnet control plane. It authenticates identities, publishes an access-filtered wing directory, exchanges connection metadata, and helps peers establish encrypted direct connections. In this first slice it does not carry free-tier MCP payload bytes; the hosted browser terminal remains an entitled relay feature until browser-direct transport ships.

The hosted browser-terminal/control relay is an optional paid transport. The
native connector in this slice is direct-only and does not silently fall back to
it. A self-hosted roost combines coordination, an operator-controlled relay, and
an optional local wing without making that embedded wing special.

## Components and trust boundaries

| Component | Responsibility | Sees payload bytes by default? |
| --- | --- | --- |
| `wt mcp stdio` | Control the wing on the same host through local state/socket APIs | Local only |
| `wt mcp connect` | Present one MCP server containing explicitly qualified resources from accessible remote wings | Yes, on the client |
| Wing | Own durable sessions and execute authorized control operations | Yes, for its own sessions |
| Coordinator (`wingthing.ai`) | Login, device identity, ACL-filtered wing directory, key exchange, and WebRTC signaling | No direct MCP payloads |
| Hosted relay | Entitled browser-terminal and control transport | Yes, encrypted transit bytes only |
| Roost | Self-hosted coordinator, optional relay, and optional embedded wing | Chosen by operator |

The JavaScript and native clients distributed by a hosted coordinator remain a supply-chain trust boundary even when payloads travel directly. Encryption in transit does not remove the need to trust the client executable.

## Resource model

There is no mutable "current wing" in the remote MCP adapter. Every wing-owned call requires `wing_id`; every returned wing-owned object includes `wing_id`. Stable resource identity is therefore:

```text
(wing_id, object_kind, object_id)
```

The portal owns only directory operations such as `wing_list`. A selected wing owns terminal, agent-run, message, and sandbox operations. Browser and MCP inventory must use the same access-filtered directory implementation.

## Connection flows

### Same-host agent

```text
agent -> stdio MCP -> local Wingthing runtime
```

No coordinator login or network path is involved.

### Remote agent, free/default

```text
agent -> wt mcp connect -> encrypted WebRTC data channel -> selected wing
                              ^
                              |
                  directory + signaling only
                         wingthing.ai
```

The connector logs in once, lists accessible wings, pins each wing's long-term key, and uses the coordinator to exchange a WebRTC offer and answer. MCP requests and results then travel on the peer-to-peer data channel. If a direct path cannot be established, the free connector returns an actionable error; it does not silently proxy the request.

### Hosted relay access

Accounts with relay access may use the hosted encrypted browser terminal and
control relay. Accounts on the temporary side of the deployment's explicit
migration boundary retain that path during rollout. `wt mcp connect` does not
switch to this transport after a direct failure.

### Self-hosted roost

A roost may allow relay traffic according to its operator policy. For one user on
one machine, `wt roost start --https` creates a localhost-only CA and leaf on demand,
installs only the public CA in that user's trust store, and keeps both private keys
on the host. A shared or remotely reachable roost still needs a real domain and
externally terminated HTTPS, such as ACME or a tailnet/VPN reverse proxy. Local MCP
needs neither public DNS nor HTTPS.

## Direct control protocol

The native connector exposes the shared Wingthing MCP contract plus `wing_list`. Its wing-owned tool schemas add a required `wing_id` field without changing the same-host or existing HTTP schemas.

The WebRTC control channel label is versioned and identifies the authenticated client actor. Messages are bounded JSON envelopes:

```json
{"version":"v1","id":"...","tool":"agent_start","arguments":{}}
{"version":"v1","id":"...","result":{"session":"..."},"is_error":false}
```

`wing_id` selects the transport and is removed before the operation reaches the wing handler. The wing derives the user, organization role, and passkey attestations from the authenticated signaling exchange; those fields are never accepted from the MCP request. It applies the same grant checks, owner scoping, filesystem scoping, argument redaction, and audit policy as its HTTP MCP adapter.

The branch now resolves an explicit wing-local policy for every direct request,
including requests on already-open channels after a `SIGHUP` reload. The compatible
default operation set uses positive per-principal session/spawn bounds; `wing.yaml`
may narrow grants, change bounds, or disable direct MCP. The rolling spawn window is
shared across reconnecting data channels for the lifetime of the wing process.
Invalid identity, organization role, or local direct policy fails before a tool
handler runs.

Coordinator-derived user and organization identity is leased for 15 minutes. The
wing closes the data channel at expiry, and the connector transparently performs a
new access-filtered discovery and signaling exchange on the next request. Wing-local
lock, passkey, path, and grant changes are stricter: they are checked on every
request and therefore do not wait for lease expiry.

The first native transport targets host/LAN/tailnet candidates. Configured ICE servers can add broader NAT traversal. This first slice fails closed on locked or per-user passkey-protected wings; it returns an explicit error until the native connector can complete the same passkey-bound authorization ceremony used by the browser.

## Entitlements

| Capability | New free account | Pro | Existing account during migration | Self-hosted roost |
| --- | --- | --- | --- | --- |
| Login, directory, key exchange, signaling | Yes | Yes | Yes | Yes |
| Local MCP | Yes | Yes | Yes | Yes |
| Direct native MCP connection | Yes on unlocked wings | Yes on unlocked wings | Yes on unlocked wings | Yes on unlocked wings |
| Direct browser terminal | Not in this slice | Not in this slice | Not in this slice | Not in this slice |
| Hosted browser terminal/control relay | No | Yes | Temporary | Operator policy |

Relay access is a server decision, returned as structured capability metadata and checked before a relayed terminal is started or attached. It is not inferred from client UI state. Temporary migration access uses an explicit deployment cutoff timestamp, is observable in `/api/app/me`, and can later be removed without changing account tiers.

The wing has the final transport decision. `hosted_relay: deny` overrides every
account cohort, including Pro, temporary-migration, and self-hosted relay access. The
gateway rejects payload routing before forwarding and the wing independently rejects
relayed PTY and general control messages. Omitted policy remains `allow` for N-1
wings; unknown explicit values fail closed. Coordination purposes remain bounded and
purpose-bound at the wing. Authorized roster and `wing.info` capability metadata show
the effective value, and denial audit records exclude command, path, and payload.

Small, purpose-specific signaling messages remain available to free users. The outer tunnel envelope declares a bounded coordination purpose, and the wing verifies that declaration against the decrypted inner message before responding. New free accounts cannot use the general encrypted control tunnel. Wings advertise purpose-binding support at registration, and the coordinator rejects direct-only signaling to older wings. A later protocol version can split these declarations into physically separate endpoints.

## Rollout

1. Add the qualified direct MCP contract, native connector, and wing-side WebRTC control handler.
2. Add relay entitlement metadata and deny new free terminal relay starts/attaches while preserving explicit temporary migration access.
3. Put direct-agent setup at the center of the logged-in free page; preserve the current terminal UI for entitled users.
4. Restrict the opaque generic tunnel for new free users to purpose-bound discovery,
   signaling, and passkey coordination. Browser-direct transport remains a separate
   later slice.
5. Evaluate an explicit native relay transport separately, and design multi-roost peer directory exchange.

Steps 1-4 are implemented on this branch. Step 5 is deliberately compatible with
the resource and entitlement model above but remains future work.

## Acceptance criteria

1. `wt mcp stdio` works with the network unavailable.
2. `wt mcp connect` lists two accessible wings and requires `wing_id` for every wing-owned call.
3. A call addressed to wing A cannot execute on wing B, and returned resources are qualified with A.
4. A successful direct MCP call sends no MCP request/result bytes through the relay.
5. A new free user is denied before a hosted relayed terminal starts or attaches, with direct/self-host/pro remediation.
6. Pro and explicit temporary-migration accounts retain the current relay behavior.
7. Roost mode keeps working without a hosted subscription and can choose its own relay policy.
8. Contract, connector, transport, authorization, and relay-policy behavior have unit/integration coverage and `make test` passes.
9. Locked and per-user passkey-protected wings reject native direct MCP calls until a passkey ceremony is implemented; coordinator identity alone never bypasses the local lock.
10. A wing with `hosted_relay: deny` rejects relayed payloads for owner and org member even when the account is otherwise entitled, while bounded discovery/signaling still works.

## Compatibility and deployment

The existing HTTP MCP endpoint remains available during migration, and the deployment can grant a measured existing-account cohort temporary access. No automatic Fly deployment is implied by a GitHub release: the production deployment must be performed and verified separately. Public docs and `/patterns` should be checked as part of the production rollout so the website does not advertise a contract older than the released binary.

The deterministic connector canary now crosses JSON-RPC stdio and two independent
real WebRTC data channels, verifies qualified `home`/`office` routing, reconnects, and
checks that the coordinator handled signaling only. The compatibility gate separately
runs the configured historical baseline and candidate binaries in both gateway/wing
upgrade orders, starts a PTY through the old browser message shape, and proves the
baseline binary can reopen candidate state. The pin is not automatically the
immediately previous release. The direct-MCP canary remains an in-process network
test; the release gate still requires the built `wt mcp connect` process and a real
Codex/Claude client against two distinct hosts, including the WSL rig.
