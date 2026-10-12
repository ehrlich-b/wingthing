All 100 storyboard cards are mapped: 51 executable cards in flows 1–6 and 49 PENDING-FEATURE cards in flows 7–12.

Each of three complete runs recorded 17 PASS and 34 FAIL. There were no skips, missing parent results, or differing parent/subtest outcomes. Durations: 265.014s, 230.245s and 236.476s.

| Flow | Cards | PASS | FAIL |
|---|---:|---:|---:|
| 1: Install and first run | 8 | 3 | 5 |
| 2: Start work | 6 | 1 | 5 |
| 3: Navigator | 7 | 5 | 2 |
| 4: Many machines | 5 | 4 | 1 |
| 5: Phone | 18 | 0 | 18 |
| 6: Agent runs | 7 | 4 | 3 |

All 34 failing cards have unique bug beads with lane/project/host labels, V/C metadata, named acceptance tests, expected/actual screens and transcript paths. Their continued presence on the canonical board was verified. See [bug list](BUG-LIST.md), [card map](CARD-TEST-MAP.md), [flakiness](FLAKY.md), and [completion audit](completion-audit.json).

Phone cards fail at absent owner CLI prerequisites. Downstream physical iPhone screens, Keychain, encrypted RPC, cellular and revocation behavior remain unexercised. Provider/SSH fixtures establish only their mapped Linux screen/state assertions; they do not establish vendor authentication or real network/device acceptance.

The real Linux binary hash and all four test-file hashes match the saved measurements. Frozen source pages and PRD match current source artifacts. Compilation was rechecked with GOFLAGS=-p=4 and nice level 15. Existing tracked production code and tests are unchanged.

The suite intentionally exits nonzero for observed acceptance failures. No push, release, deployment or provider-account operation was performed.
