Card: 05/card-6, ordinal #7: 7 · Manage each wing separatelyMachines
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#7
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Independent paired machine list and wt devices registry
Expected observable state: Separate pins/credentials and revocation per wing

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt devices unavailable; expected prerequisite screen ["NAME" "PAIRED AT" "LAST SEEN"]; actual:
        Error: unknown command "devices" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard6/TestStoryboardPhoneCard6-160-3933506.txt
--- FAIL: TestStoryboardPhoneCard6 (0.09s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard6.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard6/TestStoryboardPhoneCard6-160-3933506.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard6$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
