# Private dogfood reconnect goal

Status: acceptance proof passed, September 5, 2026. Private dogfood only;
no production or release authority. A fresh native client discovered the real
Terra/Sol job after reconnection and retrieved its verified final evidence.
The generated patch remains an artifact, not an applied or released change.

## Acceptance checklist

- [x] Preserve the complete hardening worktree outside temporary storage.
- [x] Checkpoint the existing tracked and untracked work locally.
- [x] Install and document the persistent private `wt-dogfood` entry point.
- [x] Recover its connection and list only the authenticated owner's jobs.
- [x] Recover safe observations within three retries, 60 seconds, and the original deadline.
- [x] Preserve authorization revalidation, ambiguous-launch denial, and restart interruption.
- [x] Exercise the composed native workflow with deterministic failure fixtures.
- [x] Cross the actual 15-minute identity lease on the owned VMs with a live fixture.
- [x] Complete a real Terra/Sol hardening job with all Mac clients disconnected.
- [x] Discover and retrieve exact evidence from a fresh Mac client without remembered IDs.
- [x] Rerun integration, org-mode, sandbox, and compatibility gates.
- [x] Record exact source/artifact digests and repeatable operating commands.

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
installer are implemented. Local checkpoint `52ed38c` contains the verified
reconnect work. The private entry point is installed and the two owned VMs now
run the artifact identified below. The old completed job remains separate
evidence, not acceptance of the new live lease and hardening requirements.

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

Full local logs are under `dist/dogfood/`. Fresh-client retrieval from the
installed entry point and the live fifteen-minute lease fixture passed. The
subsequent disconnected real Terra/Sol proof also passed, as recorded below.

The first composed run exposed a process-global tunnel-key cache indexed only
by the sender key. It now binds both peer identities; the native composed test
and `TestTunnelKeyCacheBindsBothPeerIdentities` cover the correction. Fixture
shutdown drains hijacked WebSocket handlers before closing SQLite, and Linux
test images now include the actual operator scripts and instructions.

## Private deployment and fresh discovery

Build: `dogfood-52ed38cefbd7-b5e714242344`.

- Source commit: `52ed38cefbd78d698f9ed0e8f2706d097e3b953d`.
- Source/build-input SHA-256: `b5e714242344ba190eee3ccaa44939e8c41c30137608bf7dff825932caa99ca7`.
- Darwin arm64 binary: `71882c29d72fdda462fb08ad08edd49157418cd5803164d84af22152b4581c02`.
- Linux amd64 binary: `1b834c2b1a563bfd3ac541cf4fb2d6542dca0f255d5da32f3d32ce0e0f5f6a2f`.
- Work-1 coordinator PID `78582` and work-2 wing PID `75864` had that Linux
  digest when read from `/proc/PID/exe` after deployment. Both were idle before
  restart. The coordinator's active-job lock was held across its replacement.
- The exact old Linux binary is retained at
  `/usr/local/lib/wingthing-dogfood/dogfood-72c7be52603d-f18a51038f51/wt` on each VM.
- `~/.local/bin/wt-dogfood` uses its own immutable versioned binary and the
  existing private profile. The default `~/.local/bin/wt` retained SHA-256
  `34f014bbc05daba619dfa2672f44ed797d10fa7e8dbd5cb71b93acea15e97035`.

The first installed `wt-dogfood review list --json` established its connection
and found the two older completed jobs in under one second. A fresh invocation
selected the newest successful evidence-bearing row without a supplied ID:
`j-c328435b2105e0b040c0f2c7526f7ccb`. Native result calls retrieved its exact patch,
accepted Sol review, and separate passing test records. Patch digest
`863ae8d7595cfd3383ee236b3c6e98b5c881c9184a6b32ccf4106f619254ff0e`, the review's
digest binding, and both test-output digests were independently recomputed and
verified. Retrieved records are `dist/dogfood/fresh-*`.

While the lease fixture was running, canceling only the Mac master's
`127.0.0.1:17881` forwarding rule followed by `wt-dogfood review list --json`
restored that forwarding rule automatically in under one second. The same job
and original Terra child remained running (`dist/dogfood/recovered-forward.json`).

## Live lease fixture passed

Submit command: `wt-dogfood review submit --spec docs/dogfood-lease-fixture.json --json`.

- Durable job: `j-f73a340ce99905dc49ebc30eb41262eb`.
- Admitted: `2026-09-05T13:39:33.386923189Z`; deadline:
  `2026-09-05T14:09:33.386923189Z`.
- Original Terra child: `t-20260905-133934-7f8b868e17384748`.
- Both source replicas remain pinned at
  `5b8fcfce454f4881e27dc762082845067cc28180` with the existing restricted egg policy.
- The fixture asks the implementer to wait 930 seconds in one foreground
  command, then create one marker. Sol reviews the marker without repeating
  the wait. This is timer-fixture waiting, not claimed software implementation.
- Zero revisions, 1,200 seconds per agent, 30 seconds per test, 1,800 seconds
  total. No worker restart or manual workspace handoff is allowed while active.

Do not count this fixture as the separate required real hardening task.

Both running wing services recorded the real identity lease expiry at
`2026-09-05T13:54:33Z`. The VM coordinator logged `reconnecting observation
agent_wait, retry 1/3` at the same time. The original Terra child remained
`t-20260905-133934-7f8b868e17384748`; there was no replacement implementation
run. The workflow then completed successfully in round zero with Sol child
`t-20260905-135533-c91d52d758a747dc`, a passing review, and both test exits zero.
The original deadline remained `2026-09-05T14:09:33.386923189Z`.

Retrieved patch SHA-256:
`df1c6b3cb40299768efb830f24b191fd412081c4975f822e510fd1ec4305f632`.
Native result records are in `dist/dogfood/live-lease-{patch,review,tests}.json`;
the patch digest, review binding, and both test-output digests were recomputed
and matched. This proves the deployed observation recovery across the actual
lease, not just the shorter injected fixture.

## Real hardening task in the disconnected proof window

`docs/dogfood-confirmed-stages-job.json` freezes the next task: distinguish
unacknowledged submission from an acknowledged child being awaited. The live
fixture provided the reproduction: a persisted implementer run ID with stage
still `implementation_submitting`. Terra may change only
`internal/reviewjob/job.go` and `internal/reviewjob/job_test.go`, adding controlled
launch/wait regressions named `TestReviewJobConfirmedRunStages`. Sol independently
reviews the exact patch; both test gates run all `TestReviewJob` tests. Two
revisions maximum, ten minutes per agent, three minutes per test, one hour total.

Submitted through `wt-dogfood review submit` after the lease fixture succeeded:

- Job: `j-debab30c550cc5876887f41b3c906b03`.
- Admitted: `2026-09-05T14:05:08.311570947Z`.
- Original deadline: `2026-09-05T15:05:08.311570947Z`.
- The submission returned its persisted ID in pending/admitted state, before
  any child ID was returned to the Mac. The CLI exited; all earlier private MCP
  clients had already exited.
- The Mac's exact private SSH master was then closed at `2026-09-05T14:05:08Z`
  (local timestamp precision one second). `lsof` found no established Mac SSH
  connection to either owned VM or the private roost port after closure.
- Local evidence: `dist/dogfood/disconnected-real-submit.json` and
  `dist/dogfood/disconnected-real-at.txt`.

Do not reconnect to either VM or run `wt-dogfood` network commands before
`2026-09-05T15:06:09Z`. This keeps the Mac disconnected through the entire job
deadline plus a shutdown margin. A local foreground timer only waits on wall
time; it does not connect to or coordinate the VMs. Do not restart or resubmit
because no progress is visible during this deliberate isolation window.

After that time, establish a fresh connection with `wt-dogfood review list
--json`, discover the new job rather than injecting its ID, and retrieve patch,
review, and both test records. Require terminal success, execution of the named
new regression in both records, matching artifact digests, independent test
workspaces, and a completed-at time before reconnecting. Verify that Terra
finished after the disconnect and that revision/run bounds held. No manual
workspace copying/handoffs or automatic patch application is allowed.

## Client hardening during the isolation window

The acceptance audit found a real remaining connection-bound gap. A stalled
SSH multiplexer's alive-check response was not bounded by `ConnectTimeout`.
The fake-profile regression first failed after the test harness killed the
wrapper, without an actionable wrapper error. A separate local Unix-socket
probe completed the actual OpenSSH hello exchange and withheld the alive-check
response: `ConnectTimeout=1` still hung after three seconds. With the new
two-second alarm, actual OpenSSH exited on SIGALRM after 2.006 seconds. This
probe used only loopback/local IPC, not either owned VM.

`scripts/wt-dogfood` now bounds SSH checks to two seconds, forward repair to
four seconds, and startup to ten seconds. Its existing health probes remain
bounded to two seconds. Fake-profile regressions exercise all three stalled
operations and require the wrapper's actionable error, not harness cancellation.
On Debian these cases completed in 6.02, 4.01, and 10.01 seconds respectively.
The timeouts do not apply to the native command after connection setup.

The installed Mac wrapper has SHA-256
`944fa6f7c446a14f0212d83ee99168dd77257f427d6be1db44d0926a971bf448`.
The installer also refreshed the operator instructions; both were compared
byte-for-byte with the source. This was a client-script-only installation:
the private binary remains `dogfood-52ed38cefbd7-b5e714242344`, its recorded
Darwin digest is unchanged, and no VM service or provider process was restarted.
The default client and original checkout's dirty recovery memo also retain
their recorded digests. The existing private profile was not copied or replaced.

The recovery audit additionally added
`TestConnectMCPObservationRecoveryCapsLongDeadline`: with a five-minute parent
deadline, all three retries share a single deadline capped at sixty seconds.
This complements the shorter-parent-deadline and read-only/exhaustion tests;
it uses only injected local transport errors and passed three repetitions.

Fresh verification logs under `dist/dogfood/`:

- `check-macos-client-timeouts-final.log`: full `make check`, including the
  additional recovery-deadline assertion.
- `dogfood-hung-control-red.log` and `dogfood-hung-control-green.log`: the
  reproduced client hang followed by passing private-entry regressions.
- `recovery-deadlines-repeat.log`: all observation-recovery unit tests, three runs.
- `linux-debian-client-timeouts.log` and `linux-ubuntu-client-timeouts.log`:
  full sandbox/native batteries, including the stalled-SSH cases and all ten
  composed workflow scenarios.
- `org-web-client-timeouts.log`: org, legacy-org, and direct-policy browser gates.
- `integration-client-timeouts.log`: integration gate.
- `compat-client-timeouts.log`: v0.144.1 compatibility, 67 CLI surfaces and
  both mixed-version directions.

These are client/source verification, separate from the subsequently retrieved
real-job proof below. No native network command or SSH connection to either VM
was made during this client-hardening work.

## Disconnected real workflow passed

The first reconnect was at `2026-09-05T15:43:40Z`, after the entire one-hour
job deadline and isolation margin. `wt-dogfood review list --json` established
the private connection and returned four owner-visible jobs. Selecting the
newest row by creation time, without a supplied ID or success-only filter,
discovered `j-debab30c550cc5876887f41b3c906b03`. Its identity, full pinned base,
both worker targets/models, and original deadline matched the submission.

| Event | UTC, September 5 |
| --- | --- |
| Submission persisted | 14:05:08.311570947 |
| Mac private SSH master closed | 14:05:08, local timestamp precision one second |
| Terra started / finished | 14:05:09 / 14:07:42 |
| Sol started / finished | 14:07:56 / 14:08:25 |
| Final workflow record persisted | 14:08:36.663527035 |
| Fresh Mac reconnect | 15:43:40 |

Native `agent_status` returned `done`, `isolation: standard`, the expected
model, and the unchanged 600-second limit for both exact children:

- Terra on work-2: `t-20260905-140509-7e53b6d98d5e4dfa`.
- Sol on work-1: `t-20260905-140756-7b35fb233f6344d2`.

The workflow succeeded in round zero, with no revisions, after about 208 seconds.
Both test records have exit zero and the exact specified test argv. Both logs
contain the execution and passing result of `TestReviewJobConfirmedRunStages`,
not merely a successful command with no matching tests. The test workspaces
are distinct: `95d4d34b74c9450fa9447a45778c0b4c` and
`5cb93c98798b4f6c8f3b3cf4320b52c1`.

Independently recomputed SHA-256 values:

- Candidate patch: `b1832813f53284826689771a84214d098cede296b650f73ca2b49722a4108775`.
- Implementation test output: `7b2ee0e26bfaccca3f3bd8253c8d70d1c467ca7e0a9fb94f5c291a2f693418c8`.
- Review test output: `e1f1793d553ffd62dea3e0275762ff775d8dcbe29e66f7d75ad8a9f520945e25`.

Sol's structured verdict is `pass` and binds that exact candidate digest.
The patch changes only the two allowed files: four insertions/three deletions
in `internal/reviewjob/job.go` and 147 insertions/four deletions in its test file.
It records the confirmed running stage with the acknowledged child ID before
waiting, with gated regressions for implementation, review, ambiguity, and a
requested revision. No generated patch was applied to either source replica
or to the Mac worktree.

All result artifacts were retrieved through native `wt-dogfood review result`,
not SSH file copying. Local durable evidence under `dist/dogfood/`:

- `disconnected-real-fresh-discovery.json`, `disconnected-real-submit.json`,
  and `disconnected-real-{at,reconnect-at}.txt` establish discovery and timing.
- `disconnected-real-{patch,review,tests,implementation}.json` contain the exact
  native result records.
- `disconnected-real-runs.json` contains the two native child status responses.
- `final-work-{1,2}-runtime.txt` records the subsequent read-only runtime audit.

The runtime audit at 15:46 UTC found the original worker PIDs still serving the
recorded Linux digest `1b834c2b1a563bfd3ac541cf4fb2d6542dca0f255d5da32f3d32ce0e0f5f6a2f`.
Both source replicas remain clean at `5b8fcfce454f4881e27dc762082845067cc28180`.
The audit also retrieved both actual 13:54:33 lease-expiry journal entries and
the coordinator's `agent_wait` reconnect entry from the earlier live fixture.
The installed Mac wrapper still matches local checkpoint `2e9109b`, while its
runtime binary remains the separately identified `52ed38c` private build.

## Completion scope and remaining limitations

This proves one fixed personal-owner implementation/review workflow on the two
owned VMs, including fresh discovery and exact evidence retrieval. It does not
claim a general workflow engine, automatic source synchronization, browser
takeover for headless runs, production readiness, or sustained multi-day usage.
Existing source replicas and provider logins are prerequisites. Coordinator
restart reports interruption without replay; it does not resume active stages.
Future source changes require an explicit pinned-workspace preparation step.
Generated patches still need a separate acceptance/application decision.

No tags, releases, pushes, merges, production changes, credential-cache copying,
or worker restarts during active work were performed. Use
`docs/dogfood-operator.md` for the repeatable native commands.
