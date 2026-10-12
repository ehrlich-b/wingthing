Local branch: `codex/wt-fix-3-20261011`, cloned from the requested `580d4d9`
carrier. Product commit: `15fc057`. No push.

Only **06/card-3** is fully fixed: baseline FAIL/FAIL/FAIL → PASS/PASS/PASS.
`fleet-wzyc` was closed after the three uncached passing tests and independently
verified closed. Session details now display workspace/provider/filesystem/network
scope from private runtime policy metadata, with explicit unknowns for missing
policy evidence. The native Linux shell smoke separately verified real policy
persistence and inventory propagation. Policy display never certifies confinement.

The other 20 originally failing cards in flows 5–6 remain open. All 18 phone
cards still lack the owner pairing/device CLI and specified paired-device product;
physical iPhone acceptance is REQUIRED-UNRUN. Runs cards 1 and 5 still fail their
native subcases because the scoped host-mailbox protection contract refuses
Linux. The refusal now retains its typed reason, policy origin and path, and
supported-host next action. Native macOS acceptance is REQUIRED-UNRUN. See
[REQUIRED-UNRUN.md](REQUIRED-UNRUN.md) and
[scoped-results.json](scoped-results.json) for every card and bug ID.

All three full storyboard runs: **18 PASS / 33 FAIL**, versus baseline
**17 PASS / 34 FAIL**; zero regressions, skips or incomplete runs. The 13 remaining
failures in flows 1–4 are outside this brief. Every original storyboard/Northstar
acceptance source was hash-checked against `580d4d9`; no card test was changed.
The original `evidence/run-1..3` and `CARD-TEST-MAP.md` remain preserved as baseline.
The fresh records are [summary.json](summary.json) and `run-1..3/`, with raw Go
JSON, per-card transcripts, rendered PTY checkpoints and adjacent ANSI bytes.

`GOFLAGS=-p=4 nice -n 15 go vet ./...` passed. Focused control, dashboard,
eggclient and localmcp package tests passed. The egg package gate remains red:
`TestClaudeLifecycleRefusesHostSettingsOutsideFinalPolicy/allowed` times out and
`TestTerminateSessionBoundsMissingReaperNotification` reports unavailable
process-descendant inventory. Both reproduce unchanged on the detached base
worktree. The intermittent `TestNativeRunPTYResizeKeepsInputCancellable` failure
also reproduced on that base; the candidate's five focused retries passed.
These existing failures were retained, with no out-of-scope repair or test
relaxation. [gates.json](gates.json) links the candidate and baseline logs.

The original full-suite failures and platform-bound subcases were not converted
to passes or rewritten. The task's all-cards-green objective and the complete
focused-package gate are not achieved; this report records the accepted change
and every remaining limitation.
