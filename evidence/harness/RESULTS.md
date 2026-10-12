# Phone harness acceptance results

Base `71ef5dc5`; branch `codex/wt-phone-harness-20261011`. `go version go1.27.2 linux/amd64`. All Go invocations used nice 15 and `GOFLAGS=-p=4`. Real web assets copied from the exact source checkout were used for embedding; no placeholder assets.

| Measurement | Before | After |
|---|---|---|
| Original flow-5 card outcomes | 0 PASS / 18 FAIL | 0 PASS / 18 FAIL, each ×3 |
| Phone prerequisite assertion failures | 18 / 18 cards | 0 / 18 cards, each ×3 |
| Physical phone acceptance | REQUIRED-UNRUN | REQUIRED-UNRUN |
| Full storyboard card suite | 21 PASS / 30 FAIL | 21 PASS / 30 FAIL |
| Existing card/subtest outcome regressions | — | 0 |

Every repaired phone card reached its original unconditional physical-device fatal check, with no earlier prerequisite assertion failure. Those raw FAIL outcomes are retained; no card is skipped or relabeled PASS. No product failure surfaced in the repaired phone prerequisites, so no product code was changed. The 12 other inherited card failures remain outside this fixture correction.

The added `TestPhoneStoryboardHarnessPrerequisites` (pair, devices, revoke) and the existing `TestPhoneStoryboardWingJourney` both PASS ×3. Backend PASS is not card PASS. None of the 18 phone beads was closed: their original card tests did not satisfy the three-PASS rule.

| Gate | Result | Receipt |
|---|---|---|
| Full storyboard suite, including all original named subtests | No regressions; retained nonzero exit | [before](storyboard-before.jsonl), [after](storyboard-after.jsonl) |
| Flow 5 and both backend tests, count=3 | 54 required physical-card FAIL; 6 backend top-level PASS; prerequisites succeed | [flow5-after.jsonl](flow5-after.jsonl) |
| go vet ./... | PASS | [vet.txt](vet.txt) |
| Race: phonepair / pake / relay / wing | PASS | [race.txt](race.txt) |
| Race: both phone CLI/harness tests | PASS | [harness-race.txt](harness-race.txt) |
| CGO_ENABLED=0 cmd/wt compilation | PASS | [nocgo-compile.txt](nocgo-compile.txt) |
| Independent codex exec -m gpt-6.1-sol -s read-only review | Completed; see independent assertion comparison | [HARNESS-REVIEW.md](../../HARNESS-REVIEW.md), [execution log](review-exec.txt) |
| Byte-for-byte original assertion/content reconstruction | PASS | [assertion-integrity.json](assertion-integrity.json) |

Exact commands:

```sh
GOFLAGS=-p=4 nice -n 15 go test -json -count=1 -timeout=30m ./cmd/wt -run '^(TestStoryboard|TestPhoneStoryboard)'
GOFLAGS=-p=4 nice -n 15 go test -json -count=3 -timeout=15m ./cmd/wt -run '^(TestStoryboardPhoneCard|TestPhoneStoryboard)'
GOFLAGS=-p=4 nice -n 15 go vet ./...
GOFLAGS=-p=4 nice -n 15 go test -race -count=1 -timeout=20m ./internal/phonepair ./internal/pake ./internal/relay ./internal/wing
GOFLAGS=-p=4 nice -n 15 go test -race -count=1 -timeout=15m ./cmd/wt -run '^TestPhoneStoryboard'
CGO_ENABLED=0 GOFLAGS=-p=4 nice -n 15 go test -run '^$' ./cmd/wt
nice -n 15 codex exec -m gpt-6.1-sol -s read-only -C /work/lanes/wt-phone-harness/wingthing --ephemeral -o HARNESS-REVIEW.md - < .scratch/harness-review-prompt.txt
```

The first command ran once before edits and once after. Focused flow-5 runs retained all original card tests. Existing native-Mac physical subtest skips are identical before and after; no skip logic was added to the original suite. Detailed named outcomes and log hashes are in [results.json](results.json). Retained raw ANSI and PTY screen checkpoints are indexed in [transcripts.json](transcripts.json). The review inspected runtime logs before they finished; this report checks their completed outcomes separately. Both review invocations were read-only, and their prompts are preserved in the execution logs and [review-prompt.txt](review-prompt.txt).

Required physical-only checks remain camera/QR readability, native phone screens and emoji approval, device Keychain/Secure Enclave, cellular and real Wi-Fi/Tailscale behavior, phone permission recovery, and background/lock-screen notifications. The Go peer does not establish these. The inherited physical checklist and bead IDs are recorded in [the prior phone results](../phone/RESULTS.md); its old missing-main-wing diagnosis is superseded by this report.
