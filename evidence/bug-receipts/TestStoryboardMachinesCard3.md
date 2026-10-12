Card: 04/card-3, ordinal #4: forge is offline
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/04-machines.html#4
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Offline forge/stale inventory plus available local attachment
Expected observable state: Cannot validate remote action; no same-name local fallback

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (stale attach denied):
        wt | 2 sessions | 4 machines
        B:1 W:1 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:1 bell:off
        local  connected  updated 0s ago
          (no matches)
        empty  connected  updated 0s ago
          (no matches)
        forge  offline  last success 8s ago; cached rows stale
          ! remote "forge": SSH to ns-forge ended with status 255; check the SSH diagnostic above. Session state is unknown; reconnect with the sam…
          /work/lanes/wt-e2e/wingthing/.scratch/northstar-245523597/work
        > B coordinator                                                         claude       northstar-245523597/work                    2m    stale
        offline  offline
          ! remote "offline": SSH to ns-offline ended with status 255; check the SSH diagnostic above. Session state is unknown; reconnect and list…
          (inventory unavailable)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
        exit status 255; Enter retries through fresh resolution. [remote attachment; Ctrl+B then q detaches; Ctrl+B twice sends Ctrl+B]?fixture quer
        y failure?Connection to forge (ns-forge) ended; session outcome unknown. Reconnect: WINGTHING_DIR='/work/lanes/wt-e2e/wingthing/.scratch/849
        66474f1/l' WINGTHING_PREVIEW_DIR='/work/lanes/wt-e2e/wingthing/.scratch/84966474f1/l' '/work/lanes/wt-e2e/wingthing/.scratch/storyboard-wt'
        'attach' '--' 'forge:same'?Pinned route: WINGTHING_DIR='/work/lanes/wt-e2e/wingthing/.scratch/84966474f1/l' WINGTHING_PREVIEW_DIR='/work/lan
        es/wt-e2e/wingthing/.scratch/84966474f1/l' '/work/lanes/wt-e2e/wingthing/.scratch/storyboard-wt' '--remote' 'ns-forge' '--remote-binary' 'wt
        ' '--remote-state' '/work/lanes/wt-e2e/wingthing/.scratch/84966474f1/r' 'attach' '--' 'same'?SSH to ns-forge ended with status 255; check th
        e SSH diagnostic above. Session state is unknown; reconnect with the same --remote-state and list sessions before relaunching?
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardMachinesCard3/TestStoryboardMachinesCard3-140-3931809.txt
--- FAIL: TestStoryboardMachinesCard3 (8.28s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardMachinesCard3.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardMachinesCard3/TestStoryboardMachinesCard3-140-3931809.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardMachinesCard3$" ./cmd/wt
