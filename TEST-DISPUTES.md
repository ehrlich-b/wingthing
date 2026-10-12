# 03/card-2: contradictory reopen assertions

The unchanged `TestStoryboardNavigatorCard2` invokes `TestNorthstarPTYAttention`, whose `symlinked-TMPDIR-native-hooks` subtest closes the navigator with an unacknowledged remote completion (`I* forge-agent`, `U:1`) and then reopens the same local HOME/state directory. At `cmd/wt/northstar_attention_pty_test.go:206`, it requires `I forge-agent` and `U:0` (“fresh instance / historical completion baseline”). This explicitly erases the same viewer's unread state.

The storyboard `/work/lanes/wt-app/storyboard/03-navigator.html#3` says: “Unread must be based on new output and a durable read cursor”. The card's own `durable-unread-cursor` subtest (`cmd/wt/storyboard_pty_test.go:466`) requires the opposite: reopening the same viewer must preserve `I* unread-parent` and `U:1`. There is no intervening acknowledgement in either scenario. The exact machine/route must not change this requirement.

The product now retains unacknowledged cursors on both local and remote machines, and persists exact-view acknowledgements. Thus the durable subtest passes while the historical-reset assertion fails with `I* forge-agent` / `U:1`. Satisfying both by treating remote unread as ephemeral would violate the storyboard and machine-qualified per-viewer semantics.

No acceptance test was changed. Proposed reviewer correction: the historical-reset checkpoint should expect the preserved remote unread marker and count; first-ever viewer baselining remains separately covered. Bug `fleet-itwd` stays open until the contradictory test is resolved and the complete card passes three times.

Evidence: `evidence/wt-fix-2/cards-3x.jsonl` includes three passes of the durable reopen subtest and three failures of the historical-reset assertion. The full suite is retained in `evidence/wt-fix-2/storyboard.jsonl`.
