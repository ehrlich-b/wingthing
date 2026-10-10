# What keeps Wingthing from perfect

Snapshot: 2026-10-10, wave3 run-recovery stack based on `ac90fdc`, branch
`codex/wave3-wingthing-runs-20261010`. P1 = security, lost work or a blocked primary
workflow; P2 = broken contract or missing acceptance; P3 = polish/operability.
“Fixture verified” means an isolated built-binary check, not real-provider,
production, mobile, sandbox-confinement or weekend-long acceptance.

The historical reports below were read in full from
`~/repos/claude/state/wingthing-dogfood-bugs.md` (#1–#21). Some defects were fixed
in superseded branches; that does not imply the fix exists in this stack or the
installed binary. Every row preserves a concrete remaining check.
Earlier dogfood observations are retained below with their original scope.

## Historical dogfood reports

| Bug | Severity | Status in this checkout and remaining closure check |
| --- | --- | --- |
| #1 MCP host exit kills workers | P1 | Egg-backed execution survives a killed stdio client and full wing replacement in the canary. Repeat a long real Codex/Claude run, terminate the parent host, and recover the same run/session/wing IDs and partial transcript from a fresh authorized client. Old installed taskrun builds remain exposed. |
| #2 OPENAI_API_KEY in egg argv | P1 | `internal/eggclient/egg.go:83` transports the environment through a private `.egg.env` file, not argv. Run a sentinel credential regression and inspect wrapper/provider argv on Mac and Linux; direct user-supplied `--env SECRET=...` still places that user's argument in argv. |
| #3 injected memory/schedule boilerplate | P2 | `internal/wingsession/runs.go:256` retains the original prompt; exact fake native output passes without injected suffixes. Inspect one real provider's received prompt and confirm unchanged UTF-8 text. |
| #4 human-readable label rejected as slug | P2 | **Still present:** `internal/localmcp/agent_runs.go:77` invokes `ValidateSessionName`, which rejects spaces at `internal/eggclient/local_sessions.go:251`. Submit `label: "weekend canary"`; it should either succeed or the tool schema must explicitly document a slug. |
| #5 isolation changes between admission/status | P2 | Current runs persist `sandbox` or `privileged` in one durable record (`internal/wingsession/runs.go:256`), replacing the old `none`/`standard` path. Compare admission/status/result with the actual platform enforcement and `egg explain`, including an inherited egg.yaml and outer-boundary mode. This canary requests outer-boundary fixtures and does not prove confinement. |
| #6 steering after a timed-out parent refused | P2 | **Local fixtures PASS:** a real egg deadline followed by steering completes the child with preserved parent/context/native result across three wing replacements. Deterministic fixtures cover ten persisted boundaries twice each. Remembered steering now retains and retries its original admission key; CLI/MCP expose the saved phase, parent and key. Real-provider and other-host acceptance remain open. |
| #7 refusal hidden behind generic stdin error | P2 | **Local fixtures PASS:** Codex native refusal/auth/rate-limit failures retain typed kinds across three wing replacements, with CLI/MCP parity and credential-sentinel exclusion. Three Claude failure fixtures preserve prior ordinary assistant output while discarding the diagnostic record. Exercise a real provider failure before promotion. |
| #8 cwd egg.yaml silently blocks launch | P2 | **REQUIRED-UNRUN:** nested `sandbox-exec` fails with `sandbox_apply: Operation not permitted`; no policy was relaxed. The minimal checkout-local reproduction requires `sandbox_denied`, the exact inherited policy path and `egg explain` guidance. Run `taskpolicy -b nice -n 15 python3 -B .scratch/notes/reproduce-policy-denial.py` from this clone in an ordinary Mac shell; this acceptance gate remains open. |
| #9 interactive provider text leaks secrets | P1 | **Open design item:** chat captures explicitly may contain secrets (`internal/egg/chat_capture.go:77`); raw PTY/transcript/lifecycle surfaces are not universally redacted. Send a fake credential through `agent_start`, `session_read`, terminal reads and stored archives. Decide whether to exclude secrets at source or change the exposure contract; deny-list filtering cannot prove complete redaction. |
| #10 Codex completion waits forever for stdout EOF | P1 | Egg semantic results follow native notify/hooks, independently of the persistent TUI; gated fake completion passes. PR #33 and the superseded reaper branch use another architecture. Verify a real completion while the provider/descendant holds output open; `agent_wait` must return the native outcome. A live interactive egg after turn completion is expected and must be stopped explicitly. |
| #11 stop silently loses queued steer follow-up | P2 | **Local fixtures PASS:** stop of an active parent durably cancels its queued child with an explicit reason across two replacements; stop of a terminal parent preserves its started child, which completes after replacement. Unit fixtures additionally cover admitted-child cancellation and timeout-terminal parents. MCP's frozen cancellation result reports terminal phase before and after replacement while the wing retains its unacknowledged stop intent. Accepted keys/parent IDs survive restart and lost-ack retry; no extra spawn/submission occurs. Real-provider and two-host acceptance remain open. |
| #12 detached descendants survive timeout | P1 | **Partial mitigation:** `internal/egg/run_processes.go` inventories identities and reports surviving descendants; it does not guarantee killing children that create new sessions or escape observation. Reproduce a setsid child and require survivor/containment metadata. Strong containment needs an explicit platform design, e.g. cgroups on Linux. |
| #13 Python resource_tracker survives stop | P1 | Same unresolved containment class as #12. Start multiprocessing, stop the native run and check every child identity; require termination or an explicit survivor report. This host denies `/bin/ps`, so descendant inventory is unavailable here. |
| #14 >128 KiB prompt triggers Linux E2BIG; prompts in argv | P1 | **Still exposed:** initial Codex TUI submission is deliberately an argv prompt (`docs/codex-run-readiness.md`, `internal/egg/run_turn.go:267`). Test a 140 KiB prompt on Forge; require either a private non-argv transport or admission-time size validation and a terminal failure event. Also audit same-UID prompt visibility. |
| #15 no tokens/rate-limit snapshot in results | P2 | **Open:** `egg.RunTurnResult` has no usage/rate-limit fields. Compare a real terminal result with its native Codex usage event; expose input/cached/output/reasoning usage and the provider quota snapshot or explicitly mark unsupported. |
| #16 blocked oaiusercontent on Codex startup | P2 | Not exercised by fake-provider/outer-boundary checks. Run a real sandboxed Codex turn on Forge; inspect the selected profile's destination policy and require blocked domains in typed events/explain, or approve an appropriate profile change. A successful model response alone does not close this report. |
| #17 boilerplate/isolation regression + missing-memory warnings | P2 | The old v0.148.7 path differs from the egg stack. #3/#5 above cover current behavior; run without `wt init` and require no warnings for optional absent memory. Recheck the released build after promotion. |
| #18 nested Codex bwrap fails inside Linux egg | P1 | The supported runtime must choose one sandbox layer. The fixture canary requests the trusted outer boundary, so it does not prove sandboxed provider tool use. In a real Linux egg, run a harmless shell tool and inspect argv/policy; require working execution without relaxing Wingthing's intended boundary. |
| #19 launch rejected below allowed workspace root | P1 | `internal/eggclient/browser_launch.go` now chooses a covering configured root; the canary launches in unique child directories and validates the returned cwd. Repeat with real local and remote workspaces, canonical symlink paths, and outside-root negative controls. |
| #20 real Codex never reaches native readiness | P1 | Startup now supplies the initial prompt and suppresses known modals (`docs/codex-run-readiness.md`). Fake empty-composer/native-turn behavior passes, but Codex 0.159.3 Mac/0.162.1 Linux and subscribed completion still need live confirmation. Run `run.sh --real-codex` outside this agent sandbox on both hosts. |
| #21 daemon handshake flakes under cmd/wt load | P2 | **Local load PASS:** 20 focused repetitions against checkout-built `wt`, the complete `cmd/wt`/`wingconnect` workload, and the finite daemon canary. The original daemon flake did not reproduce; its existing readiness contract was retained. CI/other-host acceptance remains open. |

## CI flakes and load acceptance

- **P2 — `TestRememberedPoolCanceledCallIsBounded`:** 20 focused repetitions
  and the complete two-package load pass locally. The existing receiving-handler
  cancellation barrier was already present at the pinned base. No timeout was
  raised. Repeat on the CI/other host before declaring the historical flake closed.
- **P2 — `TestBuiltWTLocalOnlyDaemonBootsWithoutTokens`:** #21 above; 20 focused
  repetitions and the complete two-package workload pass against checkout-built
  `wt`. No startup implementation change was justified by these runs.
- **Confirmed reconnect defect:** lost-ack load exposed reuse of a failed
  connection before its delayed `Done` notification. Retry now atomically
  discards that exact connection before reconnecting, without evicting a newer
  replacement. A deterministic delayed-notification fixture and generated/supplied
  run/steer-key cases pass ten repetitions. The lost-ack fixture now waits for
  cancellation, so its acknowledgement cannot accidentally win a scheduling race.

Bounded Make workload, including both focused checks and complete package load:

```sh
taskpolicy -b nice -n 15 make -j1 test-run-load
```

`test-run-load-full` passes after the reconnect repair. These are local fake
transport/checkout-binary measurements; no live CI or remote host was queried.

## Wave3 required gate acceptance

- `make -j1 test-run-recovery` and `make -j1 test-run-recovery-race`:
  **PASS in all four transition packages.** The race selection also includes the
  frozen MCP cancellation-restart regression; it supplements the full race gate.
- `make -j1 check`: **FAIL in this shell; ordinary-host rerun REQUIRED-UNRUN.**
  All 161 web tests and the full MCP/documentation packages pass after correcting
  cancellation result phase and excluding gitignored caches from the docs walk.
  Shorter private capture-fixture paths satisfy Darwin's socket length guard.
  The remaining failures are `TestDeadlineKillsProcessGroupAndReportsSurvivors`
  and `TestStopUnreachableOwnedSessionTerminatesVerifiedPID`, whose descendant
  identity checks require the denied `ps` operation. Their assertions and
  process-containment implementation were retained.
- `make -j1 gate GATE='integration static'`: **FAIL in integration; native-host
  rerun REQUIRED-UNRUN.** The preview input-lease and stable/preview persistent
  remote-session fixtures start providers that exit 71 under the native sandbox;
  the input lease then reaches its existing 20-second bound. The independent
  `sandbox-exec` preflight confirms `sandbox_apply: Operation not permitted`.
  Later preview proofs and static checks were not reached in this invocation.
- `make -j1 gate GATE=static` with `RACE_PACKAGES='./internal/wingsession
  ./internal/localmcp ./internal/wingconnect ./internal/egg ./internal/docscheck'`:
  **vet PASS; full race FAIL.** Wingconnect and docscheck pass under race;
  wingsession/egg hit the same process-inspection restrictions, and MCP reaches
  its unchanged ten-minute package bound after 600.607 seconds. No race diagnostic
  was emitted. Full race acceptance still requires an ordinary host.
- Vulnerability checks and the release-contract check are **REQUIRED-UNRUN**:
  the full race failure stops static before those steps. A populated offline
  Go advisory database and valid npm audit evidence are still required; an
  offline npm audit skip would not establish vulnerability coverage.

From this clone in an ordinary Mac shell, with the required offline inputs staged:

```sh
source .scratch/offline-env.sh
taskpolicy -b nice -n 15 make -j1 check
taskpolicy -b nice -n 15 make -j1 gate GATE='integration static' RACE_BASE=ac90fdcb0a06edb7016ebf3cb14e359162b69058
taskpolicy -b nice -n 15 python3 -B .scratch/notes/reproduce-policy-denial.py
```

These remain promotion gates. Real providers, Linux protected state, two-host
execution and the installed runtime have no new acceptance evidence.

## Parked draft PRs read through Git

The brief identifies #33 and #17 as open drafts; Git objects establish contents,
not current GitHub draft/merge/CI status. Their fetched heads were checked out
in disposable detached worktrees inside this checkout and read without `gh`.
The requested HTTPS fetch was rewritten by local Git configuration to SSH;
that violated the brief's no-SSH rule. No additional network operation followed.

- **P1 — #33, `0707393` (`refs/dogfood/pr33`, `agent-run/detach-20261008`):**
  59 changed files, 3,417 added lines against its merge base. Its
  `internal/localmcp/agent_supervisor.go` creates detached `egg supervise-run`
  processes from MCP and executes the old taskrun adapter. The historical
  report says Bryan rejected that architecture in favor of MCP-as-wing-client
  and normal eggs. This stack implements the replacement; blindly merging #33
  would reintroduce a competing execution owner. Extract only individually
  justified, still-missing fixes, with current egg regressions. Its reported
  earlier green CI/reviews do not promote it.
- **P1 — #17, `14d3475` (`refs/dogfood/pr17`, `harden/all`):** 162 changed files,
  14,089 added lines against its merge base. It adds protected descriptor walks,
  credential/grant and relay resource limits, isolation migration guards, Linux
  deny/network hardening, transcript ownership and policy pinning. Current main
  contains some independently landed pinning work, but lacks
  `internal/protectedfile` and `internal/egg/legacy_isolation.go` from that head.
  A branch-wide merge is not established safe against the weekend transports.
  Inventory remaining security fixes individually and run Mac/Linux policy,
  transcript, legacy-upgrade, grant-revocation and relay-limit acceptance. Its
  Linux protected-target extension applies to jail policy; non-jail policy still
  refuses that contract, so it is not proof that ordinary `wt claude` works there.

## Release, platforms and real clients

- **P1 — installed runtime predates the stack:** the brief reports the
  coordinator's installed `v0.148.5`. Fetched tags reach `v0.148.7`, also before
  the egg/local-only/remembered-wing/Tasks/scoped-parent stack. Build success is
  not a release, update, or installed-version acceptance. Bryan must choose
  promotion and verify the exact binary/hash on each host. This task leaves the
  installed MCP host unchanged.
- **P1 — Linux scoped Claude is unavailable:** the host preflight models provider
  writes only for macOS (`internal/localmcp/conversation_broker_policy.go:46`),
  and the Linux backend refuses every nonempty protected-write set before its
  capability probe (`internal/sandbox/linux.go:81`; regression:
  `internal/sandbox/protected_linux_test.go:13`). `sandbox.New` preserves this
  policy refusal without fallback (`internal/sandbox/sandbox.go:83`). Linux
  needs a host write model and final-policy enforcement that protect the entire
  state tree and controller executable from writes, replacement and ancestor
  renames, including paths outside HOME and overlay-prefix grants. Preserve the
  scoped parent's immutable child ceiling, host SSH denial and inaccessible
  unrestricted control socket (`internal/localmcp/wing_mailboxes.go:33`), while
  allowing its workspace mailbox. Prove native parent/child denial and recovery
  across disconnect/restart on Linux before claiming parity. The canary emits
  `unsupported-on-linux`, which the runner accepts without failing; strict
  `--require-all` still rejects unavailable coverage.
- **P2 — native client Tasks UI is unverified:** server protocol support does
  not show that a real Claude/Codex client negotiates 2025-11-25, sends optional
  augmentation or displays background tasks. Follow `docs/mcp-tasks.md` and
  observe accepted IDs, completion and cancellation in that actual client.
- **P2 — iOS is a foundation, not physical-phone acceptance:**
  `mobile/ios/README.md` lists missing pairing/sign-in, secure credential
  storage, passkey authorization, physical-phone reachability, VoiceOver,
  background delivery/push, migration and failover. Synthetic simulator tests
  and unsigned builds do not close live inference, signing or TestFlight gates.
  Validate the documented journey on an explicitly authorized real phone/home.
- **P2 — Android acceptance is absent in this checkout:** `android/` contains
  a wire-contract generator/vector, not a native app. Do not infer an APK,
  install, pairing or device journey from Go contract tests.
- **P2 — browser/relay and other providers are outside this canary:** no real
  browser, hosted/shared roost, passkey, mobile, network confinement or provider
  compatibility acceptance is claimed. `TODO.md` and `docs/testing.md` retain
  those gates, including preview authentication and cross-machine parent flow.

## Canary observations and operating limits

- **Wave3 run-recovery receipt:**
  `.scratch/dogfood-logs/20261010T204519.036036Z.jsonl` contains 17 checks:
  14 pass and 3 explicit skips, in 56,140.60 ms. The four added checks cover
  timeout/steer, queued-child stop, terminal-parent stop and typed provider
  failures, including nine wing replacements and three accepted follow-ups.
  The timeout check takes 14,413.93 ms against an actual ten-second egg deadline.
  Fake remembered SSH also recovers a steered child using its generated key.
  The binary SHA-256 is
  `298c30830a46145e7b7a7e4e302a540bc8c073ba4926902bf3d35b8cbb0c906d`.
  Scoped native Mac policy, real SSH and real Codex are the three skips.
  Private PID/endpoint/direct-child cleanup passes; descendant inventory remains
  unavailable because this host denies `ps`. Run `make -j1 test-run-canary`
  with the Mac wrapper and staged offline environment to reproduce fixture mode.
- **Fixture pass on this Mac sandbox:** local foreground and daemon startup,
  terminal I/O/stop, exact native fake result, abrupt MCP disconnect, full wing
  SIGKILL/restart, Tasks lifecycle/restart/cancel (including cancellation surviving
  a wing crash), fake remembered SSH execution, and
  cleanup pass through this checkout's real built binaries. Receipts are bounded
  JSONL in `.scratch/dogfood-logs`; each includes timing and platform. No product
  defect was established by the initial harness failures (incorrect assumptions
  about foreground daemon PID files, SIGINT exit codes and cancelled-result
  `isError` were corrected in the harness).
- **Mac receipt confirmed fail-closed protection of unsafe canary state:**
  `.scratch/dogfood-logs/20261010T162501.908821Z.jsonl` reports 9 passes, 2 skips
  and the intended `provider-writable` refusal. State was beside the fixture
  HOME, outside Seatbelt's write-deny root. The guard rejects both writable
  targets and writable regions within authoritative state
  (`internal/localmcp/conversation_broker_policy.go:108`,
  `internal/localmcp/conversation_broker_policy.go:125`), before mailbox
  registration or provider launch (`internal/localmcp/conversation_broker.go:303`).
  The canary now uses its fixture root as HOME, with state and controller beside
  workspace/TMPDIR/profile write roots. Its separate negative check deliberately
  puts state beneath the writable workspace and requires that exact refusal,
  no running egg and no mailbox registration. The follow-up Mac sandbox rerun
  passed 10 checks (including negative refusal and cleanup), with 3 explicit
  skips. A separate launch passed the corrected layout's host protection
  preflight and registered its mailbox. A reporting smoke with simulated Linux
  platform selection verified that both mailbox checks emit
  `unsupported-on-linux` and the wrapper accepts all 13 records; it used a Mac
  binary and does not establish Linux runtime acceptance. Positive native
  acceptance still needs a Mac shell permitting `sandbox-exec`.
- **Harness failure behavior verified:** an intentionally failing checkout-local
  binary produced nonzero status, explicit blocked-check records and successful
  fixture cleanup. A second wrapper invocation while locked skipped immediately.
  The web build and all 161 web tests passed; script syntax and whitespace checks
  passed. Receipts include the exact built binary's SHA-256.
- **P2 — Mac native scoped-mailbox acceptance is blocked here:** nested
  `sandbox-exec` returns `sandbox_apply: Operation not permitted`. The canary
  reports a skip before starting a parent; no sandbox policy was weakened. Run
  on the coordinator's ordinary Mac shell to exercise that check.
- **P2 — descendant inspection is blocked here:** `/bin/ps` is denied. Cleanup
  verifies private egg PID disappearance, authenticated endpoint shutdown and
  reaped direct children, and explicitly reports the unavailable descendant
  inventory. On a normal host it additionally checks for remaining processes
  naming this unique fixture. Escaped arbitrary provider descendants still need
  the #12/#13 containment design; failed cleanup retains private state.
- **P2 — live checks were not run:** real Codex and configured real SSH are
  opt-in; this task performs neither login nor remote execution. Use the exact
  coordinator commands in `scripts/dogfood/README.md`. Missing configuration is
  a skip, never a pass. The optional SSH check is read-only on the remote wing.
- **P2 — continuous weekend operation is not yet installed:** timer units and
  commands are delivered, not activated. Forge needs an already-running systemd
  user manager and a persistent repo path; Mac scheduling and wakefulness are
  the coordinator's responsibility. Run every 30–60 minutes and check timestamp
  gaps, exit status and skips. No finite canary establishes “perfect.”
- **P3 — bounded diagnostics are deliberate:** 24 receipts × 256 KiB, 4 KiB
  per error, a single lock, a 12-minute runtime bound and systemd cgroup cleanup.
  Failed cleanup can retain fixture state outside the log cap; inspect and
  resolve it before continuing unattended runs. Do not delete recent unrelated
  scratch work or signal PIDs without verifying ownership.

## Additional reported product backlog

These are remaining items in this checkout's `TODO.md`, not newly reproduced
defects. They are concrete acceptance gaps beyond the weekend MCP smoke path.

| Severity | Reported gap | Closure check |
| --- | --- | --- |
| P1 | Multi-role repository discovery hides nested egg.yaml projects | Select each role below a repository root; verify the correct workspace and policy under path ACLs. |
| P1 | Transport identity/envelope strengthening is unfinished | Prove authenticated pairing and forward secrecy; bind AEAD data and replay counters to wing, session, message type, direction and request. |
| P2 | JWT device-token storage and split-host network trust remain | Inspect stateless ES256 verification, legacy-token migration and cryptographic node authentication without changing production. |
| P2 | Browser passkey palette, project rescans and replay errors | Complete a deliberate passkey action, create a new project without restarting, and inject a corrupt replay chunk; require visible correct outcomes. |
| P2 | Sidebar listener/probe races and terminal-cache quota | Re-render/reconnect repeatedly; verify one handler/request per action and bounded total stored terminal data. |
| P2 | Egress conformance not proved on both OSes | Run allowed/denied domains, same-IP separation, raw IP, DNS, loopback, observe mode and default-merge compatibility controls. |
| P2 | Preview authentication and linked-parent acceptance remain | Follow `docs/testing.md`: real account/model access, provider startup and cold continuation, parent-to-child execution and exact human approval routing. |
| P2 | Offline, context sync, wing-to-wing coordination and richer clients are incomplete | Run a disconnected reader, compare remote instructions/memory, and exercise an explicitly scoped cross-wing workflow before claiming those features. |
| P2 | Factory/private-roost operations are an unfinished roadmap | Demonstrate owner-scoped creation/status/staging/evidence/destruction with resource bounds, and the documented private-roost real-provider journey. |
| P3 | UI/latency and compatibility/refactor cleanup remains | Compare personal/org UI, other-provider cursor behavior and round-trip latency; retire replay fallback only after supported-client acceptance. |

Paid hosted billing activation and direct hosted WebSocket routing are also
unchecked roadmap items. Neither is required for the isolated local-only canary,
and neither is authorized for production execution by this task.
