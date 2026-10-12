Card: 02/card-1, ordinal #2: 2 · Choose cwd explicitly
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/02-start.html#2
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Explicit --cwd parent output; outside-scope refusal
Expected observable state: Receiving machine stores chosen canonical cwd; no session admitted on denied path

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (explicit cwd parent):
        Error: Claude launch failed during admission/policy: wing refused admission under its configured policy. Review the selected policy and laun
        ch requirements. Policy: unavailable. Doctor launch-plan needs an explicit --allowed-home-root; supply the exact --home, --executable and po
        licy/base inputs. No complete request was captured.
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/storyboard-evidence/TestStoryboardStartCard1/TestStoryboardStartCard1-140-3911802.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"1e0c6bb6b484a3db39048378b729ab339f4a5b2a97cb851e951c0f176b100875","decision":"error","isolation":"wingthing-sandbox","principal":"user-69a605404ddf414640ba","timestamp":"2026-10-12T00:19:58.068769891Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:19:57 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/e8620c66ec/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2505679870/work]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:19:58 wing: local control ready (wing_id=702085ce63904d59a4f2fe8c)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:19:57 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/e8620c66ec/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2505679870/work]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:19:58 wing: local control ready (wing_id=702085ce63904d59a4f2fe8c)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardStartCard1 (9.51s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardStartCard1.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/storyboard-evidence/TestStoryboardStartCard1/TestStoryboardStartCard1-140-3911802.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardStartCard1$" ./cmd/wt
