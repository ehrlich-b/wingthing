# What keeps Wingthing from perfect

Snapshot: 2026-10-10, weekend stack at `92b1183`, branch
`codex/dogfood-weekend-20261010`. P1 = security, lost work or a blocked primary
workflow; P2 = broken contract or missing acceptance; P3 = polish/operability.
“Fixture verified” means an isolated built-binary check, not real-provider,
production, mobile, sandbox-confinement or weekend-long acceptance.

The historical reports below were read in full from
`~/repos/claude/state/wingthing-dogfood-bugs.md` (#1–#21). Some defects were fixed
in superseded branches; that does not imply the fix exists in this stack or the
installed binary. Every row preserves a concrete remaining check.

## Historical dogfood reports

| Bug | Severity | Status in this checkout and remaining closure check |
| --- | --- | --- |
| #1 MCP host exit kills workers | P1 | Egg-backed execution survives a killed stdio client and full wing replacement in the canary. Repeat a long real Codex/Claude run, terminate the parent host, and recover the same run/session/wing IDs and partial transcript from a fresh authorized client. Old installed taskrun builds remain exposed. |
| #2 OPENAI_API_KEY in egg argv | P1 | `internal/eggclient/egg.go:83` transports the environment through a private `.egg.env` file, not argv. Run a sentinel credential regression and inspect wrapper/provider argv on Mac and Linux; direct user-supplied `--env SECRET=...` still places that user's argument in argv. |
| #3 injected memory/schedule boilerplate | P2 | `internal/wingsession/runs.go:256` retains the original prompt; exact fake native output passes without injected suffixes. Inspect one real provider's received prompt and confirm unchanged UTF-8 text. |
| #4 human-readable label rejected as slug | P2 | **Still present:** `internal/localmcp/agent_runs.go:77` invokes `ValidateSessionName`, which rejects spaces at `internal/eggclient/local_sessions.go:251`. Submit `label: "weekend canary"`; it should either succeed or the tool schema must explicitly document a slug. |
| #5 isolation changes between admission/status | P2 | Current runs persist `sandbox` or `privileged` in one durable record (`internal/wingsession/runs.go:256`), replacing the old `none`/`standard` path. Compare admission/status/result with the actual platform enforcement and `egg explain`, including an inherited egg.yaml and outer-boundary mode. This canary requests outer-boundary fixtures and does not prove confinement. |
| #6 steering after a timed-out parent refused | P2 | The report says fixed in old main via #18. Current `toolAgentSteer` creates a child linked to the saved parent. Recheck a timeout → `agent_steer` → new completed native result and preserved parent link against eggs. |
| #7 refusal hidden behind generic stdin error | P2 | Egg results carry structured `failure_kind` (`internal/egg/run_turn.go:34`); raw provider-error text must not be used as the fix. Exercise refusal/auth/rate-limit fixtures and a real provider failure; require a useful structured kind plus safe partial results, without credential text. |
| #8 cwd egg.yaml silently blocks launch | P2 | The launch policy still applies inherited egg.yaml. Reproduce a denied provider executable and require the diagnostic to identify the policy path/rule and point to `egg explain`; old-branch fix claims are not closure evidence for the current adapter. |
| #9 interactive provider text leaks secrets | P1 | **Open design item:** chat captures explicitly may contain secrets (`internal/egg/chat_capture.go:77`); raw PTY/transcript/lifecycle surfaces are not universally redacted. Send a fake credential through `agent_start`, `session_read`, terminal reads and stored archives. Decide whether to exclude secrets at source or change the exposure contract; deny-list filtering cannot prove complete redaction. |
| #10 Codex completion waits forever for stdout EOF | P1 | Egg semantic results follow native notify/hooks, independently of the persistent TUI; gated fake completion passes. PR #33 and the superseded reaper branch use another architecture. Verify a real completion while the provider/descendant holds output open; `agent_wait` must return the native outcome. A live interactive egg after turn completion is expected and must be stopped explicitly. |
| #11 stop silently loses queued steer follow-up | P2 | Current stop code preserves/reports follow-up state (`internal/wingsession/runs.go:691`). Recheck steer + stop in both parent-terminal and parent-active cases, including restart; every accepted child needs a durable status or an explicit cancellation reason. Do not assume a superseded branch's active-steer contract. |
| #12 detached descendants survive timeout | P1 | **Partial mitigation:** `internal/egg/run_processes.go` inventories identities and reports surviving descendants; it does not guarantee killing children that create new sessions or escape observation. Reproduce a setsid child and require survivor/containment metadata. Strong containment needs an explicit platform design, e.g. cgroups on Linux. |
| #13 Python resource_tracker survives stop | P1 | Same unresolved containment class as #12. Start multiprocessing, stop the native run and check every child identity; require termination or an explicit survivor report. This host denies `/bin/ps`, so descendant inventory is unavailable here. |
| #14 >128 KiB prompt triggers Linux E2BIG; prompts in argv | P1 | **Still exposed:** initial Codex TUI submission is deliberately an argv prompt (`docs/codex-run-readiness.md`, `internal/egg/run_turn.go:267`). Test a 140 KiB prompt on Forge; require either a private non-argv transport or admission-time size validation and a terminal failure event. Also audit same-UID prompt visibility. |
| #15 no tokens/rate-limit snapshot in results | P2 | **Open:** `egg.RunTurnResult` has no usage/rate-limit fields. Compare a real terminal result with its native Codex usage event; expose input/cached/output/reasoning usage and the provider quota snapshot or explicitly mark unsupported. |
| #16 blocked oaiusercontent on Codex startup | P2 | Not exercised by fake-provider/outer-boundary checks. Run a real sandboxed Codex turn on Forge; inspect the selected profile's destination policy and require blocked domains in typed events/explain, or approve an appropriate profile change. A successful model response alone does not close this report. |
| #17 boilerplate/isolation regression + missing-memory warnings | P2 | The old v0.148.7 path differs from the egg stack. #3/#5 above cover current behavior; run without `wt init` and require no warnings for optional absent memory. Recheck the released build after promotion. |
| #18 nested Codex bwrap fails inside Linux egg | P1 | The supported runtime must choose one sandbox layer. The fixture canary requests the trusted outer boundary, so it does not prove sandboxed provider tool use. In a real Linux egg, run a harmless shell tool and inspect argv/policy; require working execution without relaxing Wingthing's intended boundary. |
| #19 launch rejected below allowed workspace root | P1 | `internal/eggclient/browser_launch.go` now chooses a covering configured root; the canary launches in unique child directories and validates the returned cwd. Repeat with real local and remote workspaces, canonical symlink paths, and outside-root negative controls. |
| #20 real Codex never reaches native readiness | P1 | Startup now supplies the initial prompt and suppresses known modals (`docs/codex-run-readiness.md`). Fake empty-composer/native-turn behavior passes, but Codex 0.159.3 Mac/0.162.1 Linux and subscribed completion still need live confirmation. Run `run.sh --real-codex` outside this agent sandbox on both hosts. |
| #21 daemon handshake flakes under cmd/wt load | P2 | Standalone default daemon startup now has a canary check, including control calls and no relay token creation. The report's full-suite broken-pipe failure remains a load gate; passing alone is insufficient. Reproduce under full `cmd/wt` load and make the handshake barrier independent of timing. |

## CI flakes and load acceptance

- **P2 — `TestRememberedPoolCanceledCallIsBounded`:** the brief reports a weekend
  CI flake. The current test waits on a receiving-handler barrier and then
  cancels (`internal/wingconnect/pool_test.go:404`). No live CI metadata was
  queried. Repeated focused and full-package runs on both hosts must terminate
  within their test bound; capture the blocked goroutine if it recurs.
- **P2 — `TestBuiltWTLocalOnlyDaemonBootsWithoutTokens`:** #21 above. Run with a
  checkout-built `WT_TEST_BINARY`, then run the full `cmd/wt` package under the
  same concurrent package load as CI. A successful single canary is a smoke
  gate, not proof that the flake is gone.

Coordinator commands, after building this checkout:

```sh
WT_TEST_BINARY="$PWD/wt" nice -n 15 go test -p 2 ./cmd/wt -run '^TestBuiltWTLocalOnlyDaemonBootsWithoutTokens$' -count=10 -timeout=5m
nice -n 15 go test -p 2 ./internal/wingconnect -run '^TestRememberedPoolCanceledCallIsBounded$' -count=10 -timeout=5m
WT_TEST_BINARY="$PWD/wt" nice -n 15 go test -p 2 ./cmd/wt ./internal/wingconnect -count=1 -timeout=15m
```

This task ran each focused test with `-count=3`: both passed. Full-package/CI
load reproduction was not run, so neither flake is marked closed.

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
- **P1 — Linux scoped Claude is unavailable:** the current Linux non-jail
  sandbox refuses nonempty protected-write targets
  (`internal/sandbox/protected_linux_test.go`). The canary emits an explicit
  skip; `--require-all` fails it. Implement and independently prove the same
  parent ceiling and protected state/socket contract before claiming two-host
  weekend parity.
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

- **Fixture pass on this Mac sandbox:** local foreground and daemon startup,
  terminal I/O/stop, exact native fake result, abrupt MCP disconnect, full wing
  SIGKILL/restart, Tasks lifecycle/restart/cancel (including cancellation surviving
  a wing crash), fake remembered SSH execution, and
  cleanup pass through this checkout's real built binaries. Receipts are bounded
  JSONL in `.scratch/dogfood-logs`; each includes timing and platform. No product
  defect was established by the initial harness failures (incorrect assumptions
  about foreground daemon PID files, SIGINT exit codes and cancelled-result
  `isError` were corrected in the harness).
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
