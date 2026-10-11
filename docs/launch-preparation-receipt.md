# Shared launch preparation, round 3

Base: `eed1635ab2a9e9eda7a3359a850324daf816ae2d`. Local changes only; no push.

`Server.RunSession` and `launchplan.Inspect` now call `egg.PrepareLaunch` for
policy resolution, sandbox specification, adapter arguments, state preparation
and callback-spool validation. The planner's separate directory checker and
Codex preparation boolean are removed. Profile directories come from the same
mount resolver; required spools come from the actual hook/notify builders.
Provider-network admission and native-run support are returned by preparation,
rather than independently reconstructed by preflight.

Dry preparation uses actual `os.MkdirAll`, `openProviderHome` and
`openProviderPath` operations on a bounded, disposable directory-entry mirror.
It retains dangling links, rebases every link into that mirror, reads no source
file contents and removes temporary state on return. The Mac final policy
builder validates the generated spools against its emitted write rules and
rechecks them when constructing a sandbox. Native reader no-follow restrictions
remain intact; preparation now refuses those incompatible layouts early.

The three round-3 fixtures are committed in
`internal/sandbox/launch_preparation_parity_test.go`. Their oracle calls the real
preparation with native filesystem effects on test-owned state. Five dangling
cases/controls, four no-follow reader cases, and six callback-policy cases cover
both spools, HOME/ancestor variants, descendant bans and literal-pin controls.
Two preflight calls per case compare stable reports and unchanged source
inventory (modes, sizes, mtimes, contents and symlink destinations). Additional
unit tests compare the whole native/dry specification and check mirror
containment and Claude's deferred settings effect.

Validation on Go 1.26.9, darwin/arm64, private offline caches, two threads and
`taskpolicy -b nice -n 15 make -j1`:

| Gate | Result | Evidence |
| --- | --- | --- |
| Three regression families | PASS | 15 variants/controls, no skips |
| Affected in-process suites | PASS, scoped as below | 531 top-level tests; 983 including subtests; no skips |
| Whole-repository `go vet -p 2 ./...` | PASS | `.scratch/logs/round3-final-vet.log` |
| Compile `cmd/wt`, without executing it | PASS | `.scratch/wt-launch-preparation`; `.scratch/logs/round3-final-build.log` |
| `git diff --check` | PASS | `.scratch/logs/round3-final-diff.log` |

The reproducible scoped invocation is `.scratch/round3-final.mk`, target
`round3-final`. Its tests cover all agent/config/fsutil/launchplan tests, the egg
package with the exclusions below, sandbox policy/preparation/differential
oracles, eggclient launch-policy tests, and the in-process doctor command tests.
Final per-package JSON logs are `.scratch/logs/round3-final-*.jsonl`. Earlier
receipts and the external review directory are unchanged.

FAIL on unchanged baseline: `TestDeadlineKillsProcessGroupAndReportsSurvivors`
and `TestUnboundedStopKillsProcessGroupAndReportsSurvivors` report that process
group cleanup/descendant inventory cannot be verified on this Mac. The failures
were reproduced from an archive of `eed1635`; see
`.scratch/logs/round3-baseline-process.jsonl`. The final scoped egg run excludes
those two tests. The initial broad run also hit the Mac Unix-socket path limit
in the Codex capture fixtures; a shorter clone-private TMPDIR fixes that test
setup and both capture tests pass in the final run.

REQUIRED-UNRUN/exclusions: the requested
`TestClaudeLifecycleRefusesHostSettingsOutsideFinalPolicy` and built-binary
`cmd/wt` tests; installed Codex hook-trust/provider probes; native confinement,
Linux mount execution and loopback transports; real provider acceptance; and
`TestVTermAuditReplay`, which depends on an external live audit recording.
No skipped or excluded gate is promoted to PASS.

The exact effects deferred by dry preparation are
`LaunchState.ClaudeSettings` (settings merge and file generation, which may read
credential-bearing caller settings) and the installed version probe behind
`CodexCapability` (replaced with digest-bound saved output through the runtime
parser). A foreign platform's final policy builder and Linux's native namespace
mount policy remain unrun. Proxy listeners, browser/tool shim installation,
config snapshots, native sandbox creation, control endpoints, PTY and provider
execution stay after the shared preparation boundary and are never run by
preflight. The mirror does not prove host ownership, ACL, read-only-mount or
kernel enforcement behavior; saved capabilities and native acceptance retain
those responsibilities.
