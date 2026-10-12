Card: 01/card-4, ordinal #3: Detect agents without a network check
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/01-install.html#3
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Local agent detection and no-agent chooser/skip
Expected observable state: PATH-only inspection; no provider, installer or account operation

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (local no-agent detection):
        wt | 0 sessions | 1 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 0s ago
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardInstallCard4/TestStoryboardInstallCard4-140-3906765.txt
--- FAIL: TestStoryboardInstallCard4 (8.06s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardInstallCard4.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardInstallCard4/TestStoryboardInstallCard4-140-3906765.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardInstallCard4$" ./cmd/wt
