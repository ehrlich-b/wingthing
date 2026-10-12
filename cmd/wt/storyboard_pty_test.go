package main

// Storyboard acceptance is intentionally red for absent product requirements.
// Every journey uses the built CLI and Northstar's real kernel PTY/VT harness.
// Fixture SSH and providers never establish physical iPhone or vendor readiness.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

var storyboardBuild struct {
	sync.Once
	binary string
	err    error
}

func storyboardBinary(t *testing.T, f *northstarFixture) string {
	t.Helper()
	storyboardBuild.Do(func() {
		storyboardBuild.binary = filepath.Join(f.repo, ".scratch", "storyboard-wt")
		cmd := exec.CommandContext(t.Context(), "nice", "-n", "15", "go", "build", "-buildvcs=false", "-o", storyboardBuild.binary, "./cmd/wt")
		cmd.Dir = f.repo
		cmd.Env = append(os.Environ(), "GOFLAGS=-p=4", "GOCACHE="+filepath.Join(f.repo, ".scratch/northstar/go-build"), "GOMODCACHE="+filepath.Join(f.repo, ".scratch/northstar/go-mod"))
		if out, err := cmd.CombinedOutput(); err != nil {
			storyboardBuild.err = fmt.Errorf("build: %w\n%s", err, out)
		}
	})
	if storyboardBuild.err != nil {
		t.Fatal(storyboardBuild.err)
	}
	alias := filepath.Join(f.bin, "wt")
	if err := os.Symlink(storyboardBuild.binary, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func storyboardFixture(t *testing.T) (*northstarFixture, string) {
	t.Helper()
	f := northstarNewFixture(t)
	f.evidence = filepath.Join(f.repo, ".scratch/storyboard-evidence", strings.ReplaceAll(t.Name(), "/", "-"))
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	binary := storyboardBinary(t, f)
	ssh, err := os.ReadFile(filepath.Join(f.bin, "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.bin, "ssh"), strings.Replace(string(ssh), "-T|-t)", "-T|-t|--)", 1), 0700)
	return f, binary
}

func storyboardNoWork(t *testing.T, f *northstarFixture) {
	t.Helper()
	for _, state := range []string{f.local, f.remote} {
		entries, err := os.ReadDir(filepath.Join(state, "eggs"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("unexpected provider/session admission in %s: %v", state, entries)
		}
		if _, err := os.Stat(filepath.Join(state, "wing.pid")); !os.IsNotExist(err) {
			t.Errorf("unexpected daemon: %s (%v)", state, err)
		}
	}
}

// Exiting commands need a PTY too. Wait for exit and drain the actual bytes;
// an idle screen is never used as evidence that the command has completed.
func storyboardCommand(t *testing.T, f *northstarFixture, binary string, args ...string) (string, error) {
	t.Helper()
	h := northstarStartPTY(t, f, binary, 160, 60, args...)
	var exitErr error
	select {
	case exitErr = <-h.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("command did not exit: %v", args)
	}
	if err := h.slave.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("PTY did not drain")
	}
	h.mu.Lock()
	out := h.transcript.String()
	fmt.Fprintf(&h.screens, "\n--- command %q; exit=%v ---\n%s\n", args, exitErr, h.emu.String())
	h.mu.Unlock()
	t.Logf("command %q exit=%v", args, exitErr)
	return out, exitErr
}

func storyboardContains(t *testing.T, output string, wants ...string) {
	t.Helper()
	flat := strings.ReplaceAll(output, "\r", "")
	for _, want := range wants {
		if !strings.Contains(flat, want) {
			t.Errorf("expected screen contains %q; actual:\n%s", want, output)
		}
	}
}

func storyboardEmpty(t *testing.T) (*northstarFixture, string, *northstarPTY) {
	t.Helper()
	f, binary := storyboardFixture(t)
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storyboardNoWork(t, f) })
	h := northstarStartPTY(t, f, binary, 140, 40)
	h.expect("empty navigator", 0, "wt |", "(no sessions)", "n new", "? help")
	return f, binary, h
}

// Run the production installer against a hermetic release transport. The
// downloaded artifact is our real Linux binary and a matching SHA256SUMS.
func storyboardInstaller(t *testing.T, failed bool) (*northstarFixture, string, string, error) {
	t.Helper()
	f, binary := storyboardFixture(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	northstarWrite(t, filepath.Join(f.bin, "curl"), `#!/usr/bin/python3
import os,sys,shutil
args=sys.argv[1:]; url=args[-1]
with open(os.environ['INSTALL_REQUESTS'],'a') as f: f.write(url+'\n')
if os.environ['INSTALL_FAIL']=='1':
 print('fixture: release download unavailable',file=sys.stderr);sys.exit(22)
if url.endswith('/latest'): print('{"tag_name":"storyboard-fixture"}');sys.exit(0)
p=args[args.index('-o')+1]
if url.endswith('/SHA256SUMS'):
 open(p,'w').write(os.environ['INSTALL_SUM']+'  wt-linux-'+os.environ['INSTALL_ARCH']+'\n')
else: shutil.copyfile(os.environ['INSTALL_BINARY'],p)
`, 0700)
	install := filepath.Join(f.home, "installed/bin")
	arch := "amd64"
	if strings.Contains(string(mustStoryboardUname(t)), "aarch64") {
		arch = "arm64"
	}
	flag := "0"
	if failed {
		flag = "1"
	}
	f.env = append(f.env, "WT_INSTALL_DIR="+install, "INSTALL_BINARY="+storyboardBuild.binary, "INSTALL_SUM="+sum, "INSTALL_ARCH="+arch, "INSTALL_FAIL="+flag, "INSTALL_REQUESTS="+filepath.Join(f.home, "installer-requests"))
	out, exitErr := storyboardCommand(t, f, "/bin/sh", filepath.Join(f.repo, "scripts/install.sh"))
	return f, install, out, exitErr
}
func mustStoryboardUname(t *testing.T) []byte {
	t.Helper()
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStoryboardInstallCard0(t *testing.T) {
	f, install, out, err := storyboardInstaller(t, false)
	if err != nil {
		t.Fatalf("installer: %v\n%s", err, out)
	}
	storyboardContains(t, out, "installed wt storyboard-fixture", "add to your PATH:")
	installed := filepath.Join(install, "wt")
	got, e := os.ReadFile(installed)
	want, _ := os.ReadFile(storyboardBuild.binary)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatal("installed artifact differs from built Linux wt")
	}
	h := northstarStartPTY(t, f, installed, 140, 40)
	h.expect("installed wt enters navigator", 0, "wt |", "(no sessions)")
	h.exit("q")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard1(t *testing.T) {
	f, _, h := storyboardEmpty(t)
	h.expect("first-run connection and terms", 0, "Local only:", "phones paired on your network", "wing", "roost")
	h.exit("q")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard4(t *testing.T) {
	f, _, h := storyboardEmpty(t)
	h.expect("local no-agent detection", 0, "No supported agent CLIs on PATH", "Choose Claude Code or Codex", "Skip")
	h.exit("q")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard5(t *testing.T) {
	f, _, h := storyboardEmpty(t)
	h.expect("installer selection", h.send("n"), "Choose Claude Code or Codex")
	h.expect("review installer", h.send("\r"), "curl -fsSL https://claude.ai/install.sh | bash", "consent", "Esc")
	h.exit("\x1bq")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard6(t *testing.T) {
	f, _, h := storyboardEmpty(t)
	northstarWrite(t, filepath.Join(f.bin, "claude"), "#!/bin/sh\nprintf 'claude-version-fixture\\n'\nprintf '%s\\n' \"$*\" >> \"$HOME/provider-invocations\"\n", 0700)
	h.expect("provider verification and native auth handoff", h.send("n"), "Verify selected provider", "Continue to the selected agent")
	h.exit("\x1bq")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard7(t *testing.T) {
	f, _, h := storyboardEmpty(t)
	h.expect("installer failure recovery route", h.send("n"), "Choose Claude Code or Codex")
	h.expect("failed-stage recovery", h.send("\r"), "Retry with fresh consent", "Rescan PATH", "Back")
	h.exit("\x1bq")
	storyboardNoWork(t, f)
}
func TestStoryboardInstallCard2(t *testing.T) {
	f, install, out, err := storyboardInstaller(t, true)
	if err == nil {
		t.Fatal("failed transport reported installer success")
	}
	storyboardContains(t, out, "error: could not find latest release")
	if _, err := os.Stat(filepath.Join(install, "wt")); !os.IsNotExist(err) {
		t.Fatal("failed install wrote a binary")
	}
	storyboardNoWork(t, f)
	_, _, out, err = storyboardInstaller(t, false)
	if err != nil {
		t.Fatal(err)
	}
	storyboardContains(t, out, "add to your PATH:", "export PATH=")
}
func TestStoryboardInstallCard3(t *testing.T) {
	f, binary := storyboardFixture(t)
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	h := northstarStartPTY(t, f, binary, 140, 40)
	h.expect("empty offline navigator", 0, "wt |", "(no sessions)")
	northstarWrite(t, filepath.Join(f.bin, "curl"), "#!/bin/sh\ntouch \"$HOME/unexpected-network\"\nexit 22\n", 0700)
	h.exit("q")
	egg := northstarStandaloneEgg(t, f, f.local, "offline-local", "offline-local", "working", "LOCAL_OFFLINE_PROVIDER")
	h = northstarStartPTY(t, f, binary, 140, 40)
	h.expect("offline local inventory", 0, "offline-local", "W offline-local")
	h.expect("offline local attach", h.send("\r"), "LOCAL_OFFLINE_PROVIDER")
	h.expect("offline detach", h.send("\x02q"), "offline-local", "Enter attach")
	h.exit("q")
	if egg.killed.Load() != 0 {
		t.Fatal("local inventory/attach stopped provider")
	}
	if _, err := os.Stat(filepath.Join(f.home, "unexpected-network")); !os.IsNotExist(err) {
		t.Fatal("navigator used download transport")
	}
	if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
		t.Fatal("navigator started a daemon")
	}
}

func storyboardCold(t *testing.T) (*northstarFixture, string) {
	t.Helper()
	f := coldFixture(t)
	f.evidence = filepath.Join(f.repo, ".scratch/storyboard-evidence", t.Name())
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	storyboardWriteLinuxPolicy(t, f, f.local)
	alias := storyboardBinary(t, f)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	binary := coldInstallBinary(t, f, storyboardBuild.binary)
	coldCleanup(t, f, binary)
	storyboardStateEvidence(t, f, f.local)
	coldRelayGuard(t, f)
	coldPolicyGuard(t, f)
	return f, binary
}
func TestStoryboardStartCard0(t *testing.T) {
	f, binary := storyboardCold(t)
	h := northstarStartPTY(t, f, binary, 140, 40, "claude", "--name", "pairing")
	h.expect("cold parent ready", 0, "COLD_CLAUDE_READY")
	coldOneDaemon(t, f)
	ids := coldSessionIDs(t, f)
	if len(ids) != 1 {
		t.Fatalf("one parent required: %v", ids)
	}
	meta := eggclient.ReadEggMetaValues(filepath.Join(f.local, "eggs", ids[0]))
	if meta["cwd"] != f.work || meta["agent"] != "claude" {
		t.Fatalf("metadata: %v", meta)
	}
	h.exit("\x02q")
	h = northstarStartPTY(t, f, binary, 140, 40, "attach", ids[0])
	h.expect("same admitted parent", 0, "COLD_CLAUDE_READY")
	h.exit("\x02q")
}
func TestStoryboardStartCard1(t *testing.T) {
	f, binary := storyboardCold(t)
	chosen := filepath.Join(f.work, "explicit cwd")
	if err := os.Mkdir(chosen, 0700); err != nil {
		t.Fatal(err)
	}
	h := northstarStartPTY(t, f, binary, 140, 40, "claude", "--cwd", chosen, "--name", "chosen")
	h.expect("explicit cwd parent", 0, "COLD_CLAUDE_READY")
	ids := coldSessionIDs(t, f)
	if len(ids) != 1 {
		t.Fatalf("admissions %v", ids)
	}
	if meta := eggclient.ReadEggMetaValues(filepath.Join(f.local, "eggs", ids[0])); meta["cwd"] != chosen {
		t.Fatalf("wrong receiving cwd: %v", meta)
	}
	h.exit("\x02q")
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	out, err := storyboardCommand(t, f, binary, "claude", "--cwd", outside)
	if err == nil {
		t.Fatal("unauthorized cwd admitted")
	}
	storyboardContains(t, out, "path")
	if len(coldSessionIDs(t, f)) != 1 {
		t.Fatal("denial created another session")
	}
}
func TestStoryboardStartCard2(t *testing.T) { storyboardScopedNew(t) }
func TestStoryboardStartCard3(t *testing.T) {
	f, binary := storyboardCold(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	// Native protocol fixture from internal/testprovider; startup must remain a
	// generic interactive egg, not an invented wt codex command.
	northstarWrite(t, filepath.Join(f.bin, "codex"), "#!"+python+"\n"+testprovider.Codex, 0700)
	h := northstarStartPTY(t, f, binary, 140, 40, "egg", "codex", "--cwd", f.work, "--name", "review", "--", "-m", "fixture-model")
	h.expect("interactive Codex native startup", 0, "OpenAI Codex", "Ask Codex to do anything")
	ids := coldSessionIDs(t, f)
	if len(ids) != 1 {
		t.Fatalf("one generic egg: %v", ids)
	}
	if meta := eggclient.ReadEggMetaValues(filepath.Join(f.local, "eggs", ids[0])); meta["agent"] != "codex" || meta["cwd"] != f.work {
		t.Fatalf("metadata: %v", meta)
	}
	h.exit("\x02q")
}
func TestStoryboardStartCard4(t *testing.T) { storyboardLaunchFailure(t) }
func TestStoryboardStartCard5(t *testing.T) {
	t.Run("two-requested-parents-one-daemon", func(t *testing.T) {
		f, binary := storyboardCold(t)
		first := northstarStartPTY(t, f, binary, 140, 40, "claude", "--name", "parallel-a")
		second := northstarStartPTY(t, f, binary, 140, 40, "claude", "--name", "parallel-b")
		first.expect("first concurrent parent", 0, "COLD_CLAUDE_READY")
		second.expect("second concurrent parent", 0, "COLD_CLAUDE_READY")
		pid := coldOneDaemon(t, f)
		if len(coldSessionIDs(t, f)) != 2 {
			t.Fatal("two explicit launches must create two parents")
		}
		first.exit("\x02q")
		second.exit("\x02q")
		if !bytes.Equal(pid, coldOneDaemon(t, f)) {
			t.Fatal("detach replaced main wing")
		}
	})
	t.Run("canceled-waiter-does-not-kill-survivor", func(t *testing.T) {
		f, binary := storyboardCold(t)
		lock, err := daemonctl.AcquireDaemonLifecycleLockAt(filepath.Join(f.local, "daemon.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		first := northstarStartPTY(t, f, binary, 140, 40, "claude", "--name", "canceled")
		second := northstarStartPTY(t, f, binary, 140, 40, "claude", "--name", "survivor")
		first.expect("first waiting on lifecycle lock", 0, "Starting local Claude...")
		second.expect("second waiting on same lock", 0, "Starting local Claude...")
		if err := first.cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		first.expect("canceled waiting caller", 0, "context canceled")
		coldPTYExit(t, first, true)
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		second.expect("accepted survivor provider", 0, "COLD_CLAUDE_READY")
		pid := coldOneDaemon(t, f)
		if len(coldSessionIDs(t, f)) != 1 {
			t.Fatal("canceled caller admitted work")
		}
		second.exit("\x02q")
		if !bytes.Equal(pid, coldOneDaemon(t, f)) {
			t.Fatal("canceling a waiter killed/replaced shared daemon")
		}
	})
}

func storyboardFleet(t *testing.T) (*northstarFixture, string, *northstarEgg, *northstarEgg, *northstarPTY) {
	t.Helper()
	f, binary := storyboardFixture(t)
	local := northstarStandaloneEgg(t, f, f.local, "same", "coordinator", "working", "LOCAL_EXACT")
	remote := northstarStandaloneEgg(t, f, f.remote, "same", "coordinator", "needs_input", "FORGE_EXACT")
	h := northstarStartPTY(t, f, binary, 140, 40)
	h.expect("grouped machines", 0, "local", "forge", "coordinator", "work", "W coordinator", "B coordinator")
	return f, binary, local, remote, h
}
func TestStoryboardNavigatorCard0(t *testing.T) {
	_, _, local, remote, h := storyboardFleet(t)
	h.waitScreen("machine filter", h.send("/forge\r"), func(s string) bool {
		return strings.Contains(s, "> B coordinator") && !strings.Contains(s, "W coordinator")
	})
	h.expect("attention filter", h.send("!"), "B coordinator")
	h.exit("q")
	if local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("inventory mutated provider state")
	}
}
func TestStoryboardNavigatorCard1(t *testing.T) {
	_, _, local, remote, h := storyboardFleet(t)
	h.expect("selected remote", h.send("/forge\r"), "> B coordinator")
	h.expect("exact provider attach", h.send("\r"), "FORGE_EXACT")
	select {
	case id := <-remote.attached:
		if id != "same" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("missing exact attachment")
	}
	h.expect("detach keeps selection", h.send("\x02q"), "> B coordinator", "Enter attach")
	h.exit("q")
	if local.killed.Load() != 0 || remote.killed.Load() != 0 || len(local.attached) != 0 {
		t.Fatal("detach changed owning process or route")
	}
}
func TestStoryboardNavigatorCard2(t *testing.T) {
	TestNorthstarPTYAttention(t)
	t.Run("durable-unread-cursor", func(t *testing.T) {
		f, binary := storyboardFixture(t)
		if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
			t.Fatal(err)
		}
		northstarStandaloneEgg(t, f, f.local, "unread", "unread-parent", "working", "UNREAD_PROVIDER")
		provider := northstarAttentionHooks(t, f, f.local, "unread", false)
		provider.emit(t, "SessionStart")
		provider.emit(t, "UserPromptSubmit")
		first := northstarStartPTY(t, f, binary, 140, 40)
		first.expect("viewer starts before new output", 0, "W unread-parent", "U:0")
		provider.emit(t, "Stop")
		first.expect("new output unread", first.send("R"), "I* unread-parent", "U:1")
		first.exit("q")
		second := northstarStartPTY(t, f, binary, 140, 40)
		second.expect("same viewer durable unread after reopen", 0, "I* unread-parent", "U:1")
		second.exit("q")
	})
}
func TestStoryboardNavigatorCard3(t *testing.T) {
	f, binary := storyboardFixture(t)
	h := northstarStartPTY(t, f, binary, 140, 40)
	h.expect("native attachment entrypoint", 0, "wt |", "Enter attach")
	h.exit("q")
	storyboardNativeMouse(t)
}
func TestStoryboardNavigatorCard4(t *testing.T) { TestNorthstarPTYRecovery(t) }
func storyboardOffline(t *testing.T) {
	f, _, local, remote, h := storyboardFleet(t)
	northstarWrite(t, filepath.Join(f.home, "forge-offline"), "offline", 0600)
	h.expect("stale remote inventory", h.send("R"), "forge  offline", "last success", "stale", "coordinator")
	h.expect("local remains available", h.send("/local\r"), "> W coordinator")
	h.expect("local attach while forge offline", h.send("\r"), "LOCAL_EXACT")
	h.expect("local detach", h.send("\x02q"), "> W coordinator")
	h.expect("select stale exact route", h.send("/\x15forge\r"), "stale", "B coordinator")
	h.expect("stale attach denied", h.send("\r"), "cannot validate")
	h.exit("q")
	if len(remote.attached) != 0 || local.killed.Load() != 0 || remote.killed.Load() != 0 {
		t.Fatal("offline route mutated/fell back")
	}
}
func TestStoryboardNavigatorCard5(t *testing.T) { storyboardOffline(t) }
func TestStoryboardNavigatorCard6(t *testing.T) {
	_, _, local, remote, h := storyboardFleet(t)
	h.expect("qualified selection", h.send("/forge\r"), "> B coordinator")
	h.expect("full details", h.send("i"), "forge:same", "Cwd:", "SSH: ns-forge")
	h.expect("return from details", h.send("\x1b"), "> B coordinator")
	for _, cancel := range []string{"\r", "\x1b"} {
		h.expect("exact stop confirmation", h.send("x"), "Stop forge:same (coordinator)? [y/N]")
		h.expect("cancel stop", h.send(cancel), "> B coordinator", "Enter attach")
		if remote.killed.Load() != 0 {
			t.Fatal("Enter/Escape stopped session")
		}
	}
	h.expect("confirmed stop target", h.send("x"), "Stop forge:same")
	rev := h.send("y")
	northstarInventoryWait(t, "exact remote stop acknowledgement", func() bool { return remote.killed.Load() >= 1 })
	h.expect("remote removed", rev, "(no matches)")
	h.exit("q")
	if remote.killed.Load() != 1 || local.killed.Load() != 0 {
		t.Fatal("wrong exact stop target")
	}
}

func TestStoryboardMachinesCard0(t *testing.T) {
	f, binary := storyboardFixture(t)
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err := storyboardCommand(t, f, binary, "remote", "add", "forge", "ns-forge", "--wt-binary", binary, "--wingthing-dir", f.remote)
	if err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	out, err = storyboardCommand(t, f, binary, "remote", "ls")
	if err != nil {
		t.Fatal(err)
	}
	storyboardContains(t, out, "forge", "ns-forge")
	profiles, err := config.LoadRemotes(f.local)
	if err != nil || profiles["forge"].SSHTarget != "ns-forge" || profiles["forge"].WingthingDir != f.remote {
		t.Fatalf("saved profile: %v %v", profiles, err)
	}
	storyboardNoWork(t, f)
}
func TestStoryboardMachinesCard1(t *testing.T) {
	f, binary := storyboardFixture(t)
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(f.remote, &config.WingConfig{WingID: "storyboard-forge-wing"}); err != nil {
		t.Fatal(err)
	}
	server, err := controlsocket.Listen(t.Context(), f.remote, "storyboard-forge-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{}}
		}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	out, err := storyboardCommand(t, f, binary, "mcp", "connect", "add", "forge", "--ssh", "ns-forge", "--wingthing-dir", f.remote, "--wt-binary", binary)
	if err != nil {
		t.Fatalf("verified connect: %v\n%s", err, out)
	}
	out, err = storyboardCommand(t, f, binary, "mcp", "connect", "ls")
	if err != nil {
		t.Fatal(err)
	}
	storyboardContains(t, out, "forge", "ns-forge")
	profiles, err := config.LoadRemotes(f.local)
	p := profiles["forge"]
	if err != nil || p.WingID != "storyboard-forge-wing" || p.WingthingDir != f.remote || p.ControlSocket == "" || p.ControlVersion == "" {
		t.Fatalf("missing pin: %+v %v", p, err)
	}
}
func TestStoryboardMachinesCard2(t *testing.T) { TestNorthstarPTYRemoteAttach(t) }
func TestStoryboardMachinesCard3(t *testing.T) { storyboardOffline(t) }
func TestStoryboardMachinesCard4(t *testing.T) {
	for _, reason := range []string{"REMOTE HOST IDENTIFICATION HAS CHANGED!", "unknown flag: --expected-channel"} {
		t.Run(reason, func(t *testing.T) {
			f, binary, local, remote, h := storyboardFleet(t)
			h.exit("q")
			northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nprintf '%s\\n' '"+reason+"' >&2\nexit 255\n", 0700)
			out, err := storyboardCommand(t, f, binary, "attach", "forge:same")
			if err == nil {
				t.Fatal("unverified route allowed")
			}
			storyboardContains(t, out, reason, "session outcome unknown")
			if len(local.attached) != 0 || len(remote.attached) != 0 || local.killed.Load() != 0 || remote.killed.Load() != 0 {
				t.Fatal("identity/version failure crossed route")
			}
		})
	}
}

// Phone-only assertions have an executable prerequisite. Stop at its observed
// failure, rather than fabricate downstream iOS screens/crypto receipts on Linux.
// The map spells out each unexercised downstream screen and state obligation.
func storyboardPhone(t *testing.T, card int) {
	f, binary := storyboardFixture(t)
	northstarStandaloneEgg(t, f, f.local, "phone-session", "pairing", "needs_input", "PHONE_SESSION_UNCHANGED")
	storyboardPhoneMainWing(t, f, binary)
	command := []string{"pair"}
	prerequisite := []string{"Pair a phone", "5:00", "Waiting for scan"}
	if card == 6 {
		command = []string{"devices"}
		prerequisite = []string{"NAME", "PAIRED AT", "LAST SEEN"}
	}
	if card == 11 {
		command = []string{"devices", "revoke", "Bryan's iPhone"}
		prerequisite = []string{"device", "not paired"}
	}
	out, err := storyboardPhoneCommand(t, f, binary, command...)
	entries, _ := os.ReadDir(filepath.Join(f.remote, "eggs"))
	if len(entries) != 0 {
		t.Fatal("phone prerequisite admitted work on another wing")
	}
	t.Logf("card 05/#%d prerequisite: %v; downstream physical-device assertions are NOT exercised", card+1, command)
	if err != nil && strings.Contains(out, "unknown command") {
		t.Fatalf("phone card blocked: required wt %s unavailable; expected prerequisite screen %q; actual:\n%s", command[0], prerequisite, out)
	}
	storyboardContains(t, out, prerequisite...)
	t.Fatal("REQUIRED-UNRUN: invitation prerequisite alone cannot prove this card's phone screen, device Keychain, encrypted RPC, route or revocation assertions; physical iPhone evidence required")
}
func TestStoryboardPhoneCard0(t *testing.T)  { storyboardPhone(t, 0) }
func TestStoryboardPhoneCard1(t *testing.T)  { storyboardPhone(t, 1) }
func TestStoryboardPhoneCard2(t *testing.T)  { storyboardPhone(t, 2) }
func TestStoryboardPhoneCard3(t *testing.T)  { storyboardPhone(t, 3) }
func TestStoryboardPhoneCard4(t *testing.T)  { storyboardPhone(t, 4) }
func TestStoryboardPhoneCard5(t *testing.T)  { storyboardPhone(t, 5) }
func TestStoryboardPhoneCard6(t *testing.T)  { storyboardPhone(t, 6) }
func TestStoryboardPhoneCard7(t *testing.T)  { storyboardPhone(t, 7) }
func TestStoryboardPhoneCard8(t *testing.T)  { storyboardPhone(t, 8) }
func TestStoryboardPhoneCard9(t *testing.T)  { storyboardPhone(t, 9) }
func TestStoryboardPhoneCard10(t *testing.T) { storyboardPhone(t, 10) }
func TestStoryboardPhoneCard11(t *testing.T) { storyboardPhone(t, 11) }
func TestStoryboardPhoneCard12(t *testing.T) { storyboardPhone(t, 12) }
func TestStoryboardPhoneCard13(t *testing.T) { storyboardPhone(t, 13) }
func TestStoryboardPhoneCard14(t *testing.T) { storyboardPhone(t, 14) }
func TestStoryboardPhoneCard15(t *testing.T) { storyboardPhone(t, 15) }
func TestStoryboardPhoneCard16(t *testing.T) { storyboardPhone(t, 16) }
func TestStoryboardPhoneCard17(t *testing.T) { storyboardPhone(t, 17) }

func TestStoryboardRunsCard0(t *testing.T) {
	f, binary := storyboardFixture(t)
	// A deterministic stream-json provider still crosses the real CLI/task
	// runner and native Linux sandbox. No --unsandboxed relaxation is used.
	northstarWrite(t, filepath.Join(f.bin, "claude"), `#!/bin/sh
if [ "$1" = --version ]; then printf 'claude fixture 1\n'; exit 0; fi
printf '{"type":"assistant","message":{"content":[{"type":"text","text":"STORYBOARD_LEGACY_RESULT"}]}}\n'
printf '{"type":"result","input_tokens":1,"output_tokens":1}\n'
`, 0700)
	policy, err := storyboardLinuxPolicy(f).YAML()
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(f.work, "egg.yaml")
	northstarWrite(t, selected, policy, 0600)
	out, err := storyboardCommand(t, f, binary, "run", "Read the tests and report failures", "--agent", "claude", "--config", selected)
	if err != nil {
		t.Fatalf("legacy task did not complete: %v\n%s", err, out)
	}
	storyboardContains(t, out, "submitted:", "STORYBOARD_LEGACY_RESULT")
	s, err := store.Open(filepath.Join(f.local, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tasks, err := s.ListRecent(10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("legacy tasks: %v %v", tasks, err)
	}
	task := tasks[0]
	if task.Status != "done" || task.CWD != f.work || task.Agent != "claude" || task.Isolation == "privileged" || task.Output == nil || !strings.Contains(*task.Output, "STORYBOARD_LEGACY_RESULT") {
		t.Fatalf("legacy receipt: %+v", task)
	}
	for _, args := range [][]string{{"timeline"}, {"status"}} {
		out, err := storyboardCommand(t, f, binary, args...)
		if err != nil {
			t.Fatal(err)
		}
		if args[0] == "timeline" {
			storyboardContains(t, out, "done")
		} else {
			storyboardContains(t, out, "pending: 0", "running: 0", "tokens:")
		}
	}
}
func TestStoryboardRunsCard1(t *testing.T) {
	// This established scoped-parent journey verifies wing/run/session receipts,
	// the allowed remote cwd, native fixture completion, and recovery by a fresh
	// client after disconnect. All its existing assertions remain intact.
	storyboardScopedNew(t)
}
func TestStoryboardRunsCard2(t *testing.T) {
	f, binary := storyboardFixture(t)
	northstarWrite(t, filepath.Join(f.bin, "codex"), "#!/bin/sh\ntouch \"$HOME/doctor-must-not-invoke\"\nexit 99\n", 0700)
	providerHome := filepath.Join(f.work, "provider-home")
	if err := os.Mkdir(providerHome, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(providerHome, "auth.json"), "PRIVATE_DOCTOR_CREDENTIAL", 0600)
	before := inspectTree(t, f.work)
	out, err := storyboardCommand(t, f, binary, "doctor", "launch-plan", "--route", "agent_run", "--agent", "codex", "--cwd", f.work, "--home", providerHome, "--allowed-home-root", f.work, "--executable", filepath.Join(f.bin, "codex"))
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	storyboardContains(t, out, "unverified", "REQUIRED-UNRUN")
	if strings.Contains(out, "PRIVATE_DOCTOR_CREDENTIAL") {
		t.Fatal("doctor exposed credential contents")
	}
	if !reflect.DeepEqual(before, inspectTree(t, f.work)) {
		t.Fatal("doctor modified workspace")
	}
	if _, err := os.Stat(filepath.Join(f.home, "doctor-must-not-invoke")); !os.IsNotExist(err) {
		t.Fatal("doctor invoked provider")
	}
	storyboardNoWork(t, f)
}
func TestStoryboardRunsCard3(t *testing.T) {
	f, _, _, _, h := storyboardFleet(t)
	h.expect("selected scope details", h.send("i"), "Cwd: "+f.work, "Isolation: fixture")
	h.expect("effective policy review", 0, "Workspace", "Provider state", "Network", "Denied")
	h.exit("\x1bq")
}
func TestStoryboardRunsCard4(t *testing.T) {
	f, binary := storyboardFixture(t)
	snapshot := filepath.Join(f.work, "captured-state")
	if err := os.CopyFS(snapshot, os.DirFS(filepath.Join(f.repo, "internal/runinspect/testdata/orphan"))); err != nil {
		t.Fatal(err)
	}
	before := inspectTree(t, snapshot)
	out, err := storyboardCommand(t, f, binary, "run-inspect", "--snapshot", snapshot, "--json")
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid PTY JSON: %v", err)
	}
	storyboardContains(t, out, "completed_with_output", "terminal_receipt_not_in_wing", "evidence_state")
	if len(report["runs"].([]any)) != 1 {
		t.Fatal("snapshot must contain exactly one run")
	}
	if strings.Contains(out, "ORIGINAL-FIXTURE-PROVIDER-SENTINEL") {
		t.Fatal("default inspector exposed output")
	}
	if !reflect.DeepEqual(before, inspectTree(t, snapshot)) {
		t.Fatal("inspector modified snapshot")
	}
	// A modified completed copy must not acquire authority from ambient state.
	northstarWrite(t, filepath.Join(snapshot, "unexpected"), "tampered", 0600)
	out, err = storyboardCommand(t, f, binary, "run-inspect", "--snapshot", snapshot, "--json")
	if err == nil {
		t.Fatal("modified snapshot accepted")
	}
	storyboardContains(t, out, "snapshot")
	storyboardNoWork(t, f)
}
func TestStoryboardRunsCard5(t *testing.T) { storyboardLaunchFailure(t) }
func TestStoryboardRunsCard6(t *testing.T) { TestNorthstarPTYRecovery(t) }

// Linux deny-write is a recursive read-only bind mount. Darwin's literal
// ancestor guards cannot be reused for it. Pin ancestors with deny-rename and
// mask state/mailbox authority explicitly, retaining the native negative probes.
func storyboardLinuxPolicy(f *northstarFixture) *egg.EggConfig {
	policy := egg.DefaultEggConfig()
	policy.Base.Name = "none"
	for dir := f.home; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		policy.FS = append(policy.FS, "deny-rename:"+dir)
	}
	for _, path := range []string{f.local, f.remote} {
		if path == f.home {
			path = filepath.Join(path, "eggs")
		}
		policy.FS = append(policy.FS, "deny:"+path)
	}
	policy.FS = append(policy.FS, "deny-write:"+filepath.Join(f.home, "mailbox"))
	return policy
}
func storyboardWriteLinuxPolicy(t *testing.T, f *northstarFixture, state string) {
	t.Helper()
	data, err := storyboardLinuxPolicy(f).YAML()
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(state, "egg.yaml"), data, 0600)
}

func storyboardStateEvidence(t *testing.T, f *northstarFixture, state string) {
	t.Helper()
	t.Cleanup(func() {
		target := filepath.Join(f.repo, ".scratch/storyboard-evidence", strings.ReplaceAll(t.Name(), "/", "-"), "runtime-logs")
		for _, root := range []string{state, filepath.Join(f.root, "tmp-real")} {
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
					return nil
				}
				if !strings.HasSuffix(path, ".log") && !strings.HasSuffix(path, ".jsonl") && !strings.HasSuffix(path, "failed.json") {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				relative, _ := filepath.Rel(root, path)
				dst := filepath.Join(target, filepath.Base(root), relative)
				if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
					return err
				}
				northstarWrite(t, dst, string(data), 0600)
				if t.Failed() {
					t.Logf("fixture runtime diagnostic %s: %s", relative, data)
				}
				return nil
			})
		}
		for _, name := range []string{"wing.log", "wing.yaml", "egg.yaml"} {
			data, err := os.ReadFile(filepath.Join(state, name))
			if err != nil {
				continue
			}
			target := filepath.Join(f.repo, ".scratch/storyboard-evidence", strings.ReplaceAll(t.Name(), "/", "-"))
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Error(err)
				continue
			}
			northstarWrite(t, filepath.Join(target, name), string(data), 0600)
			if t.Failed() && name == "wing.log" {
				t.Logf("fixture wing diagnostic: %s", data)
			}
		}
	})
}
