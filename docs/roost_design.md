# Roost deployment model

Status: implemented

Reviewed: 2026-08-24

## Terminology

| Term | Meaning |
| --- | --- |
| Portal | Client-facing inventory and controls in the browser or MCP |
| Gateway | HTTP, WebSocket routing, identity, registry, and SQLite state; started alone with `wt serve` |
| Wing | Execution runtime that owns processes, sessions, tasks, workspaces, and agent homes |
| Roost | Self-hosted portal/gateway and embedded wing in one process; started with `wt roost start` |

A roost is a deployment bundle, not a universal network namespace. It contains
one local wing. Other wings can register with the roost's gateway, but their
runtime state does not move into the embedded wing.

`wingthing.ai` is the hosted portal and gateway. Under this strict definition,
it is not a roost because it does not include the personal wings registered
with it.

## Start a roost

```bash
wt roost start
open http://localhost:8080
```

The command starts the gateway, waits for it to become ready, then starts an
embedded wing against the local gateway. Both share one process lifecycle and
log stream.

Common operations:

```bash
wt roost start
wt roost status
wt roost stop
wt roost start --foreground
```

Without OAuth configuration, the portal uses local single-user mode. With a
public HTTPS URL and an OAuth provider, it becomes a multi-user shared runtime:

```bash
WT_BASE_URL=https://lab.example.com \
GITHUB_CLIENT_ID=... \
GITHUB_CLIENT_SECRET=... \
wt roost start --addr :8080
```

Set project roots, labels, sandbox defaults, and audit policy through the same
wing configuration used by a standalone wing.

## Run components separately

Use separate processes when the gateway and execution runtime belong on
different machines:

```bash
# gateway only
wt serve

# wing on another machine
wt login --roost https://portal.example.com
wt start --roost https://portal.example.com
```

Each wing chooses one gateway URL in its current profile. Use a separate
`WINGTHING_DIR` when one machine needs independent login and key state for
another portal.

## Multi-wing behavior

A roost gateway can register the embedded wing and external wings.

- The browser receives its authorized wing roster, probes each wing through the
  encrypted tunnel, and can show sessions from several wings.
- HTTP MCP `wing_list` receives that same access-filtered roster and marks only
  the embedded wing as currently controllable.
- `wt wings --roost URL` can query the same roster and encrypted wing metadata.
- The roost's HTTP MCP endpoint currently calls only the embedded wing because
  its native tools have no `wing_id` target.

The last item is the principal portal-parity gap. Adding an external wing to a
roost does not make it selectable through HTTP MCP today.

Independent roosts do not discover or federate with one another. A client
selects a roost by URL. An LLM can register several HTTP MCP URLs under separate
names and choose the name explicitly.

Internal peer-directory and edge-routing code coordinates replicas of one
hosted gateway. WebRTC may connect a browser directly to a wing. Neither is
peer-roost discovery.

## Security model

The gateway and embedded wing remain logical trust domains even though they
share a process. Terminal and encrypted tunnel payloads use the same
application protocol as a standalone wing.

For a self-hosted roost, the machine operator controls both components and can
read the wing's files, process memory, workspaces, and provider credentials.
Application encryption protects the route and network boundary; it cannot
protect a user from the host or hypervisor administrator.

On a shared host, Wingthing gives each authenticated owner a separate agent home
and enforces owner-scoped resources. This is useful multi-user policy, not a
claim of protection from root.

## Naming debt

Configuration and flags still use `roost_url` and `--roost` for the gateway
endpoint. Renaming them to `gateway_url` and `--gateway` would be more exact but
requires a compatibility migration. Public prose should use gateway for the
component and reserve roost for the all-in-one deployment in the meantime.
