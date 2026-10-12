Card: 05/card-12, ordinal #13: Attach and type from the phonepairing
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/05-phone.html#13
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Phone output/composer and explicit Send; delivery unknown on lost ack
Expected observable state: Wing-bound encrypted input exactly once; reconnect reads, never replays

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
phone card blocked: required wt pair unavailable; expected prerequisite screen ["Pair a phone" "5:00" "Waiting for scan"]; actual:
        Error: unknown command "pair" for "wt"
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardPhoneCard12/TestStoryboardPhoneCard12-160-3933746.txt
--- FAIL: TestStoryboardPhoneCard12 (0.08s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardPhoneCard12.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardPhoneCard12/TestStoryboardPhoneCard12-160-3933746.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardPhoneCard12$" ./cmd/wt

The failure is a missing owner CLI prerequisite. Downstream iPhone camera/Keychain/crypto/RPC/route assertions are unexercised, not separately proven failures. A physical iPhone acceptance gate remains mandatory after that prerequisite ships.
