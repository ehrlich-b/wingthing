# Navigator / many-machine fixes

Branch: `codex/wt-fix-2-20261011`. Baseline: `580d4d9` cloned from `/work/lanes/wt-e2e/wingthing`. Product commit: `acae211`. Local commits only; no push.

| Card | Before | After on final product source | Bug |
|---|---|---|---|
| 03/card-5 | FAIL / FAIL / FAIL | PASS / PASS / PASS | `fleet-y9t6` closed with evidence |
| 04/card-3 | FAIL / FAIL / FAIL | PASS / PASS / PASS | `fleet-vzf3` already closed as a duplicate merged into `fleet-y9t6`; closure verified |
| 03/card-2 | FAIL / FAIL / FAIL: unread vanished after reopening | Durable unread subtest PASS / PASS / PASS; whole card FAIL / FAIL / FAIL because its inherited test requires erasure on reopening | `fleet-itwd` remains open; see [TEST-DISPUTES.md](TEST-DISPUTES.md) |

Remote outages now retain stale rows and forbid attachment, takeover, rename, stop dispatch and new-session dispatch while inventory is unavailable. Local attachment remains usable. The exact-target Stop dialog remains available to inspect/confirm the intended identity; confirming it while stale returns `cannot validate` before a command is invoked. A failed refresh during a pending prompt or confirmation is checked again before dispatch. Healthy actions still use the existing exact-route/receiver checks and disconnect diagnostics.

The local viewer stores only lifecycle/read cursors in an owner-only `navigator-attention.db`. Identity includes the machine, exact session ID and complete saved route/pin (or local state directory). Reopening retains unread, viewing details or successfully returning from attach acknowledges only the captured completion, and concurrent observers merge cursors atomically. First-ever observations baseline historical completions; blocked state is never cleared by acknowledgement. Runtime profile generations prevent old captured views acknowledging replacement routes.

No existing card, Northstar, fixture, or command test was changed. Only product files under `internal/dashboard` and new focused package tests changed. Source hashes and unchanged test hashes are retained in [source.json](evidence/wt-fix-2/source.json). The original failing transcripts and three-run evidence remain under `evidence/run-1` through `evidence/run-3` and `evidence/bug-receipts`.

Validation uses `nice -n 15`, `GOFLAGS=-p=4` and clone-local build/module caches. [Commands](evidence/wt-fix-2/commands.md), [three-run JSON](evidence/wt-fix-2/cards-3x.jsonl), [card results](evidence/wt-fix-2/cards-results.json), [race package log](evidence/wt-fix-2/focused.log), [vet log](evidence/wt-fix-2/vet.log), [CLI checks](evidence/wt-fix-2/cli-focused.jsonl), and raw `.txt`/`.ansi` PTY transcripts under `evidence/wt-fix-2/transcripts` provide reproducible evidence.

Native macOS terminal/provider behavior: **REQUIRED-UNRUN** on Linux. The supported `make web/dist` placeholder served only Go embedding; no browser or physical-device acceptance is claimed.

## Final gates

Full storyboard suite: **17 PASS / 34 FAIL → 19 PASS / 32 FAIL**, all 51 cards executed. Every baseline passing card still passes; there are no regressions in other flows. The suite exits 1 because of 31 existing out-of-scope failures and the documented 03/card-2 test dispute. [Full JSON](evidence/wt-fix-2/storyboard.jsonl) and [checked comparison](evidence/wt-fix-2/storyboard-results.json).

`go vet ./...`, race tests for `internal/dashboard`, `internal/eggclient`, `internal/config`, and CLI `TestDashboard*` checks all pass.

## Remaining failures

The source-only fixes stay within flows 3–4. These card requirements remain failing; exact assertion output is retained in the full JSON.

| Card | Reason | Scope |
|---|---|---|
| 01/card-1 | Wingthing 01/card-1: Empty navigator omits connection header and first-run terms | outside flows 3–4 |
| 01/card-4 | Wingthing 01/card-4: Bare wt omits local agent detection and no-agent chooser | outside flows 3–4 |
| 01/card-5 | Wingthing 01/card-5: New-session flow omits exact installer consent | outside flows 3–4 |
| 01/card-6 | Wingthing 01/card-6: Onboarding omits provider verification and native login handoff | outside flows 3–4 |
| 01/card-7 | Wingthing 01/card-7: Onboarding omits stage-specific install/PATH/auth recovery | outside flows 3–4 |
| 02/card-0 | Wingthing 02/card-0: Linux cold scoped Claude parent is refused before readiness | outside flows 3–4 |
| 02/card-1 | Wingthing 02/card-1: Linux explicit-cwd scoped parent is refused before readiness | outside flows 3–4 |
| 02/card-2 | Wingthing 02/card-2: Linux dashboard scoped parent cannot launch on local or forge | outside flows 3–4 |
| 02/card-4 | Wingthing 02/card-4: Native launch denial omits provider cause and selected policy | outside flows 3–4 |
| 02/card-5 | Wingthing 02/card-5: Concurrent Linux scoped parents and surviving waiter cannot reach readiness | outside flows 3–4 |
| 03/card-2 | Contradictory inherited historical-reset assertion; durable-unread subtest passes | in-scope test dispute |
| 05/card-0 | Wingthing 05/card-0: Missing phone CLI prerequisite: 1 · Start on EhrlichMac | outside flows 3–4 |
| 05/card-1 | Wingthing 05/card-1: Missing phone CLI prerequisite: 2 · Scan with Bryan’s iPhoneScan to pair | outside flows 3–4 |
| 05/card-2 | Wingthing 05/card-2: Missing phone CLI prerequisite: 3 · Check both screensPair with EhrlichMac? | outside flows 3–4 |
| 05/card-3 | Wingthing 05/card-3: Missing phone CLI prerequisite: 4 · Make the pairing privatePairing with EhrlichMac | outside flows 3–4 |
| 05/card-4 | Wingthing 05/card-4: Missing phone CLI prerequisite: 5 · Your sessions on EhrlichMacEhrlichMac | outside flows 3–4 |
| 05/card-5 | Wingthing 05/card-5: Missing phone CLI prerequisite: 6 · Use it away from homeEhrlichMac | outside flows 3–4 |
| 05/card-6 | Wingthing 05/card-6: Missing phone CLI prerequisite: 7 · Manage each wing separatelyMachines | outside flows 3–4 |
| 05/card-7 | Wingthing 05/card-7: Missing phone CLI prerequisite: QR expired or already usedScan a fresh QR | outside flows 3–4 |
| 05/card-8 | Wingthing 05/card-8: Missing phone CLI prerequisite: EhrlichMac is unreachableCan’t reach EhrlichMac | outside flows 3–4 |
| 05/card-9 | Wingthing 05/card-9: Missing phone CLI prerequisite: The wing fingerprint does not match⛔ Identity mismatch | outside flows 3–4 |
| 05/card-10 | Wingthing 05/card-10: Missing phone CLI prerequisite: The emoji differCancel pairing | outside flows 3–4 |
| 05/card-11 | Wingthing 05/card-11: Missing phone CLI prerequisite: Bryan’s iPhone is lostAccess revoked | outside flows 3–4 |
| 05/card-12 | Wingthing 05/card-12: Missing phone CLI prerequisite: Attach and type from the phonepairing | outside flows 3–4 |
| 05/card-13 | Wingthing 05/card-13: Missing phone CLI prerequisite: Stop means stop this sessionStop pairing? | outside flows 3–4 |
| 05/card-14 | Wingthing 05/card-14: Missing phone CLI prerequisite: Attention notification with quiet contentNotifications | outside flows 3–4 |
| 05/card-15 | Wingthing 05/card-15: Missing phone CLI prerequisite: Same Wi-Fi and tailnet both stay freeEhrlichMac | outside flows 3–4 |
| 05/card-16 | Wingthing 05/card-16: Missing phone CLI prerequisite: Camera or local-network permission deniedAllow local access | outside flows 3–4 |
| 05/card-17 | Wingthing 05/card-17: Missing phone CLI prerequisite: Pairing succeeds but the final reply dropsFinishing pairing | outside flows 3–4 |
| 06/card-1 | Wingthing 06/card-1: Linux scoped parent cannot obtain durable forge run receipts | outside flows 3–4 |
| 06/card-3 | Wingthing 06/card-3: Session details omit effective workspace/provider/network authority | outside flows 3–4 |
| 06/card-5 | Wingthing 06/card-5: Linux provider-policy denial loses phase-specific policy evidence | outside flows 3–4 |
