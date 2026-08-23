# Local-first branch merge readiness

Audit date: 2026-08-23.

Branch: `feature-local-first-terminal-routing` at `d121d71`, based directly on
`origin/main` at `3665624` (`v0.143.0`) when audited.

## Current evidence

The committed 24-commit branch and successive dirty-worktree snapshots were
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

The same three-binary suite passes on native x86-64 Ubuntu 24.04.3, kernel 6.8,
bare ext4, and the live Arli host's AppArmor configuration. The deployed hopper
runtime remains SHA256 `7cddba45302a6635...`; the later diagnostics and test-
portability candidate remains local.

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
conflict. The current merge risk is semantic breadth, protocol compatibility,
and the uncommitted layer. The tree currently has 85 modified tracked paths and
24 untracked status entries. A reproducible deployed security binary depends
on this exact WIP state, so source-control preservation precedes any rebase,
checkout, stash manipulation, or stack surgery.

## Review boundaries

The 24 committed changes introduce the agent catalog, persistent PTYs, task and
prompt persistence, local MCP, sandbox policy work, and design documentation.
The dirty layer then combines at least five independent risk classes across 85
tracked files plus untracked source and migration files:

1. local MCP principals, grants, task/session ownership, audit, and migrations
   `008_task_principal.sql` through `013_task_egg_config.sql`;
2. trusted outer-boundary `--unsandboxed` operation;
3. native `wt wings` discovery, bearer access, tunnel client, and TOFU pinning;
4. passkey, PTY, tunnel, and frontend protocol/security changes; and
5. broad documentation, marketing copy, install, and miscellaneous cleanup.

Security protocol changes need a separate review from local orchestration.
Native discovery needs its own visible change.
The task-principal migration needs its own rollback and old-database evidence.

## Safe stack shape

Before rewriting history, create an immutable snapshot commit on a dedicated
local preservation branch and retain the binary patch plus untracked-file
archive. This requires an explicit user-approved boundary because multiple
agents contributed to the shared worktree. The working branch must remain
untouched until the snapshot commit is verified to reproduce the five Linux
artifact digests. Then turn the dirty layer into small local safety commits.

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
