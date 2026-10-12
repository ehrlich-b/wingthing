Card: 02/card-4, ordinal #5: Provider missing, policy denied, or daemon not ready
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/02-start.html#5
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Launch failure phase, named policy, actionable next step and admitted-ID recovery
Expected observable state: Policy/provider preparation and readiness are distinct; no privileged fallback, secret exposure or duplicate admission

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (native denial):
        Error: Claude launch failed during admission/policy: wing refused admission under its configured policy. Review the selected policy and launch requirements. Policy: unavailable. Doctor launch-plan needs an explicit --allowed-home-root; supp
        ly the exact --home, --executable and policy/base inputs. No complete request was captured.
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardStartCard4-native-240-3917749.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"df404717ee4e22c638f9c53a4c1bb303cf8ebf45c9babacd5e1d7285ec7db219","decision":"error","isolation":"wingthing-sandbox","principal":"user-957cbb9f5a1268567e7c","timestamp":"2026-10-12T00:20:41.11892636Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:20:41 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/f6c05553ac/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2719393531/work]
          projects: 1 found
            work → /work/lanes/wt-e2e/wingthing/.scratch/northstar-2719393531/work
          conv: auto
        
        2026/10/11 20:20:41 wing: local control ready (wing_id=b837811848f6440389f98539)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:20:41 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/f6c05553ac/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2719393531/work]
          projects: 1 found
            work → /work/lanes/wt-e2e/wingthing/.scratch/northstar-2719393531/work
          conv: auto
        
        2026/10/11 20:20:41 wing: local control ready (wing_id=b837811848f6440389f98539)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardStartCard4/native (9.59s)
--- FAIL: TestStoryboardStartCard4 (16.84s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardStartCard4.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-transport-120-3917005.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-transport-240-3916991.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-remote-policy-120-3917146.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-remote-attach-120-3917259.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-success-120-3917584.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard4-native-240-3917749.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardStartCard4$" ./cmd/wt
