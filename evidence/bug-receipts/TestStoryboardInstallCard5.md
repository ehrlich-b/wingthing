Card: 01/card-5, ordinal #4: Choose an agent and consent to its exact installer
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/01-install.html#4
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Exact selected vendor installer, machine, consent and Escape
Expected observable state: Cancel leaves agent selection and local inventory unchanged; no installer execution

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (installer selection):
        wt | 0 sessions | 1 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 0s ago
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        Enter accept  Esc cancel  Ctrl-C quit
        Agent (blank = shell): claude_
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardInstallCard5/TestStoryboardInstallCard5-140-3907788.txt
--- FAIL: TestStoryboardInstallCard5 (8.09s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardInstallCard5.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardInstallCard5/TestStoryboardInstallCard5-140-3907788.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardInstallCard5$" ./cmd/wt
