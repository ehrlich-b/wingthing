Card: 01/card-1, ordinal #2: 2 · An empty navigator is a useful beginning
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/01-install.html#2
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Empty navigator, connection header, wing/roost explanation and useful keys
Expected observable state: Zero sessions and no account, provider or daemon started

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (first-run connection and terms):
        wt | 0 sessions | 1 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 3s ago (refreshing)
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
    northstar_pty_test.go:433: owned PTY child did not join
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardInstallCard1/TestStoryboardInstallCard1-140-3906014.txt
--- FAIL: TestStoryboardInstallCard1 (15.68s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardInstallCard1.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardInstallCard1/TestStoryboardInstallCard1-140-3906014.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardInstallCard1$" ./cmd/wt
