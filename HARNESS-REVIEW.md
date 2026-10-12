**Assertion integrity is confirmed. The corrected source resolves the previously identified phone-readiness race. No substantive assertion-integrity or remaining startup-ordering defect was found.**

Base reviewed: `71ef5dc50269d2e60f108d967a9162f59956d7fc` (`71ef5dc5`).

Files examined:

- [storyboard_pty_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_pty_test.go)
- [phone_storyboard_e2e_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/phone_storyboard_e2e_test.go)
- New untracked [storyboard_phone_harness_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_harness_test.go) and [storyboard_phone_nocgo_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_nocgo_test.go)
- [HARNESS-FIX.md](/work/lanes/wt-phone-harness/wingthing/HARNESS-FIX.md), the retained initial review, and harness evidence.
- Supporting Northstar fixtures, wing startup, pairing CLI, phone HTTP server/manager, endpoint discovery, Go testpeer, and pairing protocol.

**No existing storyboard/card assertion was weakened or removed.** Independent byte comparisons established that reversing the two fixture edits restores `storyboard_pty_test.go` exactly to base. Removing the added helper/test block and the single `storyboardPhoneReady(t, f)` setup statement restores `phone_storyboard_e2e_test.go` exactly to base. Consequently, every existing expected literal, fatal check, command-selection array, test name, timeout value and skip condition is byte-identical.

All 1,890 other existing storyboard or evidence files compared were unchanged, including `storyboardScopedNew_pty_test.go`, `storyboardNativeMouse_pty_test.go`, `storyboardLaunchFailure_pty_test.go`, the storyboard specification and runner files.

The readiness correction is sound on static inspection. [storyboardPhoneReady](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_harness_test.go:42) waits up to ten seconds for the specific phone server to accept an authenticated loopback `devices` request and return HTTP 200 with decodable inventory. That operation is read-only. It covers both control-file publication and HTTP-serving readiness after endpoint discovery. Both startup paths invoke it before pairing. The one-second delayed Tailscale fixture exercises the slow discovery path without changing an original timeout or assertion.

The fixture starts the actual main-wing CLI with disposable HOME, state and workspace, with cleanup registered before startup. The Go testpeer uses `--client` against that wing’s real encrypted protocol. Phone confirmation must leave enrollment pending without a device grant; terminal Enter then completes pairing. Inventory, attachment/read, detach and PID checks add requirements. Existing assertions receive actual CLI output.

Card 6 lists a genuinely paired device. Card 11 first pairs and successfully revokes it, verifies reconnect rejection, then repeats the original revoke command. Its real second-call error satisfies the unchanged `"device"` / `"not paired"` prerequisite. The existing journey’s live-output revocation checks remain intact. Non-CGO handling retains explicit unsupported-pairing behavior without adding skips.

The execution sequence changes through additional setup and `pair --show-link`; total elapsed-time behavior therefore changes despite unchanged original timeout values.

**Raw phone-card FAIL remains mandatory:** all 18 cards retain the unconditional `REQUIRED-UNRUN` fatal. Backend PASS cannot establish physical-card acceptance. The retained initial three-run log records backend prerequisite/journey PASS and 54 card FAIL outcomes. Final logs inspected also recorded backend PASS and retained-fatal card FAIL events, but lacked package completion; final gate success is therefore not established by this review. The linked `evidence/harness/RESULTS.md` was still absent.

This was a read-only static review and evidence inspection. No files were edited, and no tests, builds or daemons were run. Supplied runtime results were not independently reproduced. Physical iPhone UI, camera, Keychain/Secure Enclave, cellular and notification obligations remain unverified.