# Storyboard test dispute rulings

## Scope and provenance

Read every `TEST-DISPUTES.md` beneath `/work/lanes/wt-fix-*/wingthing`, including ignored directories. Found two files, in `wt-fix-2` and `wt-fix-wave2`; their contents are byte-identical and describe one unique dispute, **03/card-2** (`fleet-itwd`). No other dispute files were present at adjudication.

This checkout was cloned from `/work/lanes/wt-e2e/wingthing` at `580d4d9f8241fd37f56011bced9535872128eeda`, then switched to `codex/wt-test-disputes-20261011`. Source hashes are recorded in `evidence/test-disputes/source-hashes.json`. The current storyboard card and PRD were read directly, rather than relying only on the dispute's description or frozen test evidence.

## 03/card-2 — TEST expectation is wrong

Binding storyboard: `/work/lanes/wt-app/storyboard/03-navigator.html#3`, article `card-2`, “Blocked now; unread next”:

> Blocked and failed attention work today. Unread is a planned per-viewer marker, distinct from an agent awaiting input.

> Unread must be based on new output and a durable read cursor; it must never imply provider completion or authorization.

PRD FR-05 independently requires:

> Unread uses a per-viewer durable read cursor, distinct from blocked/failed lifecycle attention.

`TestStoryboardNavigatorCard2` runs both `TestNorthstarPTYAttention/symlinked-TMPDIR-native-hooks` and `durable-unread-cursor`. In the former, the viewer acknowledges one remote completion by attaching/detaching, then observes a **new** remote completion, leaves it unread through an offline/reconnect cycle, and quits. Reopening uses the same fixture HOME, local state directory, saved forge profile and session ID. No action acknowledges that new completion. Nevertheless, the old final assertion demands `I forge-agent` and `U:0`. The local durability subtest correctly demands preserved unread state on the same viewer's reopen. Transport cannot justify opposite cursor semantics.

Minimal correction: change that single remote reopen checkpoint to require **`I* forge-agent`, `U:1`**, and rename its description to “same viewer reopen / durable remote unread / known blocked.” Add the justifying storyboard quote beside the assertion. Preserve `B local-agent`, `bell:off`, all earlier acknowledgement/route/privacy/bell assertions, and the existing local durability subtest. The patch is `evidence/test-disputes/correction.patch`.

The PRODUCT expectation of durable unread state is correct. Do not reset persisted cursors on navigator restart or make remote unread ephemeral. First-ever viewer baselining remains distinct and is covered by `TestAttentionNativeTransitionsAndExactAcknowledgement`; restarting a previously observed viewer is not a first-ever viewer.

## Validation boundary

The test branch deliberately contains no product changes. Its inherited e2e product keeps attention records only within one dashboard instance (`internal/dashboard/attention.go`) and therefore cannot satisfy either durable reopen obligation. The existing product implementation in `wt-fix-2` commit `acae211ed8e4f0a173428939b0444704172749d5` supplies durable route-scoped cursors and acknowledgements. Validate the corrected full card against that implementation in a separate clone, without modifying the fix lanes or importing product changes into this test branch.

Observed outcomes (`evidence/test-disputes/summary.json`, complete Go JSON logs and copied PTY transcripts):

- E2e base plus corrected test: full card FAIL, with both remote and local durable reopen checkpoints failing. This is the inherited product's missing persistence, not grounds to weaken the test.
- Separate clone of `wt-fix-2` at `580cb3a9b502f1335456d62dfe5b423e0f5fd763` (includes `acae211`) plus the identical corrected test: full card **PASS three times**, including both subtests each time, with no skipped tests (`go test -json -count=3 -run '^TestStoryboardNavigatorCard2$' -timeout 3m ./cmd/wt`).

Independent read-only `codex exec -m gpt-6.1-sol` review is captured in `DISPUTES-REVIEW.md`. No bug was closed and no whole-suite or native-provider acceptance is claimed.
