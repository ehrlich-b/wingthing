Card: 05/card-11, ordinal #12: Bryan’s iPhone is lostAccess revoked
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#12
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: wt devices revoke receipt and phone Access revoked
Expected observable state: Persist revocation and close active streams before success; forge unaffected; no remote erasure claim

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt devices unavailable; expected prerequisite screen ["device" "not paired"]; actual:
        Error: unknown command "devices" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard11/TestStoryboardPhoneCard11-160-3933694.txt
--- FAIL: TestStoryboardPhoneCard11 (0.28s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard11.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard11/TestStoryboardPhoneCard11-160-3933694.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard11$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
