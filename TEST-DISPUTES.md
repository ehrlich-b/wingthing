# Storyboard acceptance disputes

The four imported `cmd/wt/storyboard*_pty_test.go` files are byte-for-byte
identical to commit `580d4d9`; no assertion, timeout, skip, or test selection was
weakened. The stronger Northstar fixture checks already on integration commit
`3a0b7fa8` were retained rather than replaced with older parent versions.

## Flow 5 cannot become green in this Linux task

`storyboardPhone` creates a standalone egg but never starts its main wing. It
then runs `wt pair`, `wt devices`, or `wt devices revoke`. The pairing protocol
requires these commands to contact the existing main wing; starting another
wing or emitting a fake invitation to satisfy the test would violate that
contract. The actual observed prerequisite error is "main wing is not ready
for pairing", not the earlier branch's "unknown command". CLI registration
and running-wing behavior are exercised separately by the added
`TestPhoneStoryboardWingJourney`, through a built CLI, kernel PTY, real wing,
Go testpeer client, and isolated gRPC egg fixtures.

Furthermore, `storyboardPhone` always calls `t.Fatal("REQUIRED-UNRUN: ...")`
after the prerequisite assertions. Thus all 18 imported tests remain FAIL even
if their setup is repaired. There is no path to PASS on Linux without weakening
these tests. We preserve FAIL in the raw suite and list physical acceptance as
REQUIRED-UNRUN separately. Backend PASS is never substituted for card PASS.

Card 11's missing-device copy is now exercised on a running wing: `wt devices
revoke unpaired-phone` returns `device "unpaired-phone" is not paired with this
wing`. The original fixture cannot reach that behavior without a wing.

## Full suite gate

The inherited suite deliberately tests missing onboarding, durable unread and
policy presentation behavior outside this phone lane. Its nonzero exit is
retained. The final report compares all card and subtest outcomes against the
three original e2e runs and records any regressions separately. No unrelated
card is relabeled PASS, skipped, or removed to obtain a green gate.

## Race prerequisite

The first full race invocation used the documented terminal embedding
placeholder (`make web/dist`). `TestStaticSW` and `TestStaticManifest` failed
because the placeholder has neither asset. The real web assets were then built
with `npm ci --ignore-scripts` and `npm run build`, and the full four-package
race invocation was rerun. Both logs are retained; these initial failures are
not evidence of a phone protocol race.

## Additional CLI journey race check

After the requested package race gate passed, an additional race run of the
CLI journey found the inherited Northstar PTY query-reader goroutine reading
`vt.Emulator.closed` concurrently with cleanup calling `Emulator.Close`.
The harness now closes the thread-safe input pipe and joins the query reader
before closing the emulator. This strengthens fixture cleanup and changes no
card assertion. The failing race receipt is `evidence/phone/journey-race.txt`;
the rerun is `evidence/phone/journey-race-fixed.txt`. A final full suite is
also run after this harness change.
