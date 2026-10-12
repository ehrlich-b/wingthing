# 03/card-2: contradictory reopen assertions

The unchanged `TestStoryboardNavigatorCard2` invokes `TestNorthstarPTYAttention`, whose `symlinked-TMPDIR-native-hooks` subtest closes the navigator with an unacknowledged remote completion (`I* forge-agent`, `U:1`) and then reopens the same local HOME/state directory. At `cmd/wt/northstar_attention_pty_test.go:206`, it requires `I forge-agent` and `U:0` (“fresh instance / historical completion baseline”). This explicitly erases the same viewer's unread state.

The storyboard `/work/lanes/wt-app/storyboard/03-navigator.html#3` says: “Unread must be based on new output and a durable read cursor”. The card's own `durable-unread-cursor` subtest (`cmd/wt/storyboard_pty_test.go:466`) requires the opposite: reopening the same viewer must preserve `I* unread-parent` and `U:1`. There is no intervening acknowledgement in either scenario. The exact machine/route must not change this requirement.

The product now retains unacknowledged cursors on both local and remote machines, and persists exact-view acknowledgements. Thus the durable subtest passes while the historical-reset assertion fails with `I* forge-agent` / `U:1`. Satisfying both by treating remote unread as ephemeral would violate the storyboard and machine-qualified per-viewer semantics.

No acceptance test was changed. Proposed reviewer correction: the historical-reset checkpoint should expect the preserved remote unread marker and count; first-ever viewer baselining remains separately covered. Bug `fleet-itwd` stays open until the contradictory test is resolved and the complete card passes three times.

Evidence: `evidence/wt-fix-2/cards-3x.jsonl` includes three passes of the durable reopen subtest and three failures of the historical-reset assertion. The full suite is retained in `evidence/wt-fix-2/storyboard.jsonl`.

## Wave 2: 05/card-0 through 05/card-17 cannot certify CLI prerequisites

The unchanged `storyboardPhone` helper creates one standalone egg, but never starts the main wing or its authenticated pairing endpoint. The storyboard's first phone card and `docs/pairing/PROTOCOL.md` require pairing with the same running main wing. `wt pair` must refuse missing main-wing control instead of inventing an invitation or silently starting another identity. `wt devices` likewise reads that wing's device registry through its authenticated endpoint. The original fixtures therefore receive the correct missing-main-wing error after pairing is integrated.

The helper also invokes `wt pair` through `storyboardCommand`, which requires the process to exit within 20 seconds. The storyboard requires a live five-minute invitation, waiting for a scan and independent terminal confirmation. No peer scans, confirms or cancels in this helper. A real `wt pair` must remain open, and terminal exit must burn its invitation. Exiting early with a "Waiting for scan" screen would be false product behavior.

Finally, the helper unconditionally calls `t.Fatal("REQUIRED-UNRUN: ... physical iPhone evidence required")`, even if all prerequisite screen assertions succeed. None of these 18 complete cards can become PASS on Linux by a legitimate CLI/wing change. No original card or Northstar test was edited.

Supplemental `TestPhoneCLIPrerequisitesOnMainWing` exercises the built CLI in kernel PTYs against an actual isolated main wing and a Go peer using the vendored native SPAKE2 API. It verifies invitation expiry, a stable wing identity, terminal cancellation, fresh IDs/secrets, two-sided approval, private persisted credentials, duplicate-confirmation recovery, listing and revocation. `TestPhoneCLIRequiresRunningMainWing` verifies all three commands refuse a missing wing without admitting work. Results are in `evidence/wave2/phone-policy-cli-final-3x.jsonl`; these are CLI/wing evidence, never physical-iPhone acceptance.

Proposed reviewer correction: split executable CLI/wing prerequisites from the physical-device cards; supply a running isolated main wing and a peer for the former, use PTY screen waits and explicit cancellation for interactive `wt pair`, and retain the physical-device gate separately. All phone card beads remain open unless a complete unchanged card passes three times.
