# Testing Wingthing

Wingthing needs evidence at several boundaries. A provider adapter can have the
right argv while the sandbox fails. A synthetic PTY can route correctly while
an upstream CLI removes a flag. A web page can render while MCP and the browser
disagree about which sessions exist.

## Current commands

| Command | What it runs | What it omits |
| --- | --- | --- |
| `make test` | Go unit and package tests | tagged integration tests, browser, real provider harnesses |
| `make check` | web build, `make test`, binary build | every tagged and external E2E tier |
| `make test-vuln` | pinned `govulncheck` plus npm's current advisory database | unreachable vulnerable Go symbols and non-Go/npm dependencies |
| `make test-integ` | in-process relay, PTY routing, P2P, tunnel, and synthetic agent lifecycle | native sandbox enforcement and browser rendering |
| `make test-compat` | real last-release and candidate binaries: historical migrations, CLI/flag surface, task-state round trip, both gateway/wing upgrade orders, PTY startup, and rollback reopen | third-party wrappers and unsupported pre-baseline releases |
| `make test-linux` | Debian container with privileged Linux sandbox, CLI, and namespace batteries | Ubuntu-specific behavior |
| `make test-linux-ubuntu` | Ubuntu 24.04 version of the Linux battery | macOS and browser |
| `make test-web` | seeded organization-mode roost (including per-identity provider-profile routing), empty-enrollment legacy org canary, and hosted direct-free/relay-entitlement deployments driven by Playwright | local MCP, headless runs, real OAuth provider |
| `make test-provider-swap` | real supported CLIs against local models, direct and through Wingthing | hosted models and shared-roost HTTP MCP |
| `make test-e2e` | both Linux batteries plus `make test-integ` | `make test-web` and `make test-provider-swap` |

The last row is easy to misread. `make test-e2e` is not the complete promotion
matrix.

## Evidence ladder

Use the cheapest layer that can disprove the claim, then add the layer that
crosses the claimed boundary.

1. **Schema and pure logic:** strict JSON, grants, bounds, graph validation,
   ownership, migrations, and provider argv.
2. **Process contract:** deterministic mock agent, PTY lifecycle, cancellation,
   restart, and task state.
3. **Transport contract:** authenticated relay routing, encryption, attach,
   spectate, and multi-user isolation.
4. **Native enforcement:** unprivileged filesystem, network, namespace, seccomp,
   cgroup, AppArmor, and Seatbelt canaries.
5. **Client contract:** browser and real MCP clients against a running portal.
6. **Provider contract:** published agent CLI and a real model perform an exact
   observable action.
7. **Cross-client contract:** one client creates work and another discovers,
   supervises, and stops the same resource.

An exit code or completion sentence is weak evidence for agent work. Prefer an
exact artifact, structured result, state transition, or denied operation.

Native Linux tests must assert the agent's effective UID, PID namespace, host
process visibility, and denied-secret visibility from inside the sandbox. The
non-root sealed-jail regression added in `2795bd3` is the reference shape; a
root-running Docker test alone can mask this failure class.

When a native battery is copied to a remote host, preserve ordinary executable
mode (`0755`) and stage it on the native Linux filesystem. A root-launched test
binary owned by another host user and copied as `0700` cannot re-exec itself after
the test deliberately drops capabilities in a nested user namespace; that is the
kernel enforcing the requested boundary, not a product failure. Cross-user CWD
fixtures must likewise have a traversable parent owned by the selected test user.
For PID isolation, compare the outer and inner `/proc/self/ns/pid` identities:
`NSpid` may contain only one value once procfs has correctly been remounted inside
the private namespace.

## Pattern acceptance matrix

| Pattern | Required deterministic gate | Required live gate | Current gap |
| --- | --- | --- | --- |
| Human, local wing | unit plus Linux/macOS sandbox and native attach | real interactive agent startup | covered across separate tiers |
| LLM, local wing | registry-defined local tool schemas, owner isolation, run lifecycle | Codex and Claude start/wait/result | provider smoke covers older prompt tools, not the current run lifecycle |
| Human, personal remote wing | relay/tunnel plus browser session lifecycle | browser attaches to a real registered wing | shared-roost browser canary is the closest automated case |
| LLM, personal remote wing | encrypted remote control RPC, target auth, qualified resources, grants, bounds, and two-wing connector reconnect | real MCP client controls two external wings without relay payloads | Passed on an external macOS wing plus Bryan: one built connector listed both direct targets, launched real Codex and Claude sessions, observed distinct responses, and stopped the qualified sessions. Fresh-user production enrollment remains a separate gate. |
| Human, shared roost | two-user browser, path ACL, per-identity provider profile, credential home, restart | canary shared deployment | automated tier asserts one identity loads its persisted Claude profile while a second identity cannot inherit it; Bryan's public HTTPS org canary covers the real deployment path |
| LLM, shared roost | OAuth HTTP MCP plus direct MCP, two owners, two actors, path bounds | Codex and Claude OAuth login and semantic run | native local MCP launched and controlled real Claude on Bryan; real OAuth-client semantic runs remain missing |
| Several portals | qualified IDs, independent auth, fan-out inventory | one client routes work to two real portals | target registry doesn't exist |

## Compatibility matrix

Wingthing has deployed users. Every transport, identity, entitlement, schema, or
database change must identify and test the affected rows below. The organization-mode
shared roost is a required promotion target, not an optional dogfood check.

| Axis | Required rows |
| --- | --- |
| Component versions | N-1 wing / N gateway+browser; N wing / N-1 gateway+browser; all N |
| Account cohort | temporary migration entitlement, new free, active Pro, self-hosted local user |
| Deployment | public hosted, private split gateway, all-in-one roost, organization-mode shared roost |
| Wing ownership | personal owner, org owner, org member, outsider |
| Protection | unlocked, wing locked, owner passkey, member passkey, revoked credential |
| Resource state | no sessions, active session, detached session, completed run, active run during upgrade |
| Database | fresh database, each supported migration baseline, copy of deployed schema |
| Client | current browser, N-1 browser assets where supported, stdio MCP, HTTP MCP OAuth, native direct MCP |

For every changed protocol, record which mixed-version rows are supported. An
unsupported combination must fail before mutation with an actionable upgrade message;
it must not time out, partially create a resource, or fall back to a weaker policy.

Database tests cover both embedded migration sets (`internal/store/migrations` and
`internal/relay/migrations`). They assert data relationships, not only that startup
returns nil: owners, org memberships and roles, wing registrations, passkeys, sessions,
tasks/runs, entitlements, OAuth clients, and audit metadata must survive.

Compatibility never means weakening a security negative control. When a new binary
correctly rejects behavior an old binary allowed, the suite preserves the old fixture
and asserts the explicit denial, remediation, and rollout contract.

## Cross-client conformance suite

Build one black-box suite from the operation registry. Run it through the local
CLI, stdio MCP, HTTP MCP, and browser API adapters.

The first scenarios should be:

1. Start a persistent agent session through MCP. The browser lists it under the
   same wing and owner, attaches, sends input, and stops it. MCP then reports it
   gone.
2. Start a browser session. MCP lists the same qualified resource, reads its
   snapshot, renames it, and stops it. The browser receives the state change.
3. Submit `agent_run` through MCP. The browser shows its pending, running, and
   terminal states, events, result, agent, model, workspace, owner, and actor.
4. Connect two wings to one portal. A list call returns both; a start call on A
   never appears on B; a guessed cross-wing session ID is rejected.
5. Authenticate two MCP clients as one owner and one client as another owner.
   Same-owner clients can exchange messages and see owned work. The other owner
   can't infer resource existence.
6. Restart the wing and portal at each lifecycle state. Durable records survive;
   orphaned processes receive an explicit failed state; no pending state remains
   forever.
7. Run the same tests once in sandbox mode and once with an explicit outer VM
   boundary. Every response and audit row reports the effective isolation.
8. Seed sessions with the N-1 wing, upgrade the gateway first and wing second, and
   repeat in reverse order. Reattach, wait, steer, stop, and verify ownership after
   each step.
9. Repeat the multi-wing scenarios as an org owner, org member, and outsider in
   organization mode. The outsider cannot list or infer resources; the member remains
   path-bounded; external personal wings never become shared service wings.

Session termination assertions must wait for process exit and disappearance from
the active inventory. Interactive shells may ignore SIGTERM; a command that merely
accepted the signal request is not evidence that the session stopped.

The suite should consume generated schemas and capability data. Hard-coded tool
counts such as `14`, `20`, or `27` should be replaced by an expected
operation set for each adapter and version.

The first contract checks now derive exact local and HTTP MCP operation sets
from `internal/control`, verify adapter metadata and schemas, and compare
`wing_list` with the browser's access-filtered roster. This is a useful unit
boundary, not a substitute for the black-box cross-client suite above.

## Workspace and placement tests

When logical workspaces are added, test state rather than copying a large home
directory:

- resolve the same workspace ID to different canonical paths on two wings;
- refuse a missing, stale, conflicted, oversized, or symlink-escaped replica;
- materialize an exact Git revision in an isolated worktree;
- transfer selected untracked inputs with a manifest and content digests;
- retain the authority side and conflict policy in provenance;
- run offline against a ready local replica without portal access;
- refuse remote-only execution while offline with a structured reason; and
- send a preview from a remote wing to a browser on the local device without
  exposing an unbounded port.

Credential tests must use canary secrets. A second owner, ordinary host user,
and sandboxed process should fail to read them. The test report must still state
that host root and hypervisor administrators remain trusted.

Memory tests need explicit revisions. A lifecycle hook should produce a proposed
memory diff, race with another writer, detect the stale revision, and retry or
request review. An instruction that merely asks the agent to keep notes current
doesn't pass.

## Promotion policy

A pull request should run the deterministic tiers affected by its boundary. A
release candidate should add browser, native sandbox, real client, and provider
evidence.

Feature work is test-first at the contract boundary. Characterize deployed behavior
before refactoring it, add a failing regression or acceptance test before the fix
where practical, and retain both positive and negative controls afterward. A new
happy-path test without denial, failure, timeout, or compatibility coverage is not
sufficient for authorization, transport, persistence, or sandbox work.

Do not chase a repository-wide coverage percentage. Require evidence for claims:
owner access is paired with outsider denial, allowed egress with proxy bypass denial,
fresh schema with deployed-schema upgrade, new/new components with N-1/N behavior,
and successful lifecycle with cancellation/restart. Every dogfood bug gets the
narrowest deterministic regression test that would have caught it.

The current CI shape is:

- required fast job: web build, unit tests, binary build, schema generation
  check, and `git diff --check`;
- required protocol job: `make test-integ` plus adapter conformance;
- required Linux jobs: Debian and Ubuntu native-architecture batteries;
- required browser job: `make test-web`;
- required compatibility job: immutable historical migrations, CLI/flag surface,
  task-store round trips, and live N-1/N gateway-wing PTY tests in both upgrade
  orders; the browser job adds the four-principal organization-mode and legacy
  enrollment suites;
- scheduled or protected-environment job: published agent and hosted-model
  canaries; and
- tag workflow: consume artifacts and evidence from an already approved commit
  instead of rebuilding an untested tag in isolation.

Provider failures need a direct control beside the Wingthing path, as the
current provider-swap harness already does. Preserve logs and a machine-readable
manifest containing source commit, binary digests, host/kernel, agent versions,
model routes, test names, durations, skips, and exact assertions.

Skips should be classified:

- **unsupported:** the host can't provide a capability outside the release
  claim;
- **missing fixture:** a required binary, model, or profile wasn't installed;
- **not selected:** an opt-in tier wasn't requested.

A required promotion job fails on a missing fixture. It doesn't turn that case
into a green skip.
