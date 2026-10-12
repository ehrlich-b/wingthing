Card: 06/card-3, ordinal #4: 4 · What can this egg touch?
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/06-runs.html#4
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Launch policy workspace/provider home/denies/network review
Expected observable state: Effective authority explicit; full OS-user mode opt-in; displayed policy alone not confinement evidence

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (effective policy review):
        wt | 2 sessions | 4 machines
        B:1 W:1 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:1 bell:off
        Details: local:same
        Name: coordinator
        Status: working
        Machine: local  connected  updated 0s ago
        Cwd: /work/lanes/wt-e2e/wingthing/.scratch/northstar-4246799934/work
        Attention: active
        Lifecycle source: claude_hook  State cursor: 1
        Reason: Native provider is working.
        Agent/kind: claude / agent
        Age: 2m
        Isolation: fixture
        Readers: 1
        Writer: ownership not reported for this channel; v observes without input or resize
        Route: local; state: /work/lanes/wt-e2e/wingthing/.scratch/dc61344b3e/l
        Controls: Enter attach; v view; t confirm takeover; r rename; x confirm stop; n new
        Attach: wt attach -- same
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        j/k scroll  Esc/Enter back  Ctrl-C quit
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardRunsCard3/TestStoryboardRunsCard3-140-3937615.txt
--- FAIL: TestStoryboardRunsCard3 (8.25s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardRunsCard3.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardRunsCard3/TestStoryboardRunsCard3-140-3937615.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardRunsCard3$" ./cmd/wt
