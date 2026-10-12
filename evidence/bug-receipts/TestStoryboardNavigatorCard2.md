Card: 03/card-2, ordinal #3: 3 · Blocked now; unread next
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/03-navigator.html#3
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Blocked lifecycle, separate unread badge/count and acknowledgement
Expected observable state: Hook cursors scoped by machine/session; completion not exit; unacknowledged unread cursor persists for same viewer across navigator reopen

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (same viewer durable unread after reopen):
        wt | 1 sessions | 1 machines
        B:0 W:0 I:1 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 0s ago
          /work/lanes/wt-e2e/wingthing/.scratch/northstar-254583916/work
        > I unread-parent                                                           claude       northstar-254583916/work                      2m
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardNavigatorCard2-durable-unread-cursor/TestStoryboardNavigatorCard2-durable-unread-cursor-140-3924193.txt
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardNavigatorCard2-durable-unread-cursor/TestStoryboardNavigatorCard2-durable-unread-cursor-140-3924166.txt
--- FAIL: TestStoryboardNavigatorCard2/durable-unread-cursor (8.21s)
--- FAIL: TestStoryboardNavigatorCard2 (21.27s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardNavigatorCard2.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardNavigatorCard2-symlinked-TMPDIR-native-hooks-120-3924090.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardNavigatorCard2-symlinked-TMPDIR-native-hooks-120-3923607.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardNavigatorCard2-durable-unread-cursor/TestStoryboardNavigatorCard2-durable-unread-cursor-140-3924193.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardNavigatorCard2-durable-unread-cursor/TestStoryboardNavigatorCard2-durable-unread-cursor-140-3924166.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardNavigatorCard2$" ./cmd/wt
