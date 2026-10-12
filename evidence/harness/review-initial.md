**Assertion integrity is confirmed. One harness reliability concern remains.**

Base reviewed: `71ef5dc50269d2e60f108d967a9162f59956d7fc` (`71ef5dc5`).

Files examined:

- [storyboard_pty_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_pty_test.go)
- [phone_storyboard_e2e_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/phone_storyboard_e2e_test.go)
- New, untracked [storyboard_phone_harness_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_harness_test.go) and [storyboard_phone_nocgo_test.go](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_nocgo_test.go)
- [HARNESS-FIX.md](/work/lanes/wt-phone-harness/wingthing/HARNESS-FIX.md)
- Supporting Northstar fixtures, CLI pairing/startup, daemon readiness, phone protocol/server, Go testpeer, and supplied harness evidence.

**No existing storyboard/card assertion was weakened or removed.** Independent byte comparisons established that:

- Removing the added startup call and restoring the single command-helper call makes `storyboard_pty_test.go` byte-identical to base.
- Removing the added helper/test block makes `phone_storyboard_e2e_test.go` byte-identical to base.
- Every existing expected literal, fatal check, command-selection array, test name, timeout and skip condition is consequently byte-identical.
- All 1,841 other existing storyboard/evidence paths compared, including the three other storyboard card test files, are byte-identical.

The execution sequence intentionally changes: the driver uses real `pair --show-link`, supplies approvals, and adds preparatory pairing/revocation. Existing timeout constants survive, but total elapsed-time behavior is not identical.

The new fixture starts the actual CLI main wing with disposable `HOME`, state and workspace; cleanup is registered before startup. The Go testpeer runs in `--client` mode against the real wing, performs PAKE and encrypted requests, and obtains its credential through the protocol. Phone confirmation must leave the grant pending before terminal Enter completes pairing. Inventory, attachment/read, detach and PID checks add requirements without substituting generated output for existing assertions.

Card 6 lists a genuinely paired device. Card 11 first successfully revokes that device and checks reconnect rejection, then repeats the original revoke command. Its genuine second-call error satisfies the unchanged `"device"` / `"not paired"` prerequisite. The existing journey’s live-output revocation checks also remain intact. The non-CGO helper retains unsupported-pairing behavior without adding skips.

**Concern: pairing readiness can race startup.** [The new startup helper](/work/lanes/wt-phone-harness/wingthing/cmd/wt/storyboard_phone_harness_test.go:21) accepts generic local-control readiness. However, [the wing publishes that status](/work/lanes/wt-phone-harness/wingthing/internal/wing/wing.go:1142) before calling `phonepair.Listen`; [endpoint discovery](/work/lanes/wt-phone-harness/wingthing/internal/phonepair/endpoints.go:32) can then take two seconds before `phone-control.json` is written. The immediate pairing command can therefore exit as “main wing is not ready,” producing an early fixture failure. This is a static reliability finding; the supplied runs did not exhibit it. It does not weaken assertions.

The supplied completed phone-flow log records three backend prerequisite/journey **PASS** runs and 54 raw card **FAIL** outcomes: all 18 cards across three runs retained the unconditional `REQUIRED-UNRUN` fatal, with no prerequisite failure diagnostics found. Backend PASS does not establish physical-card acceptance.

This review ran no tests, builds or daemons and edited no files. Evidence results were read, not independently reproduced. At final inspection the full after-gate log remained incomplete and the linked `RESULTS.md` was absent. Physical iPhone UI, camera, Keychain/Secure Enclave, cellular and notification obligations remain unverified.