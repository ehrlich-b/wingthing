Card: 06/card-5, ordinal #6: Policy, toolchain or native provider readiness failed
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/06-runs.html#6
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: Policy/toolchain/provider-readiness failure phase/source/action
Expected observable state: Denials authoritative; launch receipts retained; no policy broadening, secret exposure or duplicate retry

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (native denial):
        Error: Claude launch failed during admission/policy: wing refused admission under its configured policy. Review the selected policy and launch requirements. Policy: unavailable. Doctor launch-plan needs an explicit --allowed-home-root; supp
        ly the exact --home, --executable and policy/base inputs. No complete request was captured.
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardRunsCard5-native-240-3939982.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"69676a756cf92a8f29d30d21ac1e5809d8f5838d39a7757bbce41956fc6e00e3","decision":"error","isolation":"wingthing-sandbox","principal":"user-0c99baee1025c2987bc1","timestamp":"2026-10-12T00:22:53.70777825Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:22:53 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/689e986c54/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2362739530/work]
          projects: 1 found
            work → /work/lanes/wt-e2e/wingthing/.scratch/northstar-2362739530/work
          conv: auto
        
        2026/10/11 20:22:53 wing: local control ready (wing_id=b013886a0cb747bc8c75436d)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:22:53 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/689e986c54/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-2362739530/work]
          projects: 1 found
            work → /work/lanes/wt-e2e/wingthing/.scratch/northstar-2362739530/work
          conv: auto
        
        2026/10/11 20:22:53 wing: local control ready (wing_id=b013886a0cb747bc8c75436d)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardRunsCard5/native (9.16s)
--- FAIL: TestStoryboardRunsCard5 (13.27s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardRunsCard5.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-transport-120-3939265.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-transport-240-3939230.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-remote-policy-120-3939589.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-remote-attach-120-3939712.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-success-120-3939853.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardRunsCard5-native-240-3939982.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardRunsCard5$" ./cmd/wt
