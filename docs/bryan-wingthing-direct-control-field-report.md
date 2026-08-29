# Bryan Wingthing Direct-Control Field Report

Status: feature branch proven on a real shared roost and two physical wings; org/browser path revalidated

Date: 2026-08-25

Last live validation: 2026-08-28

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

The first direct MCP client ran as the roost service identity and connected through
the public HTTPS name. It discovered wing `e4d4904295254339899892e2` with transport
`direct-webrtc`. A later canary enrolled a separate macOS wing through the same
coordinator and exercised both physical machines in one native connector process.

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
| Two physical wings | Pass | One `wt mcp connect` process listed the external macOS wing and Bryan with distinct `wing_id` values and `mcp_transport: direct-webrtc`. |
| Qualified real-agent routing | Pass | The connector started Codex on macOS and Claude 2.1.243 on Bryan, observed `WINGTHING_PHYSICAL_MAC_OK` and `WINGTHING_PHYSICAL_BRYAN_OK` in rendered agent output, and stopped only the two returned session IDs. |

The outer and inner model selection was explicitly `sonnet`; no Opus agent was used.

## 2026-08-28 organization-mode revalidation

Bryan is also the organization-mode compatibility canary, so the direct-only
exercise was not an appropriate steady-state policy for this host. The explicit
`hosted_relay: deny` left from the August 25 test produced a truthful but broken
browser experience: opening a terminal stopped at `hosted relay payload transport
is disabled by this wing`. The host now explicitly uses `hosted_relay: allow`.
Omission still defaults to `allow`, including the current Ansible template, so this
does not change the compatibility default.

The deployed browser canary passed 17/17 checks against
`https://bryan-wingthing.pants.taxi` with zero console errors, page errors, or
failed requests. It proved browser-validated HTTPS (issuer `YR1`), admin and member
identity, per-role path filtering, mobile rendering, the legacy no-enrollment-
allowlist contract, and a real terminal open/identity-lock/resize/detach/reattach/
end lifecycle. The temporary browser session was removed, all temporary login and
membership records were deleted by exact identity and creation timestamp, and
`PRAGMA integrity_check` returned `ok`.

Two additional live defects were found and fixed during that run:

- CLI and local-MCP `send` combined text and Enter in one PTY frame. Claude Code
  treated that as a paste and left the prompt in its editor. Text and Enter are now
  separate PTY frames with a short delay. A fresh sandboxed Claude 2.1.243 session
  received, submitted, processed, and answered a prompt from one `send --enter`
  call with no repair keystroke.
- `session kill` sent SIGTERM and immediately reported `stopped`. Interactive bash
  ignores SIGTERM, so the session remained discoverable. Kill now waits for actual
  exit, escalates after a three-second grace period, and waits for the normal reap
  path. A focused ignored-SIGTERM regression and a fresh deployed interactive-shell
  canary both pass.

The current installed development candidate reports
`feature-direct-org-killfix-20260828`; SHA-256 is
`c1c7307005e414f3bafdcf5ef645c38b8517a01ed62c4c46203bc514fadf0a31`.
The roost service is active, public `/health` returns `{"ok":true}`, and the three
pre-existing sessions (`2505ba6b`, `81bc288e`, and `94093aee`) remain alive. No
test terminal or temporary browser identity remains.

### Native WSL2 security and real-agent canary

The same candidate and Linux test artifacts were also exercised directly on the
authorized Ubuntu 24.04 WSL2 rig (`6.6.87.2-microsoft-standard-WSL2`), outside the
Docker-only root path. The complete Linux agent battery passed. Real installed
Claude, Cursor, Gemini, Hermes, OpenCode, and Ollama binaries each produced PTY
output from inside the sandbox; Ollama completed three exact tool-call/dispatch
cases. The separate low-level sandbox and Linux CLI batteries also passed.

Most importantly, the non-root sealed-jail regression ran as uid/gid 1000 and
proved an outer-to-inner PID namespace inode change, only two visible procfs PIDs,
an unreadable host-process secret, an unreadable denied-path canary, and blocked
mount syscalls. The proxy-bypass test independently proved that removing
`HTTPS_PROXY` leaves no route to a disallowed destination.

This run found two test-harness defects and retained regressions for them: a real
agent helper could inherit stdout and make a synchronous scanner ignore its
deadline, and cross-user tests used a `t.TempDir` leaf beneath a root-only parent.
The observer now honors cancellation even while a writer remains open; non-root
fixtures are top-level, owner-scoped temp directories; and namespace detection
compares `/proc/self/ns/pid` inode identities instead of assuming a private procfs
must expose multiple `NSpid` values. Isolated `/tmp` staging also copies the mock
agent into the declared jail allowlist, so a portable test does not accidentally
depend on `/usr/local/bin`.

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
- Ubuntu 24.04 on WSL2, including six real installed agent CLIs, exact local-model
  tool dispatch, the low-level sandbox suite, the Linux CLI suite, and the non-root
  sealed-jail boundary; and
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
- `hosted_relay` remains additive. Omission preserves the existing `allow` behavior.
  Bryan was deliberately changed to `deny` for the August 25 direct-only exercise
  and restored to explicit `allow` during the August 28 org/browser revalidation.
- Existing entitled/private browser relay behavior remains covered in automated
  compatibility tests. Bryan's live browser relay is enabled because it is the
  organization-mode browser canary as well as a direct-control test host.
- Organization-mode members remain owner- and canonical-path-scoped. Shared-host
  semantic runs refuse privileged isolation and do not inherit the roost account's
  provider environment.

## Operator state and rollback

The current operator state is the August 28 candidate recorded above, with
`hosted_relay: allow`. After the final install, browser run, real Claude run, and
kill-path canary, all three tracked session PIDs (`128260`, `178503`, and `504140`)
were still alive. The long-running direct test terminal and its small dogfood
workspace remain available for inspection. They are test resources, not user data.

Rollback artifacts on Bryan:

- prior binary: `/usr/local/bin/wt.pre-direct-5bcaa8e-v0.144.1`
- pre-August-28 org canary: `/usr/local/bin/wt.pre-org-e2e-20260828`
- pre-Enter fix: `/usr/local/bin/wt.pre-enterfix-20260828`
- pre-kill fix: `/usr/local/bin/wt.pre-killfix-20260828`
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
- Fresh authenticated enrollment and the remaining production account-cohort
  canaries still precede broad rollout. The physical two-machine canary and the
  real N-1/candidate compatibility battery now pass.

The two-machine canary also found three custom-roost UX/privacy defects. `wt start
--roost` printed the public app URL, `wt wing status` validated the token against a
different configured coordinator, and the detached daemon supplemented explicit
`--paths` with a scan of its home-directory cwd. The branch now derives both status
and browser URLs from the active daemon's exact roost (with saved-argument fallback
for an older daemon), records that roost in a private `wing.status`, uses the same
selection for support diagnostics, strips URL credentials from displayed browser
links, and treats explicit paths as a project-metadata disclosure boundary. Focused
regressions cover coordinator precedence, public-host compatibility, self-hosted
`/app/` routing, old-daemon fallback, private status metadata, explicit-path
non-disclosure, and the legacy no-path scan. A rebuilt local canary reported
`projects: 0 found` for the empty canary workspace and the correct Bryan `/app/`
URL; its post-fix network reconnect was not repeated after the execution environment
declined that external connection.

Canary cleanup revoked and deleted the exact temporary Bryan identity, token,
membership, and audit row, removed the stopped Mac/WSL/Bryan canary artifacts, and
terminated two detached Mac wing processes discovered by the cleanup sweep. Database
foreign-key checks remained clean, and Bryan's preserved sessions `2505ba6b`,
`81bc288e`, and `94093aee` remained present.

## Release recommendation

Keep this on the feature branch while adding the fresh-human enrollment and remaining
account-cohort canaries. The public v301 deployment is an explicit, reversible production
canary: existing accounts retain temporary relay parity, while newly created free
accounts receive the direct-control posture. Do not merge to main or broaden claims
until enrollment, upgrade, rollback, and browser-readiness paths are explicitly
exercised.
