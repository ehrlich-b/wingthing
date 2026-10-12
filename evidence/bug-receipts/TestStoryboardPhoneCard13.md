Card: 05/card-13, ordinal #14: Stop means stop this sessionStop pairing?
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#14
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Phone Stop pairing? names exact machine/session; Keep running
Expected observable state: Detach preserves process; confirmed stop receipt targets exact execution, other sessions alive

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt pair unavailable; expected prerequisite screen ["Pair a phone" "5:00" "Waiting for scan"]; actual:
        Error: unknown command "pair" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard13/TestStoryboardPhoneCard13-160-3933771.txt
--- FAIL: TestStoryboardPhoneCard13 (0.07s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard13.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard13/TestStoryboardPhoneCard13-160-3933771.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard13$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
