# Bryan Wingthing Direct-Control Field Report

Status: feature branch proven on a real shared roost; public canary deployed

Date: 2026-08-25

Branch: `feature/direct-control-free-tier`

This is the durable handoff for the first real-host exercise of the direct agent
manager. It records what was actually tested, what dogfooding changed, what remains
unproven, and how to restore the test host. It supplements the product brief and
the direct-control design; it is not a claim that the feature is on the main site.

## Outcome

The core product loop works:

1. A native MCP client logs into a coordination roost.
2. The roost returns an access-filtered wing roster and exchanges WebRTC signaling.
3. MCP and terminal payloads move directly between the client and the selected wing.
4. An outer Claude Sonnet can inspect the wing, create and recover durable terminals,
   launch an inner Claude Sonnet in a sealed organization-mode workspace, wait for
   its semantic result, exchange durable messages, and reconnect later.
5. With `hosted_relay: deny`, legacy hosted terminal and tunnel payload requests are
   rejected even though direct control continues to work.

After the Bryan exercise and full regression gates passed, the same committed tree
was deployed to `wingthing.ai` as Fly release v301. The production deployment has its
own evidence and rollback record in
[wingthing-ai-production-canary-2026-08-25.md](wingthing-ai-production-canary-2026-08-25.md).

## Field topology

The target was `bryan-wingthing.pants.taxi`, an Ubuntu 22.04 shared roost running as
the unprivileged `wingthing` service user behind nginx and a valid HTTPS certificate.
The feature binary replaced `/usr/local/bin/wt` reversibly; the systemd unit, nginx,
database location, and existing users were retained.

The direct MCP client ran as the roost service identity and connected through the
public HTTPS name. It discovered wing `e4d4904295254339899892e2` with transport
`direct-webrtc`. The field test used one physical wing. Two-wing selection and
qualified resource routing are covered by the black-box in-process WebRTC test, not
yet by two separately administered physical machines.

## End-to-end evidence

| Behavior | Result | Evidence |
| --- | --- | --- |
| Direct wing discovery | Pass | `wing_list` returned Bryan with `mcp_transport: direct-webrtc`. |
| Direct terminal lifecycle | Pass | Started terminal `2505ba6b`, read two output canaries, listed and renamed it, then recovered it through fresh connectors. |
| Client disconnect persistence | Pass | The connector exited and reconnected without changing terminal identity or output. |
| Roost restart persistence | Pass | Repeated roost restarts preserved terminal PID `504140` and the two sessions that predated the feature deployment. |
| Direct-only policy | Pass | After changing to `hosted_relay: deny`, direct terminal reads continued to work. |
| Hosted payload denial | Pass | Authenticated `pty.start` and `tunnel.req` WebSocket canaries both returned `hosted relay payload transport is disabled by this wing`. |
| Content-free denial audit | Pass | Journal records contained operation and policy decision, not terminal bytes or tunnel payloads. |
| Org path boundary | Pass | Out-of-scope working directories were rejected; allowed shared-roost workspace execution succeeded. |
| Outer-agent orchestration | Pass | Claude Sonnet used the direct MCP tools to inspect, launch, wait, recover, message, rename, and read. |
| Inner semantic agent run | Pass | Run `t-20260825-222236-c9d5f8f2` completed as Claude Sonnet and returned `INNER_SONNET_OK_5bcaa8e`. |
| Sandboxed artifact write | Pass | The inner agent wrote `inner-sonnet-5bcaa8e.txt`; SHA-256 was `db13a3ab18d317662c5d14adb1cba918d90fdcba06a3da11598e4848b3739f54`. |
| Durable owner message | Pass | A fresh outer agent sent message `msg-77d372dd-3094-46b4-8bb4-db3fac8cf8ec` on channel `dogfood`. |
| HTTPS | Pass | Native MCP and WebSocket canaries used the public HTTPS name with the installed valid certificate. |
| Exact committed build | Pass | Final binary reports `feature-direct-db0dc78`; its SHA-256 matches the locally cross-compiled artifact, and a fresh direct connector reached `wingthing_capabilities` over WebRTC. |

The outer and inner model selection was explicitly `sonnet`; no Opus agent was used.

## Defects found by real Sonnet dogfooding

### Claude Code MCP metadata was rejected

Claude Code adds the MCP-standard `_meta` member to `tools/call` parameters. Both
local and direct Wingthing MCP handlers used strict JSON decoding and rejected the
entire call as an unknown field, so every tool appeared broken to a real agent even
though the hand-written harnesses passed.

The handlers now share a tool-call envelope that accepts `_meta` while preserving
strict rejection of every other unknown envelope field. Local protocol and direct
WebRTC harnesses send metadata, and a negative test pins strictness.

### Shared-host semantic runs could not start or authenticate Claude

The semantic `agent_run` path handed the relative command `claude` to a sealed
`deny:/` jail. PATH lookup cannot occur after the host root is replaced, so the
run failed with `lstat claude: no such file or directory`. After resolving that,
Claude still had no credential because shared-host mode correctly strips ambient
provider keys but the headless path did not install the file-backed API-key helper
used by interactive organization sessions.

The headless path now resolves the catalog's real command before entering the jail,
atomically installs the native runtime under the owner's private home, and gives
Claude the existing 0400 file-backed helper without putting the provider key in the
agent environment. This also handles Cursor's executable name (`agent`) rather than
assuming every catalog name equals its command.

The Linux integration now asserts all of the following in one run: the helper is
usable inside the jail, the key is absent from the environment, the agent can write
only its assigned workspace, and another user's secret remains invisible.

### Sandbox diagnosis hid the real host failure

Bryan's `/tmp` directory had mode 0755. The service user could create user
namespaces, but the capability probe could not create its temporary directory.
Wingthing discarded that cause and claimed the kernel security profile was the
problem. The Linux battery then skipped the most valuable non-root sealed-jail
assertion whenever preflight failed.

The probe now returns the exact failing operation, child output, and OS error. Help
text distinguishes WSL2, AppArmor, disabled user namespaces, and a zero namespace
limit without guessing over the actual cause. Once a host demonstrates the required
namespace primitive, a later Wingthing preflight failure is a test failure rather
than a skip. Bryan's `/tmp` was restored to the standard sticky mode 1777; `wt
doctor` then reported `linux available (user namespaces + seccomp)`.

### The Linux security Make target selected the client architecture

The test client is an arm64 Mac, while its Colima Docker daemon is native amd64.
`make test-linux` built arm64 binaries and copied them into an amd64 image, producing
`Exec format error`. Security tests now select and validate the Docker daemon's
native architecture, as the existing browser battery already intended to do.

The corrected battery also caught two portable-test assumptions: the runtime image
does not promise a Go toolchain, and an unsandboxed Linux policy must report
`unrestricted`, not claim proxy enforcement merely because the platform is Linux.

## Regression and security gates

The final tree passed:

- focused package tests for MCP, agent orchestration, and sandboxing;
- the complete unit suite (`make test`);
- the complete integration suite (`make test-integ`);
- the repository-wide race detector (`go test -race ./...`);
- `go vet ./...`;
- Debian 12's privileged Linux sandbox battery;
- Ubuntu 24.04's Linux battery, including current real Node-based Claude Code in
  the sandbox; and
- the shared-host sealed-jail integration cross-compiled and run directly on Bryan
  as the relevant non-root Linux host.

Both distro batteries passed `TestJail_LinuxProxyBypassHasNoRoute`: an agent with
the proxy environment removed has no alternate route. They also passed the full
direct MCP, organization policy, credential, persistence, audit, and shared-host
semantic-run test sets. Environment-dependent tests for unavailable external
agents or local Ollama skipped explicitly rather than weakening a required gate.

## Compatibility observations

- Two live sessions created by the prior v0.144.1 deployment survived every feature
  roost restart with their original PIDs.
- The feature used the existing database in place; a pre-deploy copy was retained.
- `hosted_relay` remains additive. Omission preserves the existing `allow` behavior,
  while Bryan was deliberately changed to `deny` for this exercise.
- Existing entitled/private browser relay behavior remains covered in automated
  compatibility tests. Bryan's live browser relay was intentionally denied after
  the direct path was proven.
- Organization-mode members remain owner- and canonical-path-scoped. Shared-host
  semantic runs refuse privileged isolation and do not inherit the roost account's
  provider environment.

## Operator state and rollback

At the end of field testing, Bryan intentionally remains on the exact committed
feature build `feature-direct-db0dc78` with `hosted_relay: deny`. The installed
binary SHA-256 is
`306644f6a24750ad60da694c0aca8905e498098aabbc5e8d5678c8f47c26a3ef`.
After the final install and restart, all three tracked session PIDs (`128260`,
`178503`, and `504140`) were still alive, `wt doctor` reported the Linux sandbox
available, and the direct capability response identified the same build. The
long-running direct test terminal and its small dogfood workspace remain available
for inspection. They are test resources, not user data.

Rollback artifacts on Bryan:

- prior binary: `/usr/local/bin/wt.pre-direct-5bcaa8e-v0.144.1`
- pre-feature state copy: `/opt/wingthing/.wingthing/backups/pre-direct-5bcaa8e/`

The service unit uses `KillMode=process`, which is why detached agent/session
processes survive a roost service restart. A rollback should restore the saved
binary, restore the database copy only if schema compatibility requires it, and
restart `wingthing-roost` while verifying the detached PIDs before and after.

## Remaining product friction

The direct manager is credible but not yet a polished default:

- A fresh human's `wt login` and first MCP enrollment were not exercised; field
  testing reused the roost service identity. The free portal must make this path
  copy-paste simple without exposing a durable bearer token.
- The roster still centers an opaque wing ID. Friendly stable labels and host
  context are necessary before a person or LLM comfortably chooses between home
  and office machines.
- An out-of-bounds workspace error is truthful but should return the caller's
  allowed roots or an immediately actionable discovery operation.
- There is no typed prepare-workspace/worktree transaction. Today an orchestrator
  composes terminal commands and `agent_run`, which works but is harder to make
  idempotent and audit semantically.
- Browser-direct terminal transport is not implemented. With hosted relay denied,
  the browser should present direct-MCP setup and policy status instead of looking
  like an empty or broken session manager.
- One gateway can aggregate several wings, but separately administered roosts do
  not yet federate. The desired home-roost/office-roost peer topology remains a
  separate directory, authorization, revocation, and conflict-resolution project.
- A physical two-machine field canary and an N-1 published-client compatibility
  canary remain before broad rollout or merging the feature to main.

## Release recommendation

Keep this on the feature branch while adding the fresh-human enrollment and physical
two-wing canaries. The public v301 deployment is an explicit, reversible production
canary: existing accounts retain temporary relay parity, while newly created free
accounts receive the direct-control posture. Do not merge to main or broaden claims
until enrollment, upgrade, rollback, and browser-readiness paths are explicitly
exercised.
