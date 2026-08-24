# Local-first branch merge readiness

Audit date: 2026-08-23.

Branch: `feature-local-first-terminal-routing`, audited from the immutable
`f59fe8a` snapshot based directly on `origin/main` at `3665624` (`v0.143.0`).

## Current evidence

The committed 25-commit base and successive security-review snapshots were
tested independently. The current local gates pass:

- `make check`
- `make test-integ`
- `git diff --check`

Native ARM64 Linux gates also pass:

- the complete Debian 12 container battery;
- the complete Ubuntu 24.04.4 container battery;
- focused Ubuntu reruns after the final doctor and host-preflight changes.

Each battery compiles and executes `run-tests`, `sandbox-tests`, and `wt-tests`.
They cover namespaces, seccomp, filesystem masks, overlayfs HOME behavior,
diagnostic retention, a real Node Claude launch, and the production shared-host
`runTaskToWithOptions` path. The shared-host fixture projects a native agent
binary and proves its personal Claude login state and workspace are visible,
another owner's login state is sealed, and ambient host credentials are absent.

The final review candidate's three-binary suite passes on native x86-64 Ubuntu
24.04.3 under WSL2 kernel 6.6.87.2, with user namespaces, seccomp, overlayfs,
and the root-deny jail active. Its `wt` SHA256 is
`3a10e9c102bd983e43ea4b0e0defcbef7279bbb13831fa96eba615de7e962132`.
The exact five artifact digests are:

- `wt`: `3a10e9c102bd983e43ea4b0e0defcbef7279bbb13831fa96eba615de7e962132`;
- `mock-agent`: `a310af469155c88aeb0889d459c46030f5e14a732b4ca0f4b0c43d774355afd9`;
- `run-tests`: `6c614f4e56a7bfed995c48b1aaf26a23242feb9a502f13f6cbaa8888de688a0a`;
- `sandbox-tests`: `9fd6121c6334959f4a1d9ba8bb24d0ede1639969114e0ad1d14d05b269c66eb4`;
- `wt-tests`: `ac865cfb3f9176b02f973992d5e877233b4bd9a4b8c4fb9a58ac18964eb4c458`.

The run included a real Claude 2.1.185 launch and the current shared-host,
private-procfs, persistent-home, one-shot-environment, symlink, and read-only
bind-remount regressions. WSL exposed one additional defect before the final
pass: a minimal `MS_REMOUNT|MS_BIND|MS_RDONLY` call discarded inherited mount
flags and failed with `EPERM`. The corrected remount path preserves the live
mount's supported VFS flags, the capability probe now exercises that exact
deny-file operation, and the focused native regression passes. AppArmor is
disabled on this WSL host, so the separate Arli AppArmor canary remains a
deployment gate rather than evidence supplied by this run.

The final seccomp install uses `SECCOMP_FILTER_FLAG_TSYNC`, checks both errno
and Linux's positive unsynchronized-thread return, and the agent-side negative
ptrace and mount probes pass. This makes filter inheritance independent of the
Go scheduler thread selected for the later agent fork/exec.

The exact candidate also reached both real Claude and Codex through typed
standard-sandbox `agent_run` calls on WSL. Each provider then rejected its
stored OAuth state as expired or already refreshed. That is a host credential
renewal gate, not a sandbox launch failure, and means this WSL run does not add
a fresh semantic-review verdict to the earlier successful provider canaries.

`make web` also passes. npm reports five high-severity advisories in build-time
dependencies; `npm audit --omit=dev` reports zero production dependency
vulnerabilities. Handle any npm audit update in its own dependency PR.

The current shared-roost control layer passes `make check` on macOS. Its tests
cover typed native registration, OAuth owner/actor propagation, two-user
session isolation, fail-closed workspace bounds, symlink canonicalization,
caller filesystem widening, shared-host credential environment filtering,
exact model argv, semantic task lifecycle, cancellation races, failed-parent
steering, concurrent stdio wait/stop, runner orphan recovery, and migrations
through `013_task_egg_config.sql`.

The Linux sandbox gate passed on an owned Proxmox VM running Ubuntu 24.04.4,
kernel 6.8, and AppArmor's restricted-unprivileged-userns policy. The first
unprivileged `ai` run reproduced a fail-open mount bug: policy resolution listed
the deny paths while AppArmor rejected the mounts. The corrected artifact then:

- reported the sandbox unavailable before an AppArmor profile existed;
- refused `wt egg` in 0.00 seconds without launching the mock agent;
- drove `_deny_init` directly under AppArmor's `unprivileged_userns` profile
  and proved the wrapper aborted before the agent marker was created;
- installed an executable-scoped profile through `sudo wt doctor --fix`;
- refused an attempted `/tmp` profile target, accepted the same artifact from
  a root-owned stable path, and prevented a later scratch copy from replacing
  that safe profile;
- denied a unique readable `~/.aws` canary from inside the live egg namespace;
- passed the full unprivileged Linux E2E runner; and
- passed the integration-tagged namespace, mount, seccomp, proxy, and sealed
  root-jail battery as root.

Vendor-binary smoke tests skip when the target lacks those CLIs. Ubuntu's real
Node Claude launch passes; the other provider-specific Linux smoke tests remain
part of release evidence.

Fetched `origin/main` was still the branch base and produced no textual rebase
conflict. The broad working snapshot was preserved in `f59fe8a`; the focused
security-review follow-up is committed as one 33-file layer, including two new
platform files. No rebase, checkout, stash manipulation, or stack surgery was
performed before that review commit.

## Review boundaries

The 25 committed changes introduce the agent catalog, persistent PTYs, task and
prompt persistence, local MCP, sandbox policy work, native discovery, protocol
changes, and design documentation. Those broad features remain the stack's
primary review boundary.

The current follow-up is narrower and consists of five connected defensive
classes:

1. shared-host allowlist-jail, private-procfs, persistent-home, and mount
   hardening;
2. one-shot provider-environment transport and same-UID process liveness that
   fails closed when a PID has been recycled by a foreign owner;
3. local MCP path bounds, ownership error indistinguishability, and audit
   redaction;
4. PTY and tunnel binding to the credential-bound source wing, authorized
   controller, and session owner, including single-flight reattach,
   provisional-route cleanup, browser-identity replacement, and response-time
   request expiry; and
5. portable Linux preflights, mount-flag-preserving read-only remounts, plus
   unit, integration, and native-x86 regression coverage for those boundaries.

This layer deliberately leaves shared-roost product behavior unchanged. The
separate question of restricting wing-wide policy, binary update, and passkey
administration to owner/admin roles still needs an explicit product decision.

## Safe stack shape

The immutable working snapshot exists at `f59fe8a`, and the security layer is
preserved as one auditable defense-in-depth commit. Its five Linux artifact
digests are recorded above; history rewriting and stack surgery remain out of
scope.

The review stack should preserve this dependency order:

1. build/test foundation and agent catalog/process handling;
2. persistent terminal/session runtime and local/SSH attachment;
3. task, prompt, and orchestrator persistence;
4. local MCP adapter and typed terminal/task operations;
5. sandbox policy enhancements, explanation, and agent arguments;
6. principal, ownership, quotas, audit, and database migration;
7. native wing discovery and TOFU behavior;
8. passkey/PTY/tunnel/web security protocol changes; and
9. documentation, site copy, and patterns page.

The exact commit boundaries may move during rebase, but each PR must compile and
provide a coherent user-visible contract. Shared-roost programmatic parity
should start after the control semantics are extracted from the local MCP
adapter. The extracted control package remains the sole semantic implementation
used by `internal/relay`.

## Required gates by stack

Every stack member requires `make check`, `make test-integ`, and
`git diff --check`. Additional evidence:

| Change | Additional evidence |
| --- | --- |
| agent invocation | exact argv for every affected provider; real Codex and Claude canary |
| terminal lifecycle | detach/reattach, idle/wait, crash, restart, old egg reclaim |
| store migration | fresh DB plus upgrades through migrations 005-013; old binary compatibility decision |
| sandbox claims | live denied-file canary as an unprivileged user, fail-closed AppArmor preflight, root sealed-jail battery, and Darwin denial tests |
| principals | two local MCP clients cannot operate each other's eggs/tasks |
| shared roost | two OAuth users, two clients for one owner, path bounds, per-user provider homes |
| PTY/tunnel protocol | old web/new wing and new web/old wing compatibility matrix or an agreed breaking migration |
| native wing discovery | cookie and bearer auth, TOFU first use, changed key, offline wing, duplicate machine ID |
| web/site | `make web`, route/template tests, mobile visual check |

Dogfood evidence is a release gate. At minimum, a Codex Terra task and a Claude
Opus task must be created programmatically, use the human's subscription login,
finish without manual terminal repair, and return structured results.

On 2026-08-22, both provider canaries met that bar:

- Codex `gpt-5.6-terra`, run `t-20260822-173930-2904d98f`, completed in 4m31s;
- Claude `opus`, run `t-20260822-173930-4cd7e0da`, completed in 4m24s.

Both were submitted with `agent_run`, crossed the former 120-second timeout,
finished without terminal interaction, and returned their results through
`agent_wait` and `agent_result`. A later live Opus run proved that a blocking
`agent_wait` and `agent_stop` can share one stdio connection: stop returned
first and both calls observed the durable failed state. Terra follow-up run
`t-20260822-183240-78f50363` completed through `agent_steer` in 1m47s and
returned its semantic result through `agent_result`.

That follow-up correctly highlighted the native-control admission boundary.
Shared-roost mode currently admits every account that completes a configured
OAuth login, matching the existing web-roost policy. Native controls stay
owner-scoped and workspace-bounded; executable host tools stay role-scoped.
The private-roost milestone needs an allowlisted or invite-based enrollment
policy before `ehrlich.dev` becomes a proving ground.

## Promotion sequence

1. Review the stack locally without changing the deployed roost.
2. Run the full cross-platform and two-user matrix, including the Linux gate.
3. Canary a separate shared roost with copied configuration but no production
   provider credentials or user data.
4. Exercise browser compatibility and OAuth MCP against the canary.
5. Agree on any breaking migration before updating the Slide roost.
6. Roll out with a pinned previous binary and database backup available for
   rollback; monitor auth failures, reconnects, egg ownership, and task errors.
7. Promote the public hosted service separately from the Slide roost.

After the shared route is proven, use a private roost on `ehrlich.dev` and its
Hopper as the final end-to-end workload canary.

No merge, tag, release, or deployment is authorized by this document.
