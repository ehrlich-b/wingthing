# Shared-roost programmatic parity

Status: implemented slice and historical design record, reviewed 2026-08-24.

The OAuth HTTP MCP and embedded-wing control slice described here shipped. The
next architecture step is the cross-wing portal contract in
[the LLM-first architecture review](llm-first-review.md). That review also
supersedes this document's loose use of "roost" as both shared host and runtime.
Private OAuth roosts now optionally enforce exact email enrollment with
`WT_ROOST_ALLOWED_EMAILS`; references to authenticated users below assume that
deployment boundary.
A roost is now the self-hosted portal/gateway plus its embedded wing; the wing
is the runtime authority.

## Decision

The next pattern adds programmatic control to a shared roost. It uses the
existing roost, wing, egg, and sandbox pieces. Projected wings and a second
container topology add no useful boundary here.

A roost is the shared host and runtime. An egg is one durable, sandboxed agent
session on that runtime. A ticket workspace is an explicit collaboration
surface that multiple eggs owned by the same person may share. The software
factory is a skill and workflow that composes those pieces with deploy
environments, evidence, review, and pull requests.

Anything meaningful a person can do against the selected Wingthing runtime in
the web UI or CLI must be available as a typed operation with the same ownership
rules and audit trail. Codex, Claude Code, and other MCP clients authenticate to
the roost as the human using them, then create and control only that human's
eggs and tasks.

The current real product is Slide's multi-user shared roost in the web UI. That
flow is the compatibility baseline while the programmatic adapter is built.
The existing workflow stays working by default. A breaking simplification may
still be worthwhile after its benefit, affected workflow, and migration have
been agreed.

## Implemented slice

The branch now carries the first complete control path from OAuth HTTP MCP to
the embedded roost runtime:

- authenticated roost identity becomes a stable owner principal plus a
  client-specific audit actor;
- built-in typed control tools sit beside operator-configured executable
  tools;
- terminal discovery and control are owner-scoped, including guessed session
  IDs;
- `agent_run`, `agent_status`, `agent_wait`, `agent_result`, `agent_events`,
  `agent_steer`, and `agent_stop` provide a durable semantic lifecycle;
- `message_send`, `message_list`, and `message_wait` provide durable same-owner
  communication across Codex, Claude, and other authenticated clients while
  preserving distinct audit actors;
- Claude and Codex accept the same top-level `model` field;
- shared users receive stable per-user agent homes with ambient provider
  credentials removed;
- remote workspace paths fail closed and resolve symlinks before use;
- programmatically launched interactive eggs and headless runs use a Linux
  allowlist filesystem jail assembled by the roost; and
- task rows record the supervising process so a later process can convert an
  abandoned run into an explicit failure.

The local stdio transport dispatches tool calls concurrently. A waiting caller
can steer, inspect, or stop another run over the same connection. Cancellation
waits for the runner to exit before recording its final state, and a failed
parent leaves its steered child failed with a parent-status explanation.

Two capability classes are deliberate. Every account admitted by the roost's
web login policy gets owner-scoped controls within configured workspace roots.
Operator-configured executable tools remain governed by role policy. This gives
admitted users the basic roost product while reserving host-level capabilities
for explicit grants. Today's shared-roost admission policy accepts any account
that completes a configured OAuth provider. The private-roost proving ground
adds a narrower enrollment policy for `ehrlich.dev`.

## Product model

| Object | Meaning | Sharing rule |
| --- | --- | --- |
| roost | Shared host, scheduler, policy boundary, and Wingthing service | Many authenticated users |
| egg | Durable agent or command session with a PTY and sandbox | One owner; explicit administrative control only |
| ticket workspace | Checkout and build artifacts for one unit of work | Explicitly granted; egg control remains owner-scoped |
| task | A bounded agent invocation with status, events, and a semantic result | Same owner model as eggs |
| deploy environment | Separately provisioned target that runs exact artifacts | Referenced by the factory; lives outside the egg and workspace lifecycle |
| factory | Skill/workflow that admits work and composes the other objects | Portable across Codex, Claude, and future clients |

The useful mental model is Lego. A local egg, a personal
wing behind a hosted relay, a shared web roost, and a programmable shared roost
are compositions of the same runtime pieces. They belong on
`wingthing.ai/patterns` with honest availability labels.

## The factory transaction

The factory is the skill and typed workflow that moves a product intent through
implementation, review, deployment evidence, and a reviewable change stack.
The roost supplies shared hardware and owner-scoped control. The
workspace/glove/build box is one ticket workspace plus its eggs. Deploy
environments are separate targets, and a ticket may own several at once.

```text
PRD + product context
  -> ticket workspace
  -> implementation eggs
  -> independent review/repair eggs
  -> immutable artifact
  -> ephemeral deploy environment
  -> runtime evidence
  -> stacked pull requests
  -> human review
```

The quality contract belongs in the factory skill. Each change leaves a small,
coherent diff, exact tests, a semantic review result, and comments that explain
durable rationale. Generated narration and obvious line-by-line comments fail
review. Publishing pull requests remains an explicit mutating step with the
human's GitHub identity and an audit event.

The deploy half needs a provider-neutral object model on the roost:

| Operation | Contract |
| --- | --- |
| `environment_create` | Allocate an owner-scoped target from a configured provider, workspace, TTL, and resource bound |
| `environment_status` | Return provisioning state, target identity, artifact digest, expiry, and health |
| `environment_stage` | Transfer one immutable artifact and verify its digest on the target |
| `environment_run` | Execute an allowlisted command with bounded time and structured stdout, stderr, and exit status |
| `environment_evidence` | Collect kernel, service, test, and policy evidence with secret redaction |
| `environment_destroy` | Destroy the exact owned target and record the irreversible operation |

Provider adapters map those operations onto Proxmox, a VM service, or another
lab. The Proxmox adapter receives a pre-bounded pool, VMID range, templates,
storage, bridge, and SSH bootstrap. Callers never choose arbitrary hypervisor
resources. Every environment carries owner, actor, workspace, provider,
resource ID, creation transaction, TTL, and audit history. Expiry cleanup is a
supervised factory task whose evidence remains after the target disappears.

The 2026-08-22 dogfood run exercised this transaction manually: Codex built an
amd64 artifact in the workspace, staged it to an owned Ubuntu 24.04 Proxmox VM,
ran unprivileged and root enforcement gates, collected evidence, and cleaned up
the temporary bundle. Claude independently found the negative control that
failed. The durable roost implementation replaces SSH, SCP, the cached Proxmox
helper, and mailbox files with the typed operations above plus owner-scoped
`message_send`, `message_wait`, and `message_list`.

## Protected workflow and new workflow

The existing shared-web path remains valid:

```text
browser -> roost login -> encrypted PTY/tunnel -> embedded service wing -> egg
```

The new initial path is deliberately shorter because the relay and embedded
wing are already in the same roost process:

```text
Codex or Claude -> OAuth -> POST /mcp -> control service -> egg/task
```

The roost calls its embedded control service directly. A future external wing
may carry the same control operations through the existing encrypted tunnel.
That transport can wait until shared-roost parity works.

## One control plane, several adapters

Today the local MCP implementation in `cmd/wt/mcp_local.go` owns both JSON-RPC
transport behavior and most control semantics. The remote MCP implementation in
`internal/mcp` exposes only configured privileged shell tools through
`egg.ToolRunner`. The web UI has a third set of session operations carried over
PTY and tunnel messages. Adding tools separately to those surfaces guarantees
drift.

Extract a transport-independent control package. Its public methods receive a
trusted principal and typed request and return a typed response:

```text
internal/control
  Principal { OwnerID, ActorID, Roles, Grants, Bounds }
  Service
    Capabilities
    ListSessions / StartTerminal / StartAgent
    ReadTerminal / SendTerminal / WaitTerminal / RenameTerminal / StopTerminal
    RunAgent / GetTask / ListTasks / WaitTask / CancelTask
    ListPrompts / GetPrompt / SavePrompt / RunPrompt / RunLoop / RunSwarm
```

Adapters are intentionally thin:

- CLI renders control responses as human text or `--json`.
- stdio MCP maps closed schemas to the same operations.
- Streamable HTTP MCP derives the principal from OAuth and maps the same schemas.
- the web path may keep its existing encrypted PTY/tunnel protocol initially,
  but its handlers call the same control service where the semantics overlap.

`internal/mcp.Server` must evolve from a server coupled to `egg.ToolRunner` into
a generic typed tool registry. A roost can then register built-in control tools
alongside operator-configured privileged tools without shelling out to `wt`.
The operation registry should be the source for MCP schemas, CLI JSON shapes,
grant names, documentation, and contract tests.

## Identity and authorization

Resource ownership and the calling client are different identities:

- `OwnerID` is the stable roost user identity, namespaced as `user:<relay-id>`.
  The same human's Codex and Claude clients therefore see the same eggs/tasks.
- `ActorID` identifies the OAuth client or local MCP client for audit and
  optional narrower grants.

The transport authenticates the request and constructs both identities. Tool
arguments can't select them. The relay user ID is the durable key; email remains
display metadata.

Default authorization is owner-only. A caller cannot list, read, send to,
rename, stop, or reclaim another owner's egg even when it guesses the session
ID. Cross-owner support requires an explicit control grant such as
`terminal.admin` and produces an audit event. Egg control remains owner-scoped
when two people share a ticket workspace.

The existing remote MCP policy uses role `allow` entries as privileged tool
names. Add a separate backwards-compatible `control_grants` field for the new
capability class. Quotas, TTLs, concurrency, path bounds, and maximum
iterations are evaluated server-side after authentication.

## Durable ownership migration

Current state is split across legacy `egg.owner`, the newer
`session.principal`, and task `principal`. Replace this with versioned structured
identity metadata written atomically with mode `0600`, while retaining readers
for all prior formats:

```json
{
  "version": 1,
  "owner_id": "user:7f...",
  "created_by": "mcp-client:codex-..."
}
```

Resolution order is structured identity, `session.principal`, `egg.owner`, then
legacy local-human behavior when no ownership metadata exists. Do not
destructively backfill live eggs. SQLite migrations must be additive and allow
old binaries to ignore new columns during the rollout window.

## Personal provider login on a shared host

The Wingthing OAuth token controls access to roost eggs. Claude and Codex keep
their provider credentials in a separate store.

Every user on a multi-user roost gets an agent home derived from stable user ID.
Email and the presence of an organization in `wing.yaml` play no part. The
current `spawnEgg` condition based on `identity.Email != "" && identity.OrgWing`
is insufficient: an OAuth roost can be multi-user with an empty wing org and
therefore expose the host user's real home to every egg.

Distinct authenticated accounts remain distinct owners even when one human
controls both. Wingthing does not infer an account link from display names or
email similarity, and it never aliases or copies provider homes implicitly.

Personal provider credentials live only in the owner's agent home. Enrollment
is explicit:

1. start an owner-scoped login egg and complete the vendor login interactively;
2. retain that credential in the owner's protected agent home; and
3. let later headless or interactive eggs use that home.

A missing credential returns structured `auth_required` state and a safe handoff
to the owner's login egg. Raw provider tokens never appear in MCP arguments,
task records, logs, templates, or environment summaries. A roost may inject a
host-shared provider API key only when the operator explicitly selects that
credential mode. Root on the roost host remains trusted; Wingthing does not
claim to conceal credentials from host root.

## Agent runs should feel like sub-agents

`agent_start` proves model arguments can reach both real CLIs, and it exposes a
PTY. Agent orchestration needs a semantic task surface. The dogfood run required the caller to handle a
`TERM=dumb` confirmation, send a prompt, send Enter again, plant a sentinel,
wait on raw terminal text, and read a large ANSI snapshot. Worse, waiting for a
sentinel matched the sentinel in the input prompt and falsely reported
completion. This contract fails the basic requirements for orchestration.

Add a provider-neutral high-level operation:

```json
{
  "agent": "claude",
  "model": "opus",
  "cwd": "/work/ticket-123",
  "prompt": "review the current stack",
  "label": "merge-audit",
  "unattended": true
}
```

`agent_run` creates a durable egg in the agent's headless structured-output
mode and immediately returns `run_id`, `session_id`, normalized agent/model,
state, and credential mode classification. It never returns a credential.
`agent_wait`, `agent_result`, `agent_events`, `agent_steer`, and `agent_stop`
complete the lifecycle. Native argument passthrough remains an escape hatch,
but ordinary model swapping is one `model` field. Provider adapters own the
exact mapping (`--model opus`, `-m gpt-5.6-terra`) and exact-argv tests.

Completion comes from the child process and structured provider events, not
text appearing on a terminal. The semantic result is bounded plain text plus
structured usage/error data. A terminal snapshot remains available for human
reattachment and debugging, but is never presented as an agent result.

Long-running prompt APIs should also become submit-first. `prompt_run` currently
blocks the MCP request; the roost needs durable submit, get/list, wait, cancel,
and retry operations before it can be reliable factory infrastructure.

## Workspace rules

Remote working directories cannot be arbitrary caller-supplied host paths.
Canonicalize the requested path and prove it is inside one of the owner's
allowed roots or a workspace granted to that owner. Reject symlink escapes after
resolution. Same-owner eggs may share a ticket workspace, which is how a build,
review, and repair loop collaborates without sharing credentials or egg control.

For programmatic shared-host launches, an `egg.yaml` may choose network domains,
resource limits, agent behavior, and auditing. The roost constructs the
filesystem boundary.
It mounts curated operating-system runtime paths read-only, the owner's stable
agent home read-write, and configured workspaces read-write. It rejects the
filesystem root, Wingthing state, and any workspace root containing the host
account home. A native agent executable is copied into the owner's home before
launch so the host account's installation tree stays outside the jail.

This sealed shared-host mode currently requires Linux namespaces. The browser
workflow keeps its existing sandbox contract during the programmatic-control
rollout. Remote MCP launches require `wing.yaml` workspace paths. Browser
adoption follows the Linux compatibility matrix and an explicit migration
decision.

## Audit contract

Every operation records timestamp, stable owner, actor/client, operation, target
resource, authorization decision, effective bounds, isolation mode, and a digest
of arguments. Logs may include non-secret normalized fields such as agent,
model, and workspace ID. They never include raw prompts or secrets by default.

## Rollout

1. Extract the typed control service without changing the current web behavior.
2. Move the existing local MCP tools onto it and keep stdio contract tests.
3. Add task lifecycle and semantic `agent_run` operations, including a generic
   model field and provider-specific exact-argv tests.
4. Fix shared-roost identity, per-user homes, credential mode, and path bounds.
5. Register built-in control operations on OAuth HTTP MCP alongside configured
   privileged tools.
6. Run two-user shared-roost integration and real provider-login canaries.
7. Move overlapping web handlers onto the control service after parity is
   proven. Any intentional browser protocol break gets its own discussed plan.
8. Reuse the same operation messages over the encrypted tunnel for external
   wings only after the shared-roost path is solid.

## Acceptance tests

- User A starts an egg; user B cannot discover or operate it by guessed ID.
- Two OAuth clients owned by A can collaborate on A's eggs and tasks, while
  audit events retain distinct actor IDs.
- A's Claude and Codex credential homes are not readable or writable by B's egg.
- An unauthenticated provider returns `auth_required`; completing owner login
  enables only that owner's later runs.
- A path outside the owner's allowed roots and a symlink escape are denied.
- Owner metadata and task status survive roost restart and old eggs remain
  reclaimable under the compatibility rules.
- Quota, TTL, cancellation, and explicit administrative grants are enforced and
  audited.
- Existing Slide shared-roost browser sessions still start, reconnect, resize,
  read history, and stop eggs.
- Codex Terra and Claude Opus can each be selected with the same `model` field,
  complete a real task using the user's subscription login, and return a
  semantic result without terminal parsing.

## Non-goals

- a raw `wt` command execution endpoint;
- a new projected-wing or container topology;
- passing raw provider credentials in API calls;
- treating a shared workspace as shared egg ownership;
- protecting secrets from root on the roost host; or
- external-wing control transport in the first shared-roost release.

## Final proving ground: private roost

After the shared-roost acceptance matrix is green, run the same pattern as a
private roost on `ehrlich.dev`. Use Hopper there as a real workload target and
prove private enrollment, personal Claude and Codex login homes, owner-scoped
terminal and task control, model selection, cancellation, semantic results,
workspace sealing, and audit records end to end. This is the final proving
ground for the route and follows completion of the shared control-plane
implementation.
