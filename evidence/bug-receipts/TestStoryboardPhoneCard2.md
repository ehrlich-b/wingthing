Card: 05/card-2, ordinal #3: 3 · Check both screensPair with EhrlichMac?
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#3
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Same four emoji on phone/terminal; both confirmations
Expected observable state: No credential before both approvals; cancellation burns invitation

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt pair unavailable; expected prerequisite screen ["Pair a phone" "5:00" "Waiting for scan"]; actual:
        Error: unknown command "pair" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard2/TestStoryboardPhoneCard2-160-3933432.txt
--- FAIL: TestStoryboardPhoneCard2 (0.06s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard2.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard2/TestStoryboardPhoneCard2-160-3933432.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard2$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
