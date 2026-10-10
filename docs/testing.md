# Testing Wingthing

Wingthing needs evidence at several boundaries. A provider adapter can have the
right argv while the sandbox fails. A synthetic PTY can route correctly while
an upstream CLI removes a flag. A web page can render while MCP and the browser
disagree about which sessions exist.

## Current commands

| Command | Coverage and prerequisites |
| --- | --- |
| `make check` | All untagged Go tests, including Android wire contract and every former focused unit selection; web syntax/Node unit tests, Vite and host binary build. Go and Node/npm; no provider or Docker requirement. |
| `make gate` | Default `integration compat static` profiles below. Built stable/preview binaries use temporary fixture state. |
| `make gate GATE=integration` | Tagged `e2e` protocol, Codex-native and lifecycle-order fixtures; packaged preview channel isolation, fake-provider onboarding and reader-drain proof; on Linux, fake-SSH remote isolation with sandboxed host eggs. No vendor/model login. |
| `make gate GATE=compat` | Real configured-baseline and candidate binaries: migrations, CLI/flag surface, task-state round trip, both gateway/wing upgrade orders, PTY startup and rollback reopen. Defaults to `v0.144.1` and `v0.147.0`; `WT_COMPAT_BASELINE_REF` selects one override. Tags must exist locally; pins are not automatically the previous release. |
| `make gate GATE=static` | Vet, race on touched Go packages, pinned `govulncheck`, npm advisory audit and release CLI contract. Network/advisory databases required. |
| `make e2e-linux` | Debian and Ubuntu privileged Docker sandbox/CLI/namespace batteries. `LINUX_DISTROS=debian` or `ubuntu` selects one; binaries must match the Docker daemon's native architecture. |
| `make e2e-web` | Seeded org-mode, legacy enrollment and hosted direct-free/relay-entitlement Playwright canaries in Docker; needs Node/npm, Docker and browser image. |
| `make e2e-mac` | Native tagged sandbox/CLI tests (including jail and real-egg input lease), fake-provider context, bound-stdio socket refusal and host-mailbox parent/child proofs. macOS required; no real model login. |
| `make gate GATE=claude` | Installed real Claude CLI against a loopback fake API; missing vendor binary fails. CI pins Claude Code 2.1.260. |
| `make gate GATE=provider-swap` | Opt-in real CLIs with local Ollama/LiteLLM models and direct controls; prerequisites and assertions below. |
| `make gate GATE=input` | Standalone real-egg preview browser-lease fixture on a supported native Unix host. |
| `make gate GATE=coverage` | Go statement coverage to `COVERAGE_OUT` and function summary; diagnostic, not a promotion claim. |
| `make release` | Local stable cross-platform assets, built-binary command contract and checksums. `CHANNEL=preview` packages the host preview and Linux preview artifact. No publication occurs. |
| `make deploy` | `check` and default `gate`, then Fly deployment; operator action after release verification. |
| `make ops ACTION=build` | Host binary only, seeding placeholder embedded assets when absent. Other actions: `android-vectors`, `status`, `scale`, `deploy-edge`. |
| `make proto`, `make web`, `make serve`, `make clean` | Protocol generation, web tests/build, local gateway build/run, local artifact cleanup. |

Profiles can be combined: `make gate GATE='integration compat'`. Real-provider
profiles are opt-in; selecting one requires its fixtures. Fast `check` does not
run tagged integration, compatibility, native sandbox, browser or provider tests.
No single E2E target is the complete promotion matrix. `android-build` and
`android-check` were retired because this checkout has no Android Makefile;
the Android wire contract stays in `check`.

Static race selection uses buildable untagged packages with Go files changed
from `RACE_BASE` (default `HEAD~1`), including uncommitted/untracked files.
Tag-only or other-platform directories stay in their integration/E2E lanes. Set it to the branch's comparison commit
for local work. CI uses the PR base or previous push commit. `RACE_PACKAGES=./...`
requests the full repository and remains mandatory in the release workflow.
`GOVULNCHECK_VERSION` pins the scanner, while advisory databases remain current.

## Shared-roost Claude settings regressions

Deployment policy and personal state are separate. Cover both on every release:

- Host model/effort reach an existing user's isolated session without replacing
  that user's theme, settings, credentials, onboarding, or project trust.
- A fresh second user gets the same host policy, never another user's profile.
- Reconnect and a new session after exit retain onboarding and preferences.
- A policy change affects the next launch; an old session exiting cannot restore
  stale host settings over the new deployment.
- Missing policy preserves Claude's default behavior; unreadable or malformed
  policy fails visibly instead of silently selecting the vendor default.
- Host API keys, hooks, permissions, and unrelated environment settings are not
  included in the model-policy projection. The existing credential helper and
  shared-host jail retain their separate boundaries.
- An explicit per-session model selection still works.

Unit tests pin the projection and profile/snapshot behavior. The browser canary
drives real browser → roost → isolated egg startup, reconnect, exit and relaunch.
The Linux shared-host task test checks the same policy in the sealed jail.
`make gate GATE=claude` requires the actual vendor CLI and inspects its outgoing
model/effort request using fixture credentials and a loopback server only. CI and
release pin Claude Code 2.1.260 for that gate; rerun it with the installed runtime
when deploying a different Claude Code version. A mock named `claude` cannot pass
this gate.

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

Provider containment regressions for GAPS #12/#13 are untagged egg tests:

```sh
go test ./internal/egg -run 'Test(SessionContainmentStopAndTimeout|DeadlineKillsProcessGroupAndDetachedDescendants|SubreaperContainsImmediateOrphans|ContainmentLaunch|ProcessTreeRejectsReusedIdentities|IdentifiedSignalDoesNotKillReusedPID|ProcStatIdentityWithComplexName|CgroupMembershipIncludesNestedGroups)' -count=1 -v -timeout=90s
```

`TestSessionContainmentStopAndTimeout` runs the real server/PTY lifecycle against
Python 3 multiprocessing, including its resource tracker and a `setsid`
grandchild, for stop, deadline timeout and provider exit. Linux requires writable
cgroup v2 delegation; its absence is an explicit unsupported skip. If creation
succeeds but attachment/cleanup fails, the test fails. Python absence is a
missing-fixture skip, not acceptance. Run this command on the coordinator's
delegated Linux host as well as macOS; a Linux cross-build cannot close the
runtime gate. `TestSubreaperContainsImmediateOrphans` uses a dedicated Linux helper
and deliberately disables cgroups to exercise adoption of an already orphaned
daemon. Launch-barrier tests require failed startup to execute no provider, cover
zero-argument commands, and preserve inherited network descriptors. Identity
negative controls refuse reused PIDs, parents, groups and sessions.

See [containment limits](security.md#lifetime-containment-limits-without-elevated-authority)
before interpreting an empty descendant inventory as universal cleanup evidence.

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
fresh schema with deployed-schema upgrade, new/new components with the explicitly
configured historical-baseline/candidate behavior,
and successful lifecycle with cancellation/restart. Every dogfood bug gets the
narrowest deterministic regression test that would have caught it.

The current CI shape is:

- required fast job: `make check` (web/unit tests and build, including Android
  wire contract); static job adds vet, race, vulnerability and CLI contract checks
  plus `git diff --check`;
- required protocol job: `make gate GATE=integration` plus adapter conformance;
- required Linux jobs: Debian and Ubuntu native-architecture batteries;
- required browser job: `make e2e-web`;
- required compatibility job: immutable historical migrations, CLI/flag surface,
  task-store round trips, and live configured-baseline/candidate gateway-wing PTY
  tests in both upgrade orders; the browser job adds the four-principal
  organization-mode and legacy
  enrollment suites;
- scheduled or protected-environment job: published agent and hosted-model
  canaries; and
- tag workflow: run deterministic and macOS gates on the tagged commit, build
  release assets once, then pass those exact artifacts to the publication job.

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

## Live provider smoke

The release smoke test answers one deliberately small question: can a real
model, behind a real supported harness, make a real tool call through
Wingthing and leave the intended result in the intended working directory?

The success signal is the exact 12-byte file `Hello World!`. A zero exit code,
a startup banner, a completion marker, or prose that merely describes a tool
call does not pass.

### Matrix

`make gate GATE=provider-swap` runs these opt-in cases:

| Harness | Provider path | Direct control | Through Wingthing | Assertion |
|---|---|---:|---:|---|
| Claude Code | Anthropic Messages -> LiteLLM -> Ollama Qwen3 4B | yes | yes | Claude `Write` creates exact file |
| Codex | OpenAI Responses -> LiteLLM -> Ollama Qwen3 8B | yes | yes | Codex command execution creates exact file |
| Gemini CLI | Gemini generateContent -> LiteLLM -> Ollama Qwen3 8B | yes | yes | Gemini `write_file` creates exact file |
| Hermes | custom OpenAI endpoint -> Ollama Qwen3 8B | yes | yes | Hermes `terminal` creates exact file |
| OpenCode | OpenAI-compatible provider -> Ollama Qwen3 8B | yes | yes | OpenCode `write` creates exact file |
| Ollama | native chat/tool API, plus Wingthing text adapter | yes | yes | exact structured call is safely dispatched; adapter returns marker |

Local MCP discovery, policy, and session survival are covered by isolated
socket and fake-provider fixtures. The removed synchronous workflow tools
are no longer part of the live provider matrix.

Cursor Agent remains in the ordinary real-startup battery. It can select models
inside its vendor boundary but does not expose the same arbitrary local/provider
substitution contract, so this matrix does not claim that Qwen is running inside
it. A `--model` flag alone does not qualify: the release matrix is for a harness
whose provider endpoint can be redirected to a non-vendor or local
implementation without patching that harness.

### Prerequisites

The smoke host needs:

- branch-built `wt`
- real `claude`, `codex`, `gemini`, `hermes`, `opencode`, and `ollama` executables
- Ollama listening on loopback with `qwen3:4b` and `qwen3:8b`
- LiteLLM listening on loopback with `test/live/litellm-config.yaml`

The pinned LiteLLM environment is intentional. LiteLLM 1.95.0 currently
declares a FastAPI range whose newest resolution is incompatible with its
proxy import, so the canary pins FastAPI 0.136.3 in
`test/live/litellm-requirements.txt`.

Example service setup:

```bash
ollama pull qwen3:4b
ollama pull qwen3:8b
python3 -m venv /opt/wingthing-agent-canaries/litellm-venv
/opt/wingthing-agent-canaries/litellm-venv/bin/pip install -r test/live/litellm-requirements.txt
/opt/wingthing-agent-canaries/litellm-venv/bin/litellm \
  --config test/live/litellm-config.yaml --host 127.0.0.1 --port 4000
```

The provider shapes follow the upstream supported extension points: Claude
Code's gateway environment, Codex custom model providers using the Responses
wire API, Hermes's custom OpenAI-compatible provider, and OpenCode's local
Ollama provider.

### Running on NewPC

NewPC keeps real binaries behind `*-real` canary names so the synthetic Linux
suite can continue to own the ordinary command names. Run the matrix from the
WSL checkout with explicit paths:

```bash
WT_SMOKE_WT_BIN=/usr/local/bin/wt \
WT_SMOKE_CLAUDE_BIN=/usr/local/bin/claude-node \
WT_SMOKE_CODEX_BIN=/usr/local/bin/codex \
WT_SMOKE_GEMINI_BIN=/usr/local/bin/gemini-real \
WT_SMOKE_HERMES_BIN=/usr/local/bin/hermes-real \
WT_SMOKE_OPENCODE_BIN=/usr/local/bin/opencode-real \
WT_SMOKE_OLLAMA_BIN=/usr/local/bin/ollama-real \
make gate GATE=provider-swap
```

Run `python3 test/live/provider_swap_smoke.py --phase direct` to isolate
provider/harness failures or use `--phase wingthing` to retest only the
Wingthing boundary. On failure the script retains its isolated workspace and
per-case logs under `/tmp`; on success it removes only that self-created
workspace. It never uses the daily Wingthing database, deploys a roost, tags a
release, or touches Slide.

### Compatibility controls

These are part of the verified contract, not hidden test indulgences:

- `WT_PROVIDER_BASE_URL` tells Wingthing that a swapped provider is the network
  boundary. A loopback value selects local-only networking and avoids injecting
  a cloud domain proxy into the harness.
- Codex receives `--skip-git-repo-check` because Wingthing tasks may run in any
  directory. When and only when Wingthing supplies the outer sandbox, its
  adapter disables Codex's inner sandbox; nested Linux `bwrap` namespaces fail.
- The Codex canary disables reasoning. LiteLLM 1.95.0 otherwise maps Codex's
  structured Responses reasoning object into Ollama's scalar `think` option and
  fails before inference.
- Gemini CLI 0.54.4 uses its native Gemini `generateContent` protocol against
  LiteLLM. Its fresh-home settings explicitly select `gemini-api-key` auth;
  auto-selecting the newer internal `gateway` type currently fails the CLI's
  own auth validator before the otherwise valid local request is sent.
- `WT_HERMES_TOOLSETS=terminal` narrows Hermes's large tool catalog for a small
  local model. Hermes also requires a declared 64K runtime context for reliable
  tool use.
- OpenCode receives `--auto` only inside Wingthing's outer sandbox. Its prompt
  names the real `write` function; a vague request can make a small model print
  plausible JSON without issuing a tool call.

This is a promotion gate, not a claim that every task will succeed with an 8B
model. It proves that Wingthing preserved cwd, environment, provider routing,
sandboxing, harness tool dispatch, process completion, and the resulting
filesystem effect for one deterministic task.

## Provider evidence and preview acceptance

Agent support requires separate catalog/argv, synthetic PTY, real credential-free
startup, live model completion and MCP orchestration evidence. `check` covers
parsers, schemas, bounds, storage and cancellation without model tokens;
integration fixtures prove routing rather than upstream CLI behavior. Native
Linux tests distinguish unsupported kernel features from broken available
features. Missing real CLIs are skips in exploratory batteries, and failures in
required provider profiles. The [archived provider matrix](archive/agent-support.md)
is a dated result, not a guarantee for the current release.

The preview fixture tiers cover channel/state isolation, provider data binding,
logical conversation reservations, native receipts/lifecycle, cursor replay,
checkpoint races, qualified inventory, protected targets and durable wake retry.
`e2e-mac` also runs a configured parent's direct-server nested-proxy negative
control and the host mailbox fake-provider proof. `CONVERSATION_FIXTURE_ROOT`
must be under the OS-account home, outside the workspace, `/tmp` and provider
write roots. `CONVERSATION_BINARY` and `CONVERSATION_BINARY_SHA256` can pin the
mailbox proof to a reviewed executable; `PREVIEW_CONTEXT_BINARY` selects the
context fixture executable. Other native proofs still build the current preview.
A fake protocol pass does not establish authenticated provider acceptance.

Before calling the personal preview complete:

1. Start isolated preview; check channel, clean enrollment/credentials and stable
   sentinels without changing stable sessions.
2. A configured parent launches two named children and observes native readiness
   and exact prompt text receipts, then inspects responses, tools and lifecycle.
3. Complete one child and block the other for human attention. Deliver completion
   once; inspect and route the exact human decision without automatic approval.
4. Reopen browser/restart control. Preserve links, cursors, pending IDs and old
   execution tails without duplicate input or launches.
5. Test provider exit/cold continuation separately, stop only the exact target,
   then repeat on an existing authorized remote route without policy weakening.

Real credential save/read, subscribed model startup/continuation, browser visual
QA, physical remote mounts, full authenticated parent-to-two-child execution and
human approval routing remain open in [TODO](../TODO.md#personal-preview-acceptance).
Native Codex parser/journal fixtures do not establish live supervisor readiness.
The composed Android/iOS wire and core fixtures do not establish APK/Xcode or
physical-device acceptance, native pairing or passkey approval.
