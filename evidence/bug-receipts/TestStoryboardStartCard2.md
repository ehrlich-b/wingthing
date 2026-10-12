Card: 02/card-2, ordinal #3: 3 · A new session in the current scope
Source: /work/lanes/wt-e2e/wingthing/evidence/storyboard-source/02-start.html#3
Carrier: f8f9ebd96e1c72336ffd796c8d496ecda0a85619; Linux forge; isolated HOME/WINGTHING_DIR; fixture provider/SSH, real built wt + kernel PTY.

Expected screen: n prompts selected machine and cwd, attaches scoped Claude parent
Expected observable state: Saved forge route, native parent/MCP registration, exact cwd and durable child receipts; generic route remains distinct

Actual outcome across three runs: FAIL, FAIL, FAIL.
Actual assertion/screen and state evidence:
screen timeout (scoped parent attached):
        wt | 0 sessions | 1 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [All] !:0 bell:off
        local  connected  updated 0s ago
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
        Claude launch failed during admission/policy: wing refused admission under its configured policy. Review the selected po
        licy and launch requirements. Policy: unavailable. Doctor launch-plan needs an explicit --allowed-home-root; supply the
        exact --home, --executable and policy/base inputs. No complete request was captured.
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardStartCard2-native-local-120-3912941.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"2af674d8c8bfaefaf4fc38cb6513ba165de6bc8516ad445c4d3113259d15889d","decision":"error","isolation":"wingthing-sandbox","principal":"user-976ea8b7a073ece24a8f","timestamp":"2026-10-12T00:20:07.670858848Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:20:07 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/193ddb6d69/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude codex]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-905930043/work/local project space]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:20:07 wing: local control ready (wing_id=7c5e29a4f08b41948f982237)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:20:07 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/193ddb6d69/l/egg.yaml (network=none)
        starting local-only wing
          agents: [claude codex]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-905930043/work/local project space]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:20:07 wing: local control ready (wing_id=7c5e29a4f08b41948f982237)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardStartCard2/native/local (8.80s)
=== RUN   TestStoryboardStartCard2/routing/forge
    storyboardScopedNew_pty_test.go:35: Unix socket lexical/canonical bytes: 63/63
    storyboardScopedNew_pty_test.go:35: Unix socket lexical/canonical bytes: 81/81
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardStartCard2-routing-forge-120-3914597.txt
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardStartCard2-routing-forge-120-3914540.txt
--- PASS: TestStoryboardStartCard2/routing/forge (1.82s)
=== RUN   TestStoryboardStartCard2/native/forge
    storyboardScopedNew_pty_test.go:102: Unix socket lexical/canonical bytes: 63/63
    storyboardScopedNew_pty_test.go:102: Unix socket lexical/canonical bytes: 81/81
    storyboardScopedNew_pty_test.go:113: screen timeout (scoped parent attached):
        wt | 0 sessions | 2 machines
        B:0 W:0 I:0 D:0 E:0 F:0 ?:0 U:0 | [forge] !:0 bell:off
        forge  connected  updated 0s ago
          (no sessions)
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        arrows/j/k move  Enter attach  v view  t takeover  n new  r rename  x stop
        / filter ! attention b bell m machines i details ? help R refresh q/Ctrl-C quit h recent
        exit status 1
    northstar_pty_test.go:445: PTY screen transcript: /work/lanes/wt-e2e/wingthing/.scratch/northstar/northstar-evidence/TestStoryboardStartCard2-native-forge-120-3914692.txt
    storyboard_pty_test.go:803: fixture runtime diagnostic mcp-audit.log: {"actor":"default","argument_sha256":"ea2ae86046d39c3adb0a89ee73f3d8205ff6989824d93e960b5ad84515b221d4","decision":"error","isolation":"wingthing-sandbox","principal":"user-cfc9d364158c67722dfa","timestamp":"2026-10-12T00:20:18.460411065Z","tool":"agent_start"}
    storyboard_pty_test.go:803: fixture runtime diagnostic wing.log: 2026/10/11 20:20:18 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/2cfca803d1/r/egg.yaml (network=none)
        starting local-only wing
          agents: [claude codex]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-724849306/forge project space]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:20:18 wing: local control ready (wing_id=0e007d2ef7324aa28d43bc14)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
    storyboard_pty_test.go:820: fixture wing diagnostic: 2026/10/11 20:20:18 egg: loaded wing config from /work/lanes/wt-e2e/wingthing/.scratch/2cfca803d1/r/egg.yaml (network=none)
        starting local-only wing
          agents: [claude codex]
          skills: []
          paths: [/work/lanes/wt-e2e/wingthing/.scratch/northstar-724849306/forge project space]
          projects: 0 found
          conv: auto
        
        2026/10/11 20:20:18 wing: local control ready (wing_id=0e007d2ef7324aa28d43bc14)
        wingthing MCP agent_start: provider write protection is modeled only for the macOS Seatbelt profile, not linux
--- FAIL: TestStoryboardStartCard2/native/forge (9.06s)
--- FAIL: TestStoryboardStartCard2 (20.42s)


Transcripts (card observations plus PTY checkpoints; kernel PTY checkpoints have adjacent .ansi raw bytes):
/work/lanes/wt-e2e/wingthing/evidence/run-1/cards/TestStoryboardStartCard2.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-routing-local-120-3912844.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-routing-local-120-3912782.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-native-local-120-3912941.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-routing-forge-120-3914597.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-routing-forge-120-3914540.txt
/work/lanes/wt-e2e/wingthing/evidence/run-1/transcripts/northstar/northstar-evidence/TestStoryboardStartCard2-native-forge-120-3914692.txt

Reproduce: cd /work/lanes/wt-e2e/wingthing && GOFLAGS=-p=4 nice -n 15 go test -count=1 -run "^TestStoryboardStartCard2$" ./cmd/wt
