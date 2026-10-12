Card: 02/card-0, ordinal #1: 1 · wt claude from a cold start
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/02-start.html#1
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Cold Claude provider output then reattach same ID
Expected observable state: Exactly one main local-only daemon and one parent at current cwd; detach preserves process/policy

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (cold parent ready):
        Error: Claude launch failed during admission/policy: wing refused admission under its configured policy. Review the selected policy and laun
        ch requirements. Policy: unavailable. Doctor launch-plan needs an explicit --allowed-home-root; supply the exact --home, --executable and po
        licy/base inputs. No complete request was captured.
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardStartCard0/TestStoryboardStartCard0-140-3910633.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"a5660bdd21e37a3861779cc4a5d662bd593ef4360b46d15a572e6950ee427d10","decision":"error","isolation":"wingthing-sandbox","principal":"user-006aff4dd2502b031fdf","timestamp":"2026-10-12T00:19:48.62600051Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:19:48 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/28fd52fb9f/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-1006805501/work]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:19:48 wing: local control ready (wing_id=fca995f2cc904055a221fb88)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:19:48 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/28fd52fb9f/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-1006805501/work]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:19:48 wing: local control ready (wing_id=fca995f2cc904055a221fb88)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardStartCard0 (9.73s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardStartCard0.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardStartCard0/TestStoryboardStartCard0-140-3910633.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardStartCard0$" ./cmd/wt
