Card: 01/card-7, ordinal #6: Recover install, PATH or provider-auth failure
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/01-install.html#6
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Failed install/PATH/auth stage with fresh-consent retry, rescan and Back
Expected observable state: Preserve selected provider and any admitted ID; no silent retry or policy change

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (installer failure recovery route):
        wt | 0 sessions | 1 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 2s ago (refreshing)
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        Enter accept  Esc cancel  Ctrl-C quit
        Agent (blank = shell): claude_
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardInstallCard7/TestStoryboardInstallCard7-140-3909466.txt
--- FAIL: TestStoryboardInstallCard7 (8.06s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardInstallCard7.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardInstallCard7/TestStoryboardInstallCard7-140-3909466.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardInstallCard7$" ./cmd/wt
