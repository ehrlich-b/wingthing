# Wave 2 integration and fixes

Combined wave-1 baseline: **24 PASS / 27 FAIL**. Final combined result:
**25 PASS / 26 FAIL** in each of three complete storyboard runs. All 51 cards
have verdicts; no card or previously passing subtest regressed. No card flakiness.
The retained failures mean the storyboard command honestly exits 1.

`summary.json` and `verify.py` check the complete counts, three identical verdicts
per card, all previously passing subtests, gate exits and unchanged acceptance
sources. Original storyboard and Northstar test files are byte-identical to
`580d4d9` (15 files); no card was weakened or rewritten.

## Product changes and scope

1. Cloned `/work/lanes/wt-e2e/wingthing`, created
   `codex/wt-fixes-integrated-20261011`, and fetched both wave-1 lane branches.
   Real merge commits `1e4a8059` and `a63c1ba2` retain the wave-1 histories.
   Conflicts preserve first-run onboarding, durable unread cursors and offline
   action checks together. Lane 3 had no `final.md` when this lane started and was
   excluded under the brief's startup condition.
2. Attention/offline: 03/card-5 and 04/card-3 pass three times; 03/card-2's durable
   reopen subtest also passes three times. The complete attention card remains
   failing because its inherited historical-reset assertion contradicts durable
   unread behavior. The product retains unread state. See `TEST-DISPUTES.md`.
3. Run policy: **06/card-3 FAIL → PASS**, three times. The egg privately captures
   prepared filesystem/network authority for its exact session. The navigator
   displays that owning-machine snapshot, explicitly labels missing/foreign or
   incompatible policy unknown, and explains privileged OS-user authority.
   It never treats policy presentation as native confinement evidence. Protected
   launch refusals retain typed phase and selected-policy provenance.
4. Phone prerequisites: reused existing pairing implementation commits
   `e3df539f` and `145bba47` from `/work/lanes/wt-lead/code`, locally ported as
   `087f37c0` and `25fa6b7d`. This brings the existing pinned BoringSSL SPAKE2
   integration, main-wing invitations, encrypted device control, LAN/tailnet
   endpoints, registry, device listing and live revocation into this clone.
   Registry rename is directory-synced before success; cancellation discards
   both approval bits and returns an explicit cancelled result. Revoke errors
   explain unpaired devices and ambiguous names. Terminal copy identifies the
   actual machine and preserves free direct tailnet access.

The supplementary real-PTY CLI journey passes three times. It starts an actual
isolated main wing, acts as a Go PAKE peer, and checks stable wing identity,
five-minute expiry, terminal cancellation, fresh secrets/IDs, two independent
approvals, one private persisted grant, original-response recovery, listing,
revocation and no provider launch/other-wing mutation. Missing-main-wing refusal
and real QR bitmap/layout checks also pass three times. These are CLI/wing
prerequisites, not physical-iPhone acceptance.

## Gates and evidence

- Full storyboard: `final-storyboard-3x.jsonl` / `.exit`; no regressions, 25/26
  each time. Baseline: `before.jsonl` / `.exit`, 24/27.
- Vet: `final-vet.log` / `.exit`, **PASS**.
- Race: `final-race.log` / `.exit`, **PASS** for dashboard, eggclient, config,
  phonepair, pake, relay and wing.
- Focused policy/protocol tests: `final-focused-3x.log` / `.exit`, **PASS**.
- CLI prerequisites and 06/card-3: `phone-policy-cli-final-3x.jsonl` / `.exit`,
  **PASS**. Earlier attention/offline repetitions: `attention-policy-3x.jsonl`.
- Built the actual web assets with `npm ci` and `npm run build` for the relay's
  static-asset race tests. This is build evidence, not browser acceptance.
- All Go builds/tests/vet were niced at 15 with `GOFLAGS=-p=4`.
- `acceptance-source.json` binds original-test hashes to product head
  `e9a7609ca3caaffd4a748d7eab2dcd3a008ced51`. `summary.json` records the tested
  binary SHA-256. Retained PTY transcripts are under `transcripts/`.

## Remaining 26 complete-card failures

| Area | Cards | Count | Reason |
| --- | --- | ---: | --- |
| Attention | 03/card-2 | 1 | Contradictory historical-reset and durable-reopen assertions; preserve durable unread. |
| Protected parent/start work | 02/card-0,1,2,4,5 | 5 | Native macOS provider-write protection is REQUIRED-UNRUN on Linux; admission stays refused. |
| Protected run admission/failure | 06/card-1,5 | 2 | Same protected native launch dependency; no policy broadening. |
| Phone | 05/card-0 through 05/card-17 | 18 | Original fixtures omit the main wing, require interactive pair to exit without a peer, and unconditionally fail at the physical-iPhone gate. CLI/wing prerequisites pass separately; complete physical cards remain REQUIRED-UNRUN. |

`fleet-wzyc` was already closed by another lane; its complete card independently
passes three times here. `fleet-itwd` remains open. No failing/native/physical
card bead was closed by this lane. Captured status receipts are alongside this
report. No push or deployment was performed.
