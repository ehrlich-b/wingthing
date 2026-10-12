# Flow 1–2 fix evidence

Product commit: `6a08b45`, branch `codex/wt-fix-1-20261011`, cloned from
`/work/lanes/wt-e2e/wingthing` at `580d4d9`. No push or publication.

All three complete 51-card storyboard runs recorded **22 PASS / 29 FAIL**,
compared with **17 PASS / 34 FAIL** in the retained baseline. All previously
passing cards still pass. No parent or subtest outcome changed across the
three new runs, and no test was skipped or omitted. See [summary.json](summary.json)
for the complete comparison and [verify.py](verify.py) for the exact collection
procedure. The original four card-test file hashes remain unchanged.

| Card | Before | After, runs 1/2/3 | Evidence test |
|---|---|---|---|
| 01/card-1 | FAIL | PASS / PASS / PASS | TestStoryboardInstallCard1 |
| 01/card-4 | FAIL | PASS / PASS / PASS | TestStoryboardInstallCard4 |
| 01/card-5 | FAIL | PASS / PASS / PASS | TestStoryboardInstallCard5 |
| 01/card-6 | FAIL | PASS / PASS / PASS | TestStoryboardInstallCard6 |
| 01/card-7 | FAIL | PASS / PASS / PASS | TestStoryboardInstallCard7 |
| 02/card-0 | FAIL | FAIL / FAIL / FAIL; REQUIRED-UNRUN | TestStoryboardStartCard0 |
| 02/card-1 | FAIL | FAIL / FAIL / FAIL; REQUIRED-UNRUN | TestStoryboardStartCard1 |
| 02/card-2 | FAIL | FAIL / FAIL / FAIL; REQUIRED-UNRUN | TestStoryboardStartCard2 |
| 02/card-4 | FAIL | FAIL / FAIL / FAIL; REQUIRED-UNRUN | TestStoryboardStartCard4 |
| 02/card-5 | FAIL | FAIL / FAIL / FAIL; REQUIRED-UNRUN | TestStoryboardStartCard5 |

Each numbered run contains `cards/<test>.txt`, its complete Go JSON log, raw
ANSI PTY transcripts, rendered checkpoints and fixture receipts. Initial smoke
check receipts may also occur in run-1's copied journey directory; the complete
run's JSON log identifies its exact checkpoint filenames. The baseline
`evidence/run-1`, `run-2`, `run-3` and their source snapshots were retained.
The two current storyboard source pages match those frozen snapshots exactly;
see [source-verification.json](source-verification.json).

The new navigator offers local connection/wing/roost guidance, PATH-only
detection, an agent chooser and shell skip, exact installer consent, separate
version/login consent, cwd review and stage-specific recovery. Existing wing
and remote creation routes remain unchanged. Additional dashboard unit tests
prove detection/cancellation execute no provider, version success does not
invoke login, failed download transport does not become pipeline success, retry
requires a fresh consent view, and Skip/remote/existing-session creation keeps
the ordinary route.

`GOFLAGS=-p=4` and nice level 15 were used for the Go gates. `go vet ./...`,
the full `internal/dashboard` package tests, and `cmd/wt` tests matching
`^Test(Claude|Dashboard)` pass. Exact commands and outputs are in
[gates.json](gates.json). These are focused CLI tests, not a claim that every
legacy native CLI test can run on Linux. The final binary hash is recorded in
[build.json](build.json).

The five remaining scoped failures require a protected Claude parent mailbox,
which the product currently supports only on macOS. Linux still refuses the
unsupported protection model. [REQUIRED-UNRUN.md](REQUIRED-UNRUN.md) names each
gate and the first unmet requirement. No sandbox refusal or card test was
weakened. Real vendor installation/authentication and native macOS readiness
remain unrun. The other 24 failures are outside the authorized fix scope:
two navigator attention/offline cards, one offline-machine card, eighteen phone
prerequisite/device cards, and three run admission/policy cards.

The board had consolidated flow-1 cards 4/5/6/7 as MERGE-closed duplicates of
`fleet-kxez`; this lane retained that history and closed the matching live owner
only after all five named card tests passed three times. The reason identifies
the supplied BRIEF scope and does not claim real native/vendor acceptance or
the later expanded marketing obligations. [bead-close.json](bead-close.json)
and [beads-after.json](beads-after.json) record the closure and resulting state.
Native failure owners remain open.
