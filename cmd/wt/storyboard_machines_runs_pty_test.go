package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// Capture bytes from the kernel PTY, before quitting or leaving the current
// screen. Screenshot rendering replays these bytes; it never draws a mock UI.
func storyboardCapture(t *testing.T, h *northstarPTY, name string) {
	t.Helper()
	destination := os.Getenv("WT_STORYBOARD_EVIDENCE")
	if destination == "" {
		return
	}
	if !filepath.IsAbs(destination) {
		t.Fatal("absolute evidence directory required")
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	data := append([]byte{}, h.transcript.Bytes()...)
	screen := h.emu.String()
	h.mu.Unlock()
	if err := os.WriteFile(filepath.Join(destination, name+".ansi"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, name+".txt"), []byte(screen), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(h.cmd.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	metadata, _ := json.Marshal(map[string]any{"cols": 120, "rows": 36, "binary_sha256": hex.EncodeToString(sum[:]), "test": t.Name(), "pty": true})
	if err := os.WriteFile(filepath.Join(destination, name+".json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
}

func storyboardParentReceipt(t *testing.T, binary, local, remote, work string, env []string, runID, key string) {
	t.Helper()
	f := northstarNewFixture(t)
	f.local, f.remote, f.work, f.env = local, remote, work, append(env, "TERM=xterm-256color", "LANG=en_US.UTF-8")
	h := northstarStartPTY(t, f, binary, 120, 36)
	h.expect("real wing inventory navigator", 0, "wt   ", "m machines", "h runs")
	h.expect("machine verified by actual handshake", h.send("mjj i"), "Machines · forge", "same machine each time it connects")
	storyboardCapture(t, h, "04-2-verified-remote")
	h.send("\x1b")
	h.expect("return before history", 0, "m machines")
	h.expect("owning wing history", h.send("h"), "Recent runs", "forge", "Project")
	// Filter only after the run row has loaded; keys sent earlier act on an empty list.
	h.expect("run row loaded", 0, "1 run")
	h.waitScreen("qualified receipt", h.send("/"+runID+"\ri"), func(screen string) bool {
		return strings.Contains(screen, "Next: ") && strings.Contains(screen, "Time limit: 90 seconds") && (strings.Contains(screen, "Result: ! Pending") || strings.Contains(screen, "Result: * Working")) && !strings.Contains(screen, "unknown_outcome")
	})
	storyboardCapture(t, h, "06-2-parent-agent-receipt")
	h.send("\x1b")
	h.expect("back to history", 0, "Recent runs")
	h.exit("q")
}

func TestStoryboardMachinesRunsPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	profiles := map[string]config.Remote{"forge": {SSHTarget: "ns-forge", WTBinary: binary, WingthingDir: f.remote}}
	if err := config.SaveRemotes(f.local, profiles); err != nil {
		t.Fatal(err)
	}
	local := northstarStandaloneEgg(t, f, f.local, "same-id", "coordinator", "working", "my-mac fixture / exact local session same-id")
	remote := northstarStandaloneEgg(t, f, f.remote, "same-id", "coordinator", "working", "forge fixture / exact remote session same-id")
	h := northstarStartPTY(t, f, binary, 120, 36)
	h.expect("two isolated session origins", 0, "machines connected", "coordinator")
	h.expect("prepare second machine once", h.send("/local\rM"), "Machines · This machine", "wt remote add Studio studio.local")
	storyboardCapture(t, h, "04-1-prepare-remote")
	h.send("\x1b")
	h.expect("exact remote selected", h.send("/\x15forge\r"), "coordinator")
	h.expect("read-only remote attach", h.send("v"), "forge fixture / exact remote session same-id")
	storyboardCapture(t, h, "04-3-exact-remote-session")
	h.expect("detach remote", h.send("\x02q"), "Enter open")
	select {
	case id := <-remote.attached:
		if id != "same-id" {
			t.Fatal(id)
		}
	default:
		t.Fatal("remote was not attached")
	}
	if len(local.attached) != 0 || local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("attach crossed origin or detach stopped session")
	}
	northstarWrite(t, filepath.Join(f.home, "forge-offline"), "offline fixture", 0600)
	h.expect("remote unavailable retains local", h.send("R"), "forge not searched", "Last seen")
	storyboardCapture(t, h, "04-4-remote-offline")
	// Change only the fixture SSH adapter, leaving host sshd/config untouched.
	northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nprintf 'REMOTE HOST IDENTIFICATION HAS CHANGED!\\n' >&2\nexit 255\n", 0700)
	h.expect("identity refusal", h.send("RM"), "Machines · forge", "Identity changed")
	storyboardCapture(t, h, "04-5-host-key-version")
	h.send("\x1b")
	northstarWrite(t, filepath.Join(f.bin, "ssh"), `#!/bin/sh
case "$*" in *"'--version'"*) printf 'wt version fixture-old\n';exit 0;; esac
printf 'Error: unknown flag: --remote-inventory\n' >&2
exit 1
`, 0700)
	h.expect("incompatible receiver", h.send("RM"), "Machines · forge", "Update wt", "before reconnecting")
	storyboardCapture(t, h, "04-5-old-version")
	h.send("\x1b")
	h.expect("diagnostics menu", h.send("d"), "Runs / diagnostics", "Explain launch readiness")
	provider := filepath.Join(f.bin, "codex")
	northstarWrite(t, provider, "#!/bin/sh\nexit 99 # planner must never execute this fixture\n", 0700)
	policy := filepath.Join(f.work, "egg.yaml")
	northstarWrite(t, policy, "base: none\nfs: ['ro:/', 'rw:"+f.work+"', 'deny:/private-data']\nnetwork: {agent_domains: none}\n", 0600)
	h.expect("explicit planner form", h.send("1"), "Route: terminal_start")
	values := []string{"agent_run", "codex", f.work, f.home, f.home, provider, policy, f.work, f.home}
	for i, value := range values {
		h.expect(fmt.Sprintf("planner input %d", i+1), h.send("\x15"+value+"\r"), []string{"Agent (blank", "Existing workspace", "Explicit provider HOME", "Allowed provider HOME root", "Absolute executable", "Policy file", "Policy base", "Host HOME", "Egg reach"}[i])
	}
	h.expect("effective launch report", 0, "Launch plan: blocked", "Blocked phase: network.provider")
	storyboardCapture(t, h, "06-6-policy-readiness-failed")
	h.expect("reach has full rules", h.send("\x1b[6~"), "Egg reach", "deny:/private-data")
	storyboardCapture(t, h, "06-4-egg-reach")
	h.send("\x1b")
	h.expect("diagnostics return", 0, "Runs / diagnostics")
	h.expect("explicit default policy selection", h.send("1"), "Route: terminal_start")
	values[6] = ""
	for i, value := range values {
		h.expect(fmt.Sprintf("unverified planner input %d", i+1), h.send("\x15"+value+"\r"), []string{"Agent (blank", "Existing workspace", "Explicit provider HOME", "Allowed provider HOME root", "Absolute executable", "Policy file", "Policy base", "Host HOME", "Egg reach"}[i])
	}
	h.expect("unverified local plan", 0, "Launch plan: unverified", "built-in defaults")
	storyboardCapture(t, h, "06-3-launch-readiness")
	h.expect("native capability unrun", h.send("\x1b[F"), "REQUIRED-UNRUN")
	h.send("\x1b")
	h.expect("report back", 0, "Runs / diagnostics")
	h.send("\x1b")
	h.expect("scope preserved", 0, "forge not searched")
	h.exit("q")
}
