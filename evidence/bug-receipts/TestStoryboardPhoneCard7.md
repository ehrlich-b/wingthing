Card: 05/card-7, ordinal #8: QR expired or already usedScan a fresh QR
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#8
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Expired/used QR refused; Enter creates a fresh QR
Expected observable state: Old invitation cannot grant; fresh secret/ID/deadline; memory-only single use

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt pair unavailable; expected prerequisite screen ["Pair a phone" "5:00" "Waiting for scan"]; actual:
        Error: unknown command "pair" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard7/TestStoryboardPhoneCard7-160-3933528.txt
--- FAIL: TestStoryboardPhoneCard7 (0.30s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard7.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard7/TestStoryboardPhoneCard7-160-3933528.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard7$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
