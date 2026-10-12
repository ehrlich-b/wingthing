# Phone acceptance results

Base `3a0b7fa8578b9889d9e17a0e5fb3a1ffab9831ca`; imported tests `580d4d9f8241fd37f56011bced9535872128eeda`. All Go commands ran at nice level 15 with `GOFLAGS=-p=4`.

Implemented fixes: preserve native needs-input attention after a wing restart; return cancelled for a cancelled invitation scan; diagnose an unpaired device in the revoke CLI. Added an in-memory Go testpeer client and a real CLI/wing/PTY backend journey.

Full unchanged storyboard suite: {'PASS': 21, 'FAIL': 30, 'FLAKY': 0, 'PENDING-FEATURE': 49}. Card regressions against the earlier e2e base: 0; named subcase regressions: 0. Integration-base improvements: TestStoryboardStartCard2, TestStoryboardStartCard4, TestStoryboardRunsCard1, TestStoryboardRunsCard5. Flaky tests: 0.

The full suite remains red, with no regressions against the imported baseline. Flow 5 has 18 FAIL/FAIL/FAIL card outcomes; the fixture does not start a main wing and every phone card deliberately ends in a physical-device fatal assertion. See ../../TEST-DISPUTES.md. Physical obligations below remain REQUIRED-UNRUN.

Backend gate: CLI/testpeer journey PASS ×3; every focused phonepair/PAKE/wing protocol case PASS ×3; phone relay cases PASS ×3. Full race on phonepair/pake/relay/wing PASS after building real web assets. go vet ./... PASS. The final CLI journey race also PASS; the final full-suite rerun after harness cleanup has identical card outcomes. Raw logs retain failed exploratory runs and the initial placeholder-assets race failure.

No bug bead was closed: each original card test is still red, so the requested three-pass condition has not been met. The backend three-pass evidence does not close a physical acceptance bead.

| Card | Bead (not closed in this task) | Card result | Backend evidence | Physical-only obligation |
|---|---|---|---|---|
| 05/card-0 | fleet-cpab | FAIL ×3 | CLI/testpeer invitation, identity, words, deadline, unchanged daemon | REQUIRED-UNRUN: Real terminal QR camera scan and readable layout on the iPhone |
| 05/card-1 | fleet-mxtz | FAIL ×3 | CLI/testpeer PAKE proof and matching emoji; protocol pin tests | REQUIRED-UNRUN: Camera scan, native screen and local-network permission |
| 05/card-2 | fleet-bmxl | FAIL ×3 | CLI/testpeer both independent approvals; credential recovery tests | REQUIRED-UNRUN: Four-emoji presentation and deliberate approval on the phone |
| 05/card-3 | fleet-5bo6 | FAIL ×3 | Protocol expiry/used/cancel tests with deterministic clock | REQUIRED-UNRUN: Expiry presentation and fresh-QR rescan on the phone |
| 05/card-4 | fleet-wudw | FAIL ×3 | Protocol single-use claims; CLI cancellation/new invitation | REQUIRED-UNRUN: Used-invitation screen and return to scanner |
| 05/card-5 | fleet-lo13 | FAIL ×3 | Transcript pin and wrong-proof protocol tests | REQUIRED-UNRUN: Changed-pin hard-stop screen and no automatic trust replacement |
| 05/card-6 | fleet-ag5w | FAIL ×3 | CLI device listing; protocol registry hash, mode 0600 and persistence | REQUIRED-UNRUN: Device-only Keychain / Secure Enclave storage and native success screen |
| 05/card-7 | fleet-yq9i | FAIL ×3 | Opaque accountless relay routing tests; replay and revocation tests | REQUIRED-UNRUN: Cellular relay reconnection and acknowledgement-loss UI without replay |
| 05/card-8 | fleet-pet5 | FAIL ×3 | Relay unreachable/denied tests; no live iPhone cache evidence | REQUIRED-UNRUN: Offline cached-state screen; no fabricated live status |
| 05/card-9 | fleet-ezc3 | FAIL ×3 | CLI/testpeer configuration denial and colliding remote untouched | REQUIRED-UNRUN: Phone authority and independently paired-wing UI |
| 05/card-10 | fleet-v2pw | FAIL ×3 | Protocol other-wing denial; registry bound to one wing | REQUIRED-UNRUN: Second physical machine pairing and separate Keychain credentials |
| 05/card-11 | fleet-8xhy | FAIL ×3 | CLI/testpeer revoke, closed live read and denied reconnect | REQUIRED-UNRUN: Real lost-phone/live-access revocation over network carriers |
| 05/card-12 | fleet-0p5v | FAIL ×3 | CLI/testpeer blocked inventory and attached read/input/detach | REQUIRED-UNRUN: Native session view, terminal rendering, input and delivery-unknown UI |
| 05/card-13 | fleet-v3b6 | FAIL ×3 | CLI/testpeer exact-session stop; wing attachment/input/stop tests | REQUIRED-UNRUN: Native explicit stop confirmation and detach distinction |
| 05/card-14 | fleet-0oli | FAIL ×3 | No physical/background notification execution; attention inventory only | REQUIRED-UNRUN: Notification consent, quiet lock-screen copy, tap routing and background delivery |
| 05/card-15 | fleet-qh4d | FAIL ×3 | CLI/testpeer direct LAN; fixture Tailscale IP/MagicDNS and endpoint-order tests | REQUIRED-UNRUN: Real Wi-Fi/Tailscale IP/MagicDNS carrier selection on both devices |
| 05/card-16 | fleet-hqms | FAIL ×3 | CLI hidden fallback link has endpoint/key/words; iOS Settings unrun | REQUIRED-UNRUN: Denied camera/local-network Settings recovery and private manual invitation entry |
| 05/card-17 | fleet-6ev0 | FAIL ×3 | CLI/testpeer recovered same grant after ignored final response; duplicate confirm | REQUIRED-UNRUN: Native final-reply-loss recovery screen with the original grant |

Regression details are in results.json; raw full-suite evidence is ../run-{1,2,3}/. No push, deployment, real-device install or account/entitlement operation was performed.
