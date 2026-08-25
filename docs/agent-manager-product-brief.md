# Agent Manager Product Brief and Gap Audit

Status: working source of truth for `feature/direct-control-free-tier`

Last reviewed: 2026-08-25

Related designs:

- [Direct agent manager and coordination-only free tier](direct-agent-manager-design.md)
- [Bryan direct-control field report](bryan-wingthing-direct-control-field-report.md)
- [wingthing.ai production canary](wingthing-ai-production-canary-2026-08-25.md)
- [Roost deployment model](roost_design.md)
- [Local agent meta-layer](agent-meta-layer.md)
- [MCP service accounts and API credentials](mcp-service-accounts-design.md)
- [Sandbox enhancement design](sandbox-enhancement-design.md)

## Why this document exists

The direct-control design describes one transport and entitlement slice. It does
not by itself capture the product people are asking for or the work required to
make that product safe and coherent. This brief records the use cases gathered
from recent internal Wingthing conversations, compares them to the implementation,
and turns the discrepancies into an ordered engineering plan.

This is the first document to read after context loss. It is deliberately candid
about incomplete or misleading behavior. Checked boxes and passing unit tests do
not override the end-to-end product gates below.

## Product thesis

Wingthing is an agent manager for agents, and also for people.

An owner should be able to put wings on a home machine, an office VM, a private
lab, or a hosted worker; see one authorized inventory; and let either a person or
an agent create workspaces, launch durable agents, inspect semantic progress,
steer or stop work, and retrieve results. Claude, Codex, OpenCode, and other
adapters are interchangeable workers behind the same control contract.

`wingthing.ai` is the hosted coordination plane, analogous to a tailnet control
plane. It provides identity, an access-filtered directory, signaling, and optional
paid encrypted relay. Direct clients should talk to wings without sending their
MCP or terminal payloads through the hosted service. A private roost provides the
same control-plane shape under the operator's trust boundary.

The human browser, local CLI, local MCP, remote MCP, and future automation API are
clients of one resource and authorization model. None should invent a separate
notion of the current wing, owner, session, or workspace.

## Canonical user stories

The following stories recur in internal conversations and should drive scope.

### Multiple machines, one inventory

An owner runs wings on an office VM and a home VM. A browser and an LLM can list
both, distinguish their resources by `wing_id`, and operate either without logging
into each machine separately. Sessions remain alive when the controlling client
disconnects.

### Prepare a workspace and launch an agent

An owner or orchestrator can select a machine, create or reuse a directory or Git
worktree, run bounded setup commands, install or validate project prerequisites,
and launch a selected agent in that directory. The preparation result is typed,
audited, and composable; callers do not have to scrape a short-lived PTY to learn
whether setup succeeded.

### Agents manage other agents

An outer agent can discover available wings and workers, launch Claude or Codex,
wait without polling, inspect structured status and results, steer a run, exchange
owner-scoped messages, and stop it. Model choice is a parameter, not an architectural
fork. Concurrent work is bounded by policy.

### Choose the trust boundary

Wingthing can configure an agent, configure its nested sandbox, provide remote
access, or do any useful subset. A dedicated AI VM, container, or external sandbox
can be the outer boundary without pretending a nested sandbox is active. Conversely,
when Wingthing claims enforcement, filesystem and network policy must be real on
that platform and cannot silently disappear in an escape mode.

### Private human access

A small team can self-host one private roost behind a VPN/tailnet and register
several wings with it. The browser provides a unified view and terminal access.
HTTPS may be supplied by a real hostname and ACME or by a tailnet/VPN reverse proxy,
but the supported topology and trust consequences must be explicit.

### Recurring unattended work

An owner can schedule a prompt or agent run hourly or weekly, query configured
systems such as logs or issue trackers, and deliver a bounded result to a declared
destination such as Slack or email. The workload uses a revocable service identity,
not a human's indefinitely copied OAuth session.

### Shared context and tools

Employees and agents can use Wingthing's MCP endpoint to access configured tools,
role-specific instructions, databases, search, or other context under ACLs and
audit. Server-to-server consumers get first-class service accounts. Wingthing is
the policy and orchestration surface; generated web application hosting belongs to
a separate product.

### Durable working context

Agent memory and project instructions should not accidentally depend on the folder
from which a client was launched. Connecting to another wing can deliberately sync
or mount declared context without copying secrets or unapproved files.

## Slack-derived workflow map

This is an internal product-research map of the recurring requests. It deliberately
does not appear on the public `/patterns` page. The public page contains only
self-contained setup guides for behavior that ships today; gaps stay here until
they become usable product workflows.

| Requested workflow | How to do it now | Pattern or gap |
| --- | --- | --- |
| One office VM and one home VM, durable Claude/Codex sessions, one parent-agent inventory | Register both wings with one coordinator; run `wt mcp connect`; call `wing_list`; qualify every operation with `wing_id`. | `remote-orchestration`; working direct MCP, physical two-host field canary still due. |
| The same multi-machine view for a person, without trusting public payload relay | Run one private roost behind a VPN/tailnet and valid HTTPS, then register the wings with its gateway. Hosted free accounts get coordination/direct MCP; hosted browser relay is Pro/grandfathered. | `shared-web-roost`; browser-direct transport and roost federation remain gaps. |
| Run shell setup, make a directory or Git worktree, then launch the chosen agent there | Use an existing idempotent setup script through `terminal_start`, wait for its completion canary, then call `agent_run` or `agent_start` with the resulting `cwd`. | Compose now; typed `workspace_prepare`/worktree lifecycle is P0. |
| Let an outer agent supervise inner Claude, Codex, OpenCode, or another worker | Register local stdio MCP or the native direct connector; use semantic start/status/wait/result/steer/stop plus durable messages. | `local-subagents` and `remote-orchestration`; working. |
| Choose agent configuration, nested sandbox, remote access, or an outer VM/container independently | Use ordinary egg policy for the nested boundary, or explicitly declare trusted outer-boundary mode when the VM/container is the sandbox. | `local-sandbox`; remote outer-boundary policy parity remains partial. |
| Scheduled log/error review using Prometheus, Grafana, OpenSearch, or databases, followed by a Slack report | Put data access behind authenticated MCP tools. Local scheduled tasks exist, but remote schedule CRUD, revocable service identities, and typed delivery are not shipped. | P1 automation gap. |
| Let local agents use governed Jira/log/database/context connectors | Add the authenticated roost/context-service HTTP MCP endpoint to the local client; keep connector ACLs and audit at that service. | `shared-roost-agents`; working for configured tools. |
| Build and publish a dashboard or small internal app from the agent's result | Wingthing manages the agents, workspace, context, and evidence; hand the artifact to a separate internal hosting product. | Explicit non-goal for Wingthing itself. |
| Pair independently administered home and office roosts once, then browse one merged inventory | Add them as separately named MCP servers today. True peer directory/identity federation is not implemented. | Not a public pattern; federation gap. |

## Supported topology today

The shortest viable private multi-machine topology is one gateway, not peer
federation:

```text
home wing ----\
               +-- one private roost/gateway -- browser and MCP clients
office wing --/
```

Both wings register with the same access-filtered gateway. The browser can aggregate
their sessions, and native `wt mcp connect` can target an external wing directly.
This is the topology to document and dogfood now.

Independent roosts do not discover or federate. The aspirational setup in which a
home roost and an office roost are paired once and thereafter expose one inventory
requires a peer directory, conflict rules, cross-roost authorization, revocation,
and routing that do not exist yet.

## Implementation truth as of 2026-08-25

| User need | State | What is actually true |
| --- | --- | --- |
| Durable local Claude/Codex/OpenCode sessions | Working | Persistent PTYs and semantic agent runs survive client disconnects; the default idle timeout is disabled. |
| Local agent orchestrates agents | Working | `wt mcp stdio` exposes typed terminal, agent-run, message, sandbox, prompt, loop, and swarm controls. |
| Remote agent controls multiple wings | Field-proven on one real wing; two-wing routing proven in-process | `wt mcp connect` exposed Bryan through direct WebRTC to a real Claude Sonnet orchestrator. Terminal and semantic-run state survived connector and roost restarts. The black-box JSON-RPC/two-WebRTC canary proves qualified routing across two wings; a physical home-plus-office canary is still outstanding. |
| Mixed agent backends | Working | Claude, Codex, Cursor, Gemini, Hermes, Ollama, and OpenCode adapters exist. |
| Long-running semantic runs | Working | Start, status, events, wait, result, steer, and stop operations exist. |
| Unified human browser | Entitled/self-hosted only | Pro, grandfathered, or private-roost users can retain the relay browser. A new hosted free account receives setup/readiness UI and no session inventory. |
| Direct browser terminal | Missing | Browser-direct transport is explicitly outside the current slice. |
| Secure direct access to a protected wing | Missing | Native direct MCP rejects locked and per-user-passkey-protected wings because it cannot complete the passkey ceremony. |
| User-selectable direct-only policy | Working on branch | `hosted_relay: deny` overrides Pro, grandfathered, and private-roost relay entitlement at the honest gateway and again at the wing. Omitted policy remains `allow` for deployed wings. |
| Hosted Pro fallback for native MCP | Missing | The connector tells users to enable relay after direct failure, but it has no native relay fallback path. |
| First-class workspace/worktree preparation | Partial | `terminal_start` accepts argv and `agent_start` accepts `cwd`; no typed one-shot execution, worktree object, setup hook, or atomic prepare-and-launch operation exists. |
| Prompt assets, loops, and swarms remotely | Missing | These registry tools remain local-MCP-only. |
| Recurring automation through MCP | Missing | Internal cron support and schedule parsing exist, but there are no schedule create/list/remove MCP tools or delivery targets. |
| Unattended service identity | Designed only | Human OAuth exists; the service-account design has not been implemented. |
| Bring-your-own outer sandbox | Partial | Local MCP exposes trusted outer-boundary mode. The remote direct surface does not expose an equally clear policy contract. |
| Linux network confinement | Working on branch | The route-less network namespace and inherited-FD relay enforce one egress path without root; the WSL2 battery passed. |
| Peer roost federation | Missing | A client selects a gateway URL; independent roosts are separate inventories. |
| Durable context/memory sync | Missing | Context sync remains backlog work. |
| Generated app hosting | Non-goal | Wingthing should give agents tools and context; a separate service should publish apps. |

## Security and policy invariants

These are release requirements, not aspirational documentation.

1. **Coordinator identity is not local consent.** Compromise of a hosted account or
   coordinator must not silently bypass a wing's lock or passkey policy.
2. **Direct-only must be enforceable.** A wing owner can disable hosted payload
   relay even when an account is Pro or temporarily grandfathered.
3. **One authorization model.** Direct MCP receives explicit grants, owner scoping,
   path scoping, spawn/session bounds, and audit policy derived from authenticated
   identity. Nil policy must never accidentally mean unrestricted remote access.
4. **Every resource is qualified.** The stable identity of a wing-owned resource is
   `(wing_id, kind, id)`. Nested returned resources must not lose `wing_id`.
5. **Claims follow the effective boundary.** `--unsandboxed` or outer-boundary mode
   must report that choice and must not imply nested filesystem or network enforcement.
6. **No theatrical egress policy.** Enforce mode either establishes a real kernel
   boundary or refuses to launch. An agent that ignores `HTTPS_PROXY` must have no
   alternate route to a denied host.
7. **Secrets and content stay out of audit.** Audit records identity, target, policy,
   digest, and decision, not prompts, messages, terminal bytes, or credentials.
8. **Remote mutation has durable accountability.** Define which operations fail
   closed when their audit record cannot be written; do not silently market a
   best-effort log as a security guarantee.
9. **Bounds are enforced at the wing.** The client and coordinator are not trusted
   to enforce maximum sessions, spawn rates, execution deadlines, or response sizes.
10. **Direct transport is not the entire security story.** Keeping payload bytes off
    `wingthing.ai` reduces exposure but does not by itself solve authentication,
    authorization, client supply-chain trust, endpoint compromise, or metadata.

## Compatibility is a product invariant

Wingthing has real users and an existing organization-mode shared-roost deployment.
Compatibility is therefore part of the architecture, not cleanup after a new path
works. The direct agent-manager work must be additive until a separately reviewed
migration deliberately removes an old behavior.

The following deployed contracts remain supported while this branch rolls out:

- existing browser terminal start, attach, reconnect, rename, stop, and encrypted
  relay behavior for entitled users;
- existing HTTP MCP OAuth clients and their current tool schemas;
- existing personal wings and org-bound wings registered with the hosted gateway;
- organization owner/member/outsider visibility and role derivation;
- the shared roost's embedded service wing without exposing an external personal
  wing to every authenticated roost user;
- folder-based ACLs and canonical-path enforcement for organization members;
- separate provider login homes and owner-scoped sessions on a shared host;
- existing passkeys, wing allow/revoke records, lock state, and browser ceremonies;
- durable sessions and database records created by the currently deployed binary;
- local self-hosted mode without OAuth, and private OAuth roost deployments that
  intentionally retain relay behavior; and
- configuration files that omit newly introduced keys.

Compatibility rules:

1. **Schemas grow additively.** Existing fields retain meaning and optional new fields
   get safe defaults. Renames require an alias/deprecation period. Unknown fields and
   capability versions fail predictably rather than being reinterpreted.
2. **Old and new components coexist deliberately.** The supported matrix covers an
   N-1 wing with an N gateway/browser and an N wing with an N-1 browser/gateway for
   every protocol changed by the branch. Unsupported combinations return an upgrade
   error before mutation.
3. **Database migrations are forward-only and transactional.** Test both fresh stores
   and upgrades from a copy of the deployed relay and runtime schema. Do not drop or
   rewrite user data in the feature rollout. A rollback uses a database backup when an
   old binary cannot safely read the migrated schema.
4. **Defaults preserve private deployments.** A policy chosen for new public hosted
   accounts must not silently change an existing shared/private roost. Public account
   cohorts and private operator policy are separate inputs.
5. **Security tightening is explicit.** Fail-closed improvements may intentionally
   reject an unsafe operation, but they require a clear error, upgrade/remediation
   path, compatibility test, and release note. Never silently fall back to weaker
   behavior to keep an old client green.
6. **Session continuity is tested.** Upgrade with live and detached sessions, then
   reattach from both browser and MCP. Existing eggs either remain controllable or
   receive a durable, truthful terminal state; they do not disappear or change owner.
7. **Rollback is rehearsed.** Promotion keeps the previous binary and a verified DB
   backup, separates public hosted rollout from the organization-mode roost, and
   records which protocol/database boundary prevents a binary-only rollback.

The organization-mode deployment is the highest-value regression canary because it
combines OAuth, multiple users, role and path policy, shared host credentials, browser
relay, passkeys, durable sessions, and operator-managed configuration. It must not be
the first place a new binary or migration is tried.

## Known contract discrepancies

These are cases where behavior or prose currently says more than the system does.

### Direct protection and authorization

The nil-grants/unbounded direct-server discrepancy is closed on this branch. Every
direct channel now receives an explicit wing-resolved grant set, configured-path
scope, positive session/spawn bounds, and process-shared admission state. Optional
strict `direct_mcp` configuration can narrow grants, change bounds, or disable the
surface; malformed policy fails startup/reload rather than falling back to full
authority. Deterministic, real-data-channel, repeat, race, integration, Linux-build,
and three-user organization-mode browser gates cover the slice.

The direct connector fails closed when a wing is locked or has a passkey for the
user. This is the correct temporary failure behavior, but it means secure enrollment
and the primary direct path cannot coexist. A native passkey-bound authorization
ceremony is required before the direct path can be the default for sensitive wings.

### Human and agent views diverge

The new free browser intentionally clears wing and session state and displays direct
agent setup instructions. It is not a unified human view. Headless semantic agent
runs also do not have a complete browser inventory. The product needs either a
browser-direct client or precise positioning that hosted free is agent-only.

### Relay fallback is described before it exists

Native `wt mcp connect` only establishes a direct control client. When it fails, the
message points to Pro relay, but no code performs that fallback. Until implemented,
docs and errors must say that Pro preserves the hosted browser relay, not imply that
the native MCP connector automatically falls back.

### Workspace setup is possible only through PTY composition

An orchestrator can start an arbitrary command in a durable PTY and later read its
screen. That is useful escape-hatch behavior, not a typed setup API. It lacks a
structured exit code, stdout/stderr bounds, idempotency, workspace identity, and an
atomic handoff to agent launch. It also violates the design principle that callers
should not scrape terminal state when a typed fact can exist.

### Remote automation is narrower than local automation

Prompt assets, bounded loops, and swarms are local-only registry surfaces. Cron has
no MCP surface. Therefore “everything a person can do, an LLM can do remotely” is
not yet an accurate claim.

### Public and private relay policy are coupled

The `wt relay` command defaults OAuth deployments to the public `direct-free` policy.
That is appropriate for `wingthing.ai`, but surprising for a private split gateway
whose operator expects relay access. Public entitlement policy and private gateway
defaults need distinct, explicit configuration.

### Result qualification is shallow

The direct connector adds a top-level `wing_id`, but nested sessions or runs are not
necessarily qualified individually. The contract promises every returned wing-owned
object is self-identifying. Either deepen qualification or define envelopes so the
promise is unambiguous.

## Ordered implementation plan

### P0: make direct remote control safe

1. **Done:** resolve explicit grants, configured paths, identity, role, and positive
   process bounds at the wing; support additive local restriction/disable policy.
2. **Done:** require the resolved policy when constructing direct MCP and remove the
   remote nil-grants/full-access sentinel.
3. **Done:** cover grant denial, owner/member/admin/outsider roles, member paths,
   maximum sessions, reconnect-resistant spawn rate, lock rejection, real WebRTC,
   race detection, and the existing three-user shared-roost browser canary.
4. Extend the policy with an explicit outer-boundary permission and decide/test audit
   failure behavior for remote mutations.
5. Specify and implement the native passkey challenge/response using the existing
   one-time wing nonce and client-bound token semantics. A coordinator assertion alone
   must not satisfy it.
6. **Done:** add a persistent per-wing `hosted_relay: allow|deny` control. `deny`
   wins over account entitlement and is observable in capability metadata and
   content-free gateway/wing audit records.

### P0: prove the actual multi-machine path

Build an end-to-end canary that starts or uses one coordinator and two distinct wings,
runs the actual `wt mcp connect` process, and drives it through the same JSON-RPC
stdio interface used by Claude and Codex. It must prove:

- the authorized roster contains both wings;
- every wing-owned call requires `wing_id`;
- a command addressed to wing A cannot execute on wing B;
- returned resources remain qualified;
- direct MCP request and result bytes do not enter the hosted relay path;
- an unauthorized, locked, revoked, offline, or unreachable wing fails clearly;
- disconnecting the MCP client does not kill the durable terminal or agent;
- reconnecting can list and continue that work.

Run the same canary on the WSL rig after coordinating around existing work there. The
recent WSL battery proves sandbox behavior only; it does not prove the remote
agent-manager path.

The first deterministic boundary is now covered by
`TestConnectMCPStdioRoutesTwoWingsDirectlyAndPersistsAcrossReconnect`. It drives the
connector's JSON-RPC stdio surface, creates independent real WebRTC data channels to
`home` and `office` wing runtimes, requires `wing_id`, checks qualified results,
proves state written to one wing is absent from the other, closes the connector, and
discovers the durable state through a fresh connector. Its coordination spy observes
only `wing.info` and `webrtc.offer`. This deliberately does not satisfy the remaining
two-host, actual-binary/client, NAT, or WSL promotion gate.

### P0: preserve existing users and organization mode

Treat the current shared-roost browser canary and org authorization tests as the
floor, then extend them for this branch:

1. Run the shared-roost browser suite with an org owner, ordinary member, and
   outsider. Prove wing roster, project visibility, path denial, session launch,
   encrypted attach, detach/reattach, rename/stop, account/org pages, and mobile.
2. For owner and member, exercise native direct MCP against the same org wing. Derive
   role and paths at the wing; never accept them from tool arguments or signaling
   payload supplied by the caller.
3. Prove an outsider cannot list, signal, guess, attach to, or infer the existence of
   the org wing or its sessions. Prove roost mode does not make an external personal
   wing globally visible.
4. Use distinct provider-home canary secrets for two users. Each user's agent can read
   its own canary and cannot read the other's; terminal output and audit expose neither.
5. Cover unlocked, wing-locked, owner-passkey, and member-passkey rows through browser,
   HTTP MCP, and native direct MCP. An unavailable native ceremony fails closed without
   disturbing the working browser ceremony.
6. Seed detached sessions and semantic runs using the deployed/N-1 binary, upgrade
   gateway and wing in both orders, and prove inventory, ownership, attach, wait,
   result, and stop behavior.
7. Upgrade copies of both relay and runtime databases from every supported migration
   baseline. Compare row counts and owner/org/resource relationships before and after.
8. Exercise new-free, grandfathered, Pro, legacy private gateway, local roost, and
   OAuth private roost policies. No cohort can acquire broader access from a missing
   timestamp, cache miss, or default branch.
9. Canary a separate deployment with copied non-secret configuration and synthetic
   users. Only after it passes may the organization-mode deployment be upgraded.

### P0: make workspace preparation first-class

Add a bounded semantic execution primitive before building elaborate project types:

```text
exec_run(
  wing_id?, argv[], cwd?, env_names?, timeout_seconds?, max_output_chars?
) -> {
  execution_id, exit_code, stdout, stderr, truncated, started_at, finished_at
}
```

It accepts argv, never an implicit shell string. Environment values come from
operator-approved configuration or explicit non-secret inputs and are redacted from
audit. It enforces path, process, timeout, output, and sandbox policy at the wing.

On top of that, add a Git-aware workspace operation:

```text
workspace_prepare(
  wing_id?, project_root, kind, name, base_ref?, reuse?, setup_steps[]?
) -> {
  workspace_id, path, repository, branch, base_ref, reused, setup_results[]
}
```

`kind` initially supports `directory` and `git_worktree`. Preparation is idempotent
under an explicit `reuse` policy. A later `agent_start` or `agent_run` accepts
`workspace_id`; the wing resolves it to a bounded path. Provide a compound
prepare-and-launch operation only after the two underlying transactions are durable
and independently auditable.

The browser project scanner must periodically rescan configured roots so a newly
prepared workspace appears without restarting the wing.

### P0: make packaging and claims honest

1. Either implement native Pro relay fallback or correct the connector error, pricing,
   and docs to describe only the relay paths that exist.
2. Separate hosted `wingthing.ai` entitlement defaults from private `wt serve` and
   `wt roost` defaults.
3. Replace the compile-time migration timestamp with an explicit deploy-time cutoff
   or migration record. Verify the affected account set before production rollout.
4. Publish the agent-manager setup page and new docs together. A production check must
   include `/`, `/docs`, `/patterns`, capability metadata, a new free account, an old
   account, and a self-hosted roost.
5. State plainly whether free hosted accounts receive an agent-only control path or a
   human session UI. Do not use “unified view” for the readiness page.

### P1: complete the agent-manager product loop

1. Expose schedules as typed create/list/get/pause/resume/remove operations.
2. Implement service accounts and revocable workload credentials before unattended
   delivery or external server-to-server use is promoted.
3. Add declared delivery targets with bounded payloads, retry policy, and content-safe
   audit; start with Slack webhook/app delivery and email only if ownership is clear.
4. Decide which prompt, loop, and swarm resources are safe remotely, then expose them
   through the same registry and wing qualification contract.
5. Add headless agent-run inventory and steering to the human browser.
6. Add deliberate context and memory synchronization with allowlists and conflict
   behavior; never infer that an arbitrary launch directory is durable memory.
7. Add a browser-direct terminal path if unified human access is part of hosted free.

### P2: federated roosts and provider-managed environments

Peer roosts require an explicit federation design: discovery, trust establishment,
identity mapping, ACL intersection, revocation propagation, resource qualification,
conflict behavior, offline state, and routing/fallback policy. Do not reuse the
replica peer directory and call it federation.

Environment provisioning should remain provider-based. Wingthing can define typed,
bounded environment lifecycle controls and adapters for Proxmox, cloud VMs, or other
systems without making any one provider the core runtime.

## Completed coding slice: remote policy propagation

This slice is implemented and verified on 2026-08-25.

1. Before production code, add characterization tests for the deployed behavior:
   personal owner, org owner, org member, outsider, embedded shared-roost wing,
   external personal wing, path ACLs, passkey/lock rejection, and existing relay
   entitlements. Reuse the fixtures in `internal/relay/authz_test.go`,
   `cmd/wt/mcp_direct_wing_test.go`, and `test/web/orgmode.mjs` where possible.
2. Introduce the smallest policy value type needed by direct MCP.
3. Resolve it in the wing-side authenticated direct-channel path.
4. Populate grants and bounds on `localMCPServer` without changing local stdio
   compatibility.
5. Make a missing remote policy fail closed.
6. Add table-driven tests in `cmd/wt/mcp_direct_wing_test.go` for allowed grant,
   denied grant, maximum sessions, spawn rate, member paths, owner identity, and the
   existing lock/passkey behavior.
7. Add an integration row proving existing HTTP MCP and browser org behavior is
   unchanged when direct MCP policy is enabled.
8. Run focused Go tests, the full unit and integration suites, static checks, Linux
   cross-compilation, and then the two-host canary when it exists.

Evidence: focused policy/config tests; real WebRTC grant and org-path tests; ten
repeat runs; race detector; `make test`; `make test-integ`; `make check`; Linux amd64
cross-build; and the seeded organization-mode Docker browser canary (15/15, no
browser errors). The canary also fixed its architecture selection to use the Docker
daemon rather than assuming the client host matches it.

## Completed coding slice: local hosted-relay opt-out

`hosted_relay: allow|deny` is an additive wing-local policy. Default `allow`
preserves every deployed browser and organization-mode workflow. `deny` must be
enforced twice: advertised to an honest gateway so it refuses before routing, and
checked by the wing so a compromised or stale gateway cannot start/attach a relayed
PTY or send a general payload tunnel. Coordination-only signaling remains available
for native direct MCP. Capability metadata and content-free audit must expose the
effective decision.

The branch now enforces that contract at both boundaries. Registration and config
update messages carry the additive policy; authorized portal/MCP roster entries and
encrypted `wing.info` report the effective value. The gateway refuses PTY start,
attach, input, passkey/session control, and general tunnel payloads before forwarding.
The wing client independently rejects those messages, limits the surviving discovery,
signaling, and passkey purposes to 256 KiB, skips relay session reclaim, and records a
private content-free local policy audit. Gateway denials append only actor, wing,
operation, and policy metadata.

Compatibility is deliberate: an N-1 wing omits the field and remains `allow`; an N
wing talking to an N-1 gateway still enforces `deny` locally; unknown explicit wire
values fail closed. Real WebSocket integration covers org owner/member denial plus
continued discovery forwarding, while the existing three-user organization browser
canary covers the omitted/default path. Explicit integration cohorts cover new-free
account denial, a Pro owner, a grandfathered org member, and a private-roost owner;
the local `deny` wins for every otherwise-entitled cohort.

### Test-first working rule

Every implementation patch on this branch carries evidence at the lowest useful
layer and at the boundary it changes:

- pure policy and schema changes get exhaustive table-driven allow/deny/default tests;
- parsers, bounds, and untrusted envelopes get malformed, oversized, unknown-version,
  replay/duplicate, and fuzz or property coverage where useful;
- authorization changes always include owner, member, outsider, cross-owner, revoked,
  and missing-policy negative controls;
- lifecycle changes cover success, failure, timeout, cancellation, disconnect,
  restart, and idempotent retry;
- transport changes cross a real process boundary and include mixed-version behavior;
- database changes use fresh and upgrade fixtures and assert preserved relationships;
- browser-visible changes extend the three-user organization-mode Playwright canary;
- sandbox claims are verified from inside the sandbox on the claimed operating system;
  mocks alone cannot establish enforcement; and
- every bug found while dogfooding receives a regression test that would have caught
  the original failure before the fix is considered complete.

Do not optimize for a coverage percentage or raw test count. “Slathered in tests”
means every important claim has a positive control, a negative control, and evidence
at the boundary where the claim could fail. Tests must remain deterministic by
default; live providers and shared rigs are separate, explicit promotion gates.

## Release gates

The branch is not production-ready until all P0 items have owners or explicit
de-scoping, and the shipped prose matches that scope. At minimum:

- all repository tests and checks pass;
- Linux cross-build passes;
- the WSL sandbox negative-control battery passes;
- the real two-wing native connector canary passes;
- locked/passkey policy cannot be bypassed;
- remote grants and bounds have negative tests;
- the N-1/N component compatibility matrix passes for every changed protocol;
- fresh and deployed-schema database migration tests pass for both SQLite stores;
- the three-user organization-mode browser and direct-MCP canary passes, including
  path ACLs, owner isolation, provider homes, passkeys, and upgrade/reconnect;
- new free, Pro, grandfathered, and private-roost entitlements are verified against
  a deploy-time migration boundary;
- a private one-gateway/two-wing setup has a short, tested HTTPS/VPN guide;
- production pages and binaries describe the same product.

No test is removed or weakened merely because a product path is being repositioned.
If an assertion represented obsolete behavior, replace it with a test of the explicit
migration or denial contract and record why the old behavior is no longer supported.

## Branch and deployment snapshot

At the time of this review:

- branch: `feature/direct-control-free-tier`;
- production canary: the runtime shipped in Fly v301 from `db0dc78`; current release
  v302 from `d3d6024` keeps that runtime and replaces the public Patterns page with
  shipped-only, self-contained setup guides; v301 and v300 remain rollback releases,
  along with a verified pre-deploy database backup;
- the direct-control implementation and Linux egress fix have passed unit,
  integration, static, cross-build, and WSL sandbox testing;
- the branch is not merged to main, but its committed runtime is deployed as the
  public canary;
- the public site now presents Wingthing as an agent manager and `/patterns` is live;
- all 25 accounts present at deployment are before the configured
  `2026-08-26T00:00:00Z` grandfather cutoff; new accounts after that instant receive
  coordination/direct control only unless entitled; and
- the public HTTP surfaces and anonymous native-MCP fail-closed path passed their
  post-deploy canaries. Fresh authenticated enrollment remains outstanding.

Recheck every item before relying on this snapshot. Git history, live deployment,
and account state can change independently.

## Definition of the dream

The feature is real when a new user can put one wing at home and one at the office,
open either an LLM or a browser, authenticate once, see the same authorized inventory,
prepare a worktree, launch different durable agents, leave, reconnect, inspect and
steer them, and schedule follow-up work—while the owner can prove which machine ran
what, which policy bounded it, whether payload bytes were relayed, and how to revoke
access without taking the machines apart.
