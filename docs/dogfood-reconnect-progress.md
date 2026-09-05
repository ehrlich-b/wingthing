# Private dogfood reconnect goal

Status: in progress, September 5, 2026. No production or release authority.

## Acceptance checklist

- [x] Preserve the complete hardening worktree outside temporary storage.
- [x] Checkpoint the existing tracked and untracked work locally.
- [ ] Install and document the persistent private `wt-dogfood` entry point.
- [ ] Recover its connection and list only the authenticated owner's jobs.
- [ ] Recover safe observations within three retries, 60 seconds, and the original deadline.
- [ ] Preserve authorization revalidation, ambiguous-launch denial, and restart interruption.
- [x] Exercise the composed native workflow with deterministic failure fixtures.
- [ ] Cross the actual 15-minute identity lease on the owned VMs with a live fixture.
- [ ] Complete a real Terra/Sol hardening job with all Mac clients disconnected.
- [ ] Discover and retrieve exact evidence from a fresh Mac client without remembered IDs.
- [ ] Rerun integration, org-mode, sandbox, and compatibility gates.
- [ ] Record exact source/artifact digests and repeatable operating commands.

## Preservation

The worktree moved intact from `/private/tmp/wingthing-dogfood-hardening` to
`/Users/ehrlich/repos/wingthing-dogfood-hardening`. Local checkpoint `071cb3a`
contains all 39 previously modified/new files. The original checkout remains
on `fix/unlimited-direct-runs`; its modified recovery memo retains SHA-256
`cdb33de152c4cf96c0e93bcbf11e275e239ffbf979ca1389be5f86e051146715`.

Generated patches remain artifacts, not automatically applied changes. No tags,
releases, pushes, merges, production changes, or credential-cache copying.

## Source verification, September 5

The owner-scoped listing, bounded observation recovery, persistent wrapper, and
installer are implemented. The wrapper is not installed and the new binaries
are not deployed yet. Both owned VMs were checked idle and still run
`dogfood-72c7be52603d-f18a51038f51`; the old completed job remains separate
evidence, not acceptance of this reconnect goal.

The composed fixture uses a real relay, authenticated encrypted signaling,
Pion data channels, the native review backend and coordinator, real pinned Git
workspaces, and sandboxed fixture executables speaking the Codex JSONL adapter
protocol. It does not call a model provider. Its ten cases cover submitting
client disconnect, a three-second injected lease, transient disconnect,
revocation/cross-owner denial, failed tests, a requested revision, revision-limit
exhaustion, an accepted launch with its acknowledgment discarded, an unavailable
worker, and an actual ten-second job timeout. It asserts exact persisted child
counts, original job/run bounds, digest-bound artifacts, and independent test
workspace identities. The production lease remains fifteen minutes.

| Gate | Latest completed result |
| --- | --- |
| `make check` | Passed: web tests/build, Go unit suite, local build |
| `make test-integ` | Passed |
| `make test-compat` | Passed against v0.144.1, including 67 CLI surfaces and both mixed-version directions |
| `make test-web` | Passed: org 22/22, legacy-org, hosted direct-only policy |
| `make test-linux` | Passed full Debian sandbox/runtime battery, including all ten composed cases |
| `make test-linux-ubuntu` | Passed full Ubuntu sandbox/runtime battery, including all ten composed cases |
| `make test TEST_FLAGS='-race -tags integration -run TestReviewJobComposedNativeWorkflow -count=1 -timeout=180s'` | Passed all ten cases on macOS under the race detector |
| `make test TEST_FLAGS='-tags integration -run TestReviewJobComposedNativeWorkflow/timeout -count=3 -timeout=120s'` | Passed three consecutive real timeout fixtures |
| `make test TEST_FLAGS='-run TestDogfood -count=10 -timeout=180s'` | Passed ten repetitions of private dispatch, connection recovery/failure, and immutable installation |

Full local logs are under `dist/dogfood/`. The live fifteen-minute lease fixture,
fresh-client retrieval from the installed entry point, and disconnected real
Terra/Sol task are still outstanding. Exact new deployment digests will be
recorded after building and verifying private artifacts.

The first composed run exposed a process-global tunnel-key cache indexed only
by the sender key. It now binds both peer identities; the native composed test
and `TestTunnelKeyCacheBindsBothPeerIdentities` cover the correction. Fixture
shutdown drains hijacked WebSocket handlers before closing SQLite, and Linux
test images now include the actual operator scripts and instructions.
