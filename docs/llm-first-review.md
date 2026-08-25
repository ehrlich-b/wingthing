# LLM-first architecture review

Status: review with first implementation slice

Reviewed: 2026-08-24
Repository snapshot: `c87a778`

## Verdict

Wingthing already has most of the hard runtime pieces: persistent PTYs,
provider adapters, semantic headless runs, owner-scoped control, sandbox
enforcement, a browser, local and HTTP MCP, encrypted routing, and tests that
exercise real agent harnesses.

Those pieces don't yet form one portal. The browser, local MCP server, roost MCP
server, and native CLI each expose a different subset of the same machine. They
share some storage. The first implementation slice adds a shared operation
registry and one access-filtered wing roster for the browser and roost HTTP MCP.
There is still no single session/run inventory, resource namespace, or event
stream.

The next product boundary should be:

> One inventory and control contract for every agent session and run, on every
> wing the caller can reach. A person and an LLM use different renderings of the
> same resources.

"All your agents in one place" should mean one inventory, one authority model,
and one lifecycle contract. Code, credentials, and processes stay on their
chosen machines.

## What exists now

The current product has four partial views:

| State | Authority | CLI | local MCP | roost HTTP MCP | web portal |
| --- | --- | ---: | ---: | ---: | ---: |
| Connected wing roster | gateway memory/database | `wt wings` | no | yes | yes |
| Live egg/PTY sessions | wing `eggs/` directory and egg sockets | yes | yes | embedded wing only | every accessible registered wing |
| Headless agent runs | wing `wt.db` task tables | limited | yes | embedded wing only | no |
| Prompt assets, loops, and swarms | wing `wt.db` and prompt store | yes | yes | no | no |
| Owner-scoped agent messages | wing `wt.db` | no | yes | embedded wing only | no |
| Sandbox explanation | wing policy resolver | CLI | yes | embedded wing only | partial editor |
| Session history and recordings | wing egg files | partial | no | no | yes |

This explains the current behavior:

- `terminal_start` and `agent_start` create ordinary eggs. If the same wing is
  connected to a web portal, those sessions appear there.
- `agent_run` creates a task record and a supervised headless process. It
  doesn't create an egg or PTY, so the browser never sees it.
- A self-hosted roost returns the same authorized wing roster through the
  browser and HTTP MCP `wing_list`. Runtime tools still call the embedded
  wing's local state directly and don't accept a `wing_id`.
- The hosted portal can show several personal or organization wings registered
  with its gateway. It can't see independent self-hosted roosts.
- Registering several HTTP MCP servers gives an LLM several named targets, but
  no Wingthing service aggregates their inventory.

The existing `PeerDirectory`, edge map, and Fly replay logic coordinate
replicas inside one gateway deployment. WebRTC peers connect browsers to wings.
Neither mechanism discovers or federates independent roosts.

## Use fewer public nouns

The repository currently gives "roost" two incompatible definitions. The
original design and `wt roost` command define it as a relay plus an embedded
wing. The website calls the relay-only hosted service a roost. Both definitions
appear in current documentation.

The public model should be:

| Term | Definition |
| --- | --- |
| **Portal** | The client-facing inventory and control surface. It may be hosted or self-hosted, and it has a browser and MCP endpoint. |
| **Gateway** | The portal component that authenticates callers, keeps a wing roster, and routes traffic. Most users don't need this noun. |
| **Wing** | One execution runtime. The wing owns sessions, runs, workspaces, policy resolution, and provider credentials. |
| **Session** | An interactive persistent PTY. |
| **Run** | A semantic headless task with events and a result. |
| **Egg** | The execution boundary for one persistent session. Keep this term in sandbox and debugging documentation. |
| **Roost** | The self-hosted deployment bundle started by `wt roost start`: portal/gateway plus an embedded wing. |

Under this definition, `app.wingthing.ai` is the hosted portal and
`wingthing.ai` supplies its gateway. A roost is one way to deploy the same
portal and runtime pieces. It stops being the root of the object model.

Internal names such as `RoostURL` can remain during a compatibility window,
but new user-facing flags and schemas should say `portal` or `gateway` when
they select an endpoint.

## One control contract

The control service belongs beside the state it controls, on the wing.

```text
                       portal
              identity, roster, target routing
                         |
          +--------------+---------------+
          |              |               |
       web API         HTTP MCP       native client
          |              |               |
          +-------- versioned control RPC-+
                         |
                 selected wing service
             sessions, runs, policy, events
                         |
                    eggs + task store
```

For a roost's embedded wing, the portal can call the service in-process. For an
external wing, the same request travels through the application-encrypted
tunnel. Local CLI and stdio MCP call the service over a wing-owned local socket.
Transport changes don't create new semantics.

The first implementation slice moves names, schemas, grants, annotations,
surface availability, authority, and audit targeting into `internal/control`.
The registry should ultimately define each capability once:

- stable operation name and version;
- request and response schemas;
- resource and ownership rules;
- grant and server-side bounds;
- read-only, mutating, destructive, and open-world annotations;
- audit redaction; and
- supported transports.

Adapters render that registry as CLI JSON, MCP tools, portal HTTP resources, and
browser actions. Contract tests compare every adapter with the registry. The
semantic handlers still live in `cmd/wt/mcp_local.go` and should move to a
transport-independent package before more runtime tools are added.

## Resource identity and target selection

Target selection must be explicit and safe under concurrent callers. A mutable
"current wing" on an MCP connection will route the wrong call eventually.

Every resource reference should contain:

```json
{
  "portal_id": "lab",
  "wing_id": "1ae20a6b28854276b1514d14",
  "kind": "session",
  "id": "research"
}
```

The portal endpoint supplies `portal_id`; the caller doesn't get to impersonate
another portal. List and start operations accept a `wing_id`. Later operations
return and accept the full resource reference. Human labels remain local aliases,
not routing identity.

The first read-only step is now present: `wing_list` returns the exact authorized
browser roster and says which entry the current endpoint can control. The next
multi-wing MCP slice needs a small set of additions:

- `session_list` and `run_list` can filter by wing or return the whole portal;
- start calls require `wing_id` when more than one target is available;
- returned session and run references retain their wing; and
- read, wait, send, steer, and stop route by the returned reference.

The web portal should use these same list and lifecycle operations. Its current
`sessions.list` tunnel message and localStorage merge can then become one
adapter instead of a second object model.

## Several portals

Portal federation is a later problem. The first useful version is a client-side
target registry:

```text
personal portal  --\
team roost -------+--> local Wingthing target registry --> one combined inventory
GPU lab roost ----/
```

Each entry has a name, URL, portal identity pin, independent OAuth session, and
capability cache. Read-only inventory can fan out. Mutations always carry the
selected portal and wing. Owner identities from two portals remain different
even when their email strings match.

This gives a person and an LLM one view without designing roost peering,
cross-roost trust, replicated policy, or distributed ownership. A server-side
federation protocol should wait for a real workflow that client-side aggregation
can't handle.

## Placement is part of every run

The workflow discussion about laptops and development VMs exposes five separate
placement decisions:

| Decision | Question | Current representation | Needed representation |
| --- | --- | --- | --- |
| Execution | Where does the agent or build run? | Implied by the MCP server or browser wing | Explicit `wing_id` and required capabilities |
| Workspace | Where is the authoritative code and untracked state? | Absolute host `cwd` | Logical workspace with per-wing replicas and sync state |
| Display | Where does a person or browser tool view the result? | PTY plus ad hoc preview file | Owned preview resource with source wing, route, TTL, and content type |
| Credentials | Which identity may the process use on that host? | Ambient env or per-user agent home | Owner-scoped credential reference resolved on the execution wing |
| Memory | Which durable project/user context follows the work? | Provider-specific home and Wingthing local files | Scoped memory asset with explicit replication and provenance |

An execution target and a workspace are independent. The current API's host
`cwd` binds them together. A portable run should accept a logical workspace:

```json
{
  "wing_id": "gpu-lab",
  "workspace_id": "agentless",
  "agent": "claude",
  "model": "opus",
  "prompt": "run the integration matrix"
}
```

The wing resolves `workspace_id` to a canonical local path, verifies that the
replica is ready, then records the resolved path and revision in run provenance.
`cwd` can remain as a local escape hatch, but it shouldn't be the portable
contract.

### Workspace modes

Wingthing shouldn't begin by implementing general bidirectional filesystem sync.
A workspace can declare one of four concrete modes:

- **resident:** the authoritative files already live on one wing;
- **git materialized:** the target creates an isolated worktree from an exact
  repository and revision, with an explicit patch or artifact for selected
  untracked inputs;
- **externally replicated:** Syncthing, Mutagen, a shared filesystem, or another
  operator-selected mechanism owns replication; Wingthing reports readiness,
  revision, conflicts, and last sync time; or
- **artifact:** an immutable bundle is staged to the target and verified by
  digest.

A full `~/Work` tree has caches, sockets, secrets, large build products, and
files with no conflict semantics. Silent two-way sync is a poor default.
Workspace manifests should name included roots, excluded paths, maximum bytes,
the authority side, and conflict behavior. Non-git state then becomes visible
policy instead of an accidental omission.

Offline work follows from this model. A local replica that is marked ready can
run on the local wing without a portal. Remote-only replicas remain unavailable
while offline, and the UI can say that directly.

### Display and browser placement

An agent running on a VM shouldn't need to launch a GUI on that VM so a person
can inspect a web app. A run should publish a preview resource:

```text
remote build/run -> owned preview route -> local browser
```

The existing preview panel is the start of this contract. Promote it from a
magic file to typed operations with source wing, local or remote endpoint,
owner, TTL, readiness, and close state. The browser can render the preview on
the laptop. A browser agent can consume the same route later, whether its browser
runs on the laptop or another wing.

### Credentials

Provider and repository credentials belong on the execution wing under the
run owner's identity. Shared-host owner homes are useful isolation between
ordinary eggs. They don't protect secrets from host root, a hypervisor
administrator, or someone who can copy the VM disk.

The control API should accept a credential class or reference, never a raw
token. The target reports `ready` or `auth_required` and supplies a bounded
login handoff. Short-lived delegated Git credentials or SSH-agent forwarding
are preferable when the workload permits them. A personal VM remains the right
boundary when the shared host operator isn't trusted.

### Durable memory

Agent-specific memory tied to a launch directory will fragment when work moves
between wings. Wingthing should model durable context as ordinary scoped assets:

- owner memory;
- project/workspace memory;
- task or ticket memory; and
- agent-private cache that isn't promised to move.

Each durable asset needs a source, revision, last writer, and replication mode.
An Obsidian or Git repository can be one storage adapter because it is just
Markdown with an existing sync story. Wingthing should expose explicit
`memory_read`, `memory_update`, and stale-state checks rather than relying on
an instruction that asks an agent to remember to edit notes. Automatic updates
need lifecycle hooks and reviewable diffs.

## Make the usage patterns crisp

The current six patterns mix actor, runtime, transport, and deployment. A recipe
should instead answer these fields:

```text
driver: human | LLM
portal: none/local | hosted | self-hosted URL
wing: local | explicit wing_id
workspace: resident | git | replicated | artifact
display: terminal | browser | preview route
credentials: local owner | remote owner | delegated
memory: local | project | replicated
```

The current shipped matrix is:

| Driver | Local wing | Personal wing through hosted portal | Shared roost wing |
| --- | --- | --- | --- |
| Human | CLI and `wt attach` | browser or SSH | browser |
| LLM | stdio MCP | no general external-wing MCP route | OAuth HTTP MCP |

Multi-roost orchestration is composition over the matrix. It shouldn't be
presented as a sixth runtime type.

## Test harness review

The harness has a sound evidence ladder. Unit tests cover schemas and semantic
cores. Synthetic relay tests isolate routing. Privileged Linux tests exercise
real namespace and seccomp boundaries. The browser canary drives the complete
shared-roost UI. The provider-swap battery compares direct harness behavior with
the Wingthing path and checks an exact filesystem artifact.

The non-root sealed-jail regression added in `2795bd3` is exactly the kind of
test this harness needs. It makes the mock agent report its UID and host-process
visibility, then proves that a non-root launch inside a private PID namespace
cannot see the host process or a denied secret. This closes a class of false
confidence created by root-running container tests.

The gaps now line up with the product gap:

1. The release smoke discovers 14 legacy MCP tools while the unconfigured local
   server publishes 27. It doesn't exercise `agent_run`, wait, result, events,
   steering, stop, messages, or HTTP MCP.
2. No test starts a session through MCP and then discovers and controls it in the
   browser, or does the reverse.
3. An in-process contract test now connects accessible and inaccessible wings,
   compares MCP `wing_list` with the browser roster, and checks control
   metadata. No black-box test targets and isolates runtime work on two wings.
4. No test exposes headless runs in the browser because that product surface
   doesn't exist.
5. The web E2E tier isn't part of `make test-e2e` or required CI.
6. The tag release workflow builds artifacts without running the documented
   promotion gates.
7. The HTTP MCP tests exercise OAuth and owner propagation in-process, but no
   real Codex or Claude client logs into a roost and completes a semantic run.
8. Manual host names, run IDs, dates, versions, and artifact hashes in evidence
   documents will go stale. Machines should emit a signed or hashed manifest
   that the docs link to.

[Testing](testing.md) turns these findings into a proposed command and
acceptance matrix.

## Recommended implementation branch

Create a separate work branch after the current test work and Claude's stack
have a stable base. The docs/review branch should remain small enough to merge or
hand back independently.

The implementation stack should preserve this order:

1. settle public terminology and add persistent portal identity;
2. continue extracting semantic handlers behind the new `internal/control`
   registry;
3. add qualified resource references and cross-adapter conformance tests;
4. put runs, messages, history, and sessions in one portal inventory;
5. carry the control RPC through the encrypted tunnel to external wings;
6. add explicit multi-wing selection to HTTP MCP and the browser;
7. add logical workspaces and run placement;
8. promote previews, credential readiness, and durable memory to typed
   resources; and
9. add a client-side multi-portal target registry if the prior slices are solid.

The branch should not begin with roost federation, a filesystem sync engine, or
a new dashboard. Those would harden the current split semantics. The control
contract and resource identity come first.
