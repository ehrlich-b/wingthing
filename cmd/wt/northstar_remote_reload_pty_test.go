package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "northstar-reload-ssh" {
		os.Exit(northstarReloadSSH(os.Args[2:]))
	}
}

func northstarReloadSSH(args []string) int {
	if len(args) == 5 && args[3] == "ns-delayed" {
		if args[0] != "-T" || args[1] != "-o" || args[2] != "BatchMode=yes" {
			return 90
		}
		mailbox := os.Getenv("NORTHSTAR_RELOAD_MAILBOX")
		wake := make(chan os.Signal, 1)
		signal.Notify(wake, syscall.SIGTERM)
		defer signal.Stop(wake)
		record, _ := json.Marshal(northstarSSHRecord{Target: args[3], Mode: args[0], Batch: true, Command: args[4]})
		if err := os.WriteFile(filepath.Join(mailbox, "delayed.json"), record, 0600); err != nil {
			return 91
		}
		if err := os.WriteFile(filepath.Join(mailbox, "started"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return 92
		}
		<-wake
		// Deliberately finish with old successful data during cancellation. The
		// unit race also delivers this result after a replacement query starts.
		_ = os.WriteFile(filepath.Join(mailbox, "cancelled"), []byte("cancelled"), 0600)
		_ = json.NewEncoder(os.Stdout).Encode(eggclient.RemoteSessionInventory{Version: "fixture", ContractVersion: eggclient.RemoteSessionContractVersion, Sessions: []eggclient.LocalSession{{ID: "late", Name: "retired-host-result"}}})
		return 0
	}
	return northstarRemoteSSH(args)
}

func TestNorthstarPTYRemoteReload(t *testing.T) {
	started := time.Now()
	f := northstarNewFixture(t) // Includes a real parent and test-created TMPDIR alias.
	f.evidence = filepath.Join(f.repo, ".scratch", "evidence", "11", "pty")
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	binary := northstarBuild(t, f, "stable")
	canonical, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || canonical != filepath.Join(f.root, "tmp-real") || !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("CLI did not traverse the TMPDIR parent alias")
	}
	mailbox := filepath.Join(f.home, "mailbox")
	mirror := filepath.Join(f.home, "m")
	if err := os.Mkdir(mirror, 0700); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{f.local, f.remote, mirror} {
		if !strings.HasPrefix(state, f.home+"/") || strings.HasPrefix(state, f.work+"/") {
			t.Fatal("unprotected fixture state")
		}
		socket, err := controlsocket.Path(state)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, socket) // Asserts lexical and canonical byte lengths before bind.
	}
	if !strings.HasPrefix(mailbox, f.home+"/") || strings.HasPrefix(mailbox, f.work+"/") {
		t.Fatal("unprotected fixture mailbox")
	}
	custom := filepath.Join(f.bin, "reload custom wt")
	if err := os.Symlink(binary, custom); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(mailbox, "ssh.jsonl")
	f.env = append(f.env, "NORTHSTAR_REMOTE_SSH_LOG="+log, "NORTHSTAR_RELOAD_MAILBOX="+mailbox, "GOTMPDIR="+f.tmp)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nexec "+remotepkg.ShellQuote(exe)+" northstar-reload-ssh \"$@\"\n", 0700)
	local := northstarStandaloneEgg(t, f, f.local, "same", "healthy-local", "working", "RELOAD_LOCAL_READY")
	old := northstarStandaloneEgg(t, f, f.remote, "same", "old-profile", "working", "RELOAD_OLD_READY")
	replacement := northstarStandaloneEgg(t, f, mirror, "same", "replacement-profile", "working", "RELOAD_NEW_READY")
	profiles := map[string]config.Remote{}
	save := func() {
		t.Helper()
		if err := config.SaveRemotes(f.local, profiles); err != nil {
			t.Fatal(err)
		}
	}
	cli := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Env, cmd.Dir = f.env, f.work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("registry CLI: %v %s", err, out)
		}
	}
	save()
	h := northstarStartPTY(t, f, binary, 160, 40)
	h.expect("healthy local before machine changes", 0, "healthy-local", "1 machines")
	cli("remote", "add", "empty", "ns-mirror", "--wt-binary", custom, "--wingthing-dir", filepath.Join(f.home, "e"))
	h.expect("new empty machine without restart or R", h.send("m"), "empty", "Machines")
	h.expect("back from live picker", h.send("\x1b"), "empty", "(no sessions)")
	profiles["empty"] = config.Remote{SSHTarget: "ns-mirror", WTBinary: custom, WingthingDir: filepath.Join(f.home, "e")}
	profiles["forge"] = config.Remote{SSHTarget: "ns-forge", WTBinary: binary, WingthingDir: f.remote}
	save()
	h.expect("R loads added forge", h.send("R"), "old-profile", "healthy-local")
	h.expect("capture rename against original generation", h.send("/old-profile\rr\x15forbidden-name"), "Name: forbidden-name")
	// Replace only binary/state; the SSH alias and session ID still collide.
	profiles["forge"] = config.Remote{SSHTarget: "ns-forge", WTBinary: custom, WingthingDir: mirror}
	save()
	h.expect("cadence cancels original rename", 0, "Cancelled: saved profile forge", "changed or was removed")
	h.expect("clear old filter for replacement", h.send("/\x15forge\rR"), "replacement-profile")
	h.expect("capture stop against replacement", h.send("x"), "Stop forge:same")
	profiles["forge"] = config.Remote{SSHTarget: "ns-delayed", WTBinary: custom, WingthingDir: mirror}
	save()
	h.expect("replacement refuses old stop", 0, "Cancelled: saved profile forge")
	northstarInventoryWait(t, "old query in flight", func() bool { _, err := os.Stat(filepath.Join(mailbox, "started")); return err == nil })
	profiles["forge"] = config.Remote{SSHTarget: "ns-forge", WTBinary: custom, WingthingDir: mirror}
	save()
	h.expect("replace profile while old SSH is outstanding", h.send("R"), "replacement-profile", "forge  connected")
	data, err := os.ReadFile(filepath.Join(mailbox, "started"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	northstarInventoryWait(t, "retired SSH joined", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
	h.expect("capture pending stop before removal", h.send("x"), "Stop forge:same")
	cli("remote", "rm", "forge")
	h.expect("removed generation cancels confirmation", 0, "Cancelled: saved profile forge")
	h.expect("confirmation key cannot mutate removed target", h.send("y/\x15\rR"), "healthy-local", "2 machines")
	h.waitScreen("no retired result can resurrect machine", h.send("R"), func(s string) bool {
		return strings.Contains(s, "healthy-local") && !strings.Contains(s, "replacement-profile") && !strings.Contains(s, "retired-host-result") && !strings.Contains(s, "forge  ")
	})
	profiles = map[string]config.Remote{"forge": {SSHTarget: "ns-forge", WTBinary: custom, WingthingDir: mirror}}
	save()
	h.expect("route restored", h.send("R"), "replacement-profile", "forge  connected")
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), "remotes: [malformed", 0600)
	h.expect("malformed registry retains stale rows and local work", h.send("R"), "Registry error", "replacement-profile", "stale", "healthy-local")
	h.expect("local attach while registry malformed", h.send("/healthy-local\r\r"), "RELOAD_LOCAL_READY")
	h.expect("local detach preserves healthy work", h.send("\x02q"), "healthy-local", "Registry error")
	h.expect("malformed remote mutation selection", h.send("/\x15replacement-profile\rr\x15forbidden-name\r"), "cannot validate remote session", "registry error")
	save()
	h.waitScreen("valid registry recovers without restart", h.send("/\x15\rR"), func(s string) bool {
		return strings.Contains(s, "forge  connected") && strings.Contains(s, "replacement-profile") && !strings.Contains(s, "Registry error") && !strings.Contains(s, "stale")
	})
	h.expect("attach exact replacement route", h.send("/replacement-profile\r\r"), "RELOAD_NEW_READY")
	h.expect("replacement detach", h.send("\x02q"), "replacement-profile", "Enter attach")
	h.exit("q")
	for _, fixture := range []*northstarEgg{local, old, replacement} {
		if fixture.killed.Load() != 0 {
			t.Fatal("retired confirmation killed a session")
		}
	}
	if eggclient.ReadSessionName(old.dir) != "old-profile" || eggclient.ReadSessionName(replacement.dir) != "replacement-profile" || len(old.attached) != 0 || len(replacement.attached) != 1 || len(local.attached) != 1 {
		t.Fatal("action crossed profile generation")
	}
	for _, record := range northstarSSHRecords(t, log) {
		if strings.Contains(record.Command, "'session' 'rename'") || strings.Contains(record.Command, "'session' 'kill'") {
			t.Fatal("retired mutation reached SSH")
		}
		if !strings.Contains(record.Command, "'attach'") && (!record.Batch || record.Mode != "-T") {
			t.Fatalf("background SSH could prompt: %+v", record)
		}
	}
	for _, name := range []string{"ssh.jsonl", "delayed.json"} {
		data, err := os.ReadFile(filepath.Join(mailbox, name))
		if err != nil {
			t.Fatal(err)
		}
		northstarWrite(t, filepath.Join(f.evidence, name), string(data), 0600)
	}
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), "remotes: [bad", 0600)
	startup := northstarStartPTY(t, f, binary, 160, 40)
	startup.expect("malformed registry at startup keeps local inventory", 0, "healthy-local", "Registry error", "1 machines")
	save()
	startup.expect("startup registry recovers", startup.send("R"), "replacement-profile", "forge  connected")
	startup.exit("q")
	northstarWrite(t, filepath.Join(f.local, "config.yaml"), "wing_id: [bad", 0600)
	invalid := northstarStartPTY(t, f, binary, 160, 40)
	invalid.expect("invalid primary config still fails", 0, "parse config")
	coldPTYExit(t, invalid, true)
	northstarWrite(t, filepath.Join(f.evidence, "reload-timing.txt"), fmt.Sprintf("registry journey=%s; alias=%s; canonical=%s; old query joined=true\n", time.Since(started), f.tmp, canonical), 0600)
}

// Compose the daily path on one executable, with native protected providers
// and durable wing records. Recovery discovers existing work without admission.
func TestNorthstarPTYDailyLoop(t *testing.T) {
	coldRequireSandbox(t)
	build := northstarNewFixture(t)
	built := northstarBuild(t, build, "stable")
	f := coldFixture(t)
	f.evidence = filepath.Join(f.repo, ".scratch", "evidence", "11", "daily-loop")
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	binary := coldInstallBinary(t, f, built)
	coldCleanup(t, f, binary)
	scopedNewNativeFixture(t, f, f.local, f.work)
	coldPolicyGuard(t, f)
	gate := testprovider.NewCompletionGate(t, f.work)
	parent := northstarStartPTY(t, f, binary, 160, 36, "claude", "--name", "daily-parent", "--cwd", f.work)
	parent.expect("direct cold scoped Claude", 0, "PARENT_READY")
	coldOneDaemon(t, f)
	var captured struct {
		ParentSessionID string           `json:"parent_session_id"`
		Receipts        []map[string]any `json:"receipts"`
	}
	data, err := os.ReadFile(filepath.Join(f.work, "receipts.json"))
	if err != nil || json.Unmarshal(data, &captured) != nil || captured.ParentSessionID == "" || len(captured.Receipts) != 1 {
		t.Fatalf("daily native receipts: %s %v", data, err)
	}
	receipt := captured.Receipts[0]
	childID, runID, wingID := receipt["session_id"].(string), receipt["run_id"].(string), receipt["wing_id"].(string)
	for _, id := range []string{captured.ParentSessionID, childID} {
		northstarSocket(t, filepath.Join(f.local, "eggs", id, "egg.sock"))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := gate.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	parent.exit("\x02q")
	forge := northstarStandaloneEgg(t, f, f.remote, "forge", "daily-forge", "working", "DAILY_FORGE_READY")
	hooks := northstarAttentionHooks(t, f, f.remote, "forge", false)
	hooks.emit(t, "SessionStart")
	hooks.emit(t, "UserPromptSubmit")
	if err := config.SaveRemotes(f.local, map[string]config.Remote{"forge": {SSHTarget: "ns-forge", WTBinary: binary, WingthingDir: f.remote}}); err != nil {
		t.Fatal(err)
	}
	h := northstarStartPTY(t, f, binary, 160, 36)
	h.expect("bare navigator retains cold local parent", 0, "daily-parent", "daily-forge")
	h.expect("exact forge attach", h.send("/daily-forge\r\r"), "DAILY_FORGE_READY")
	if receiveNorthstarID(t, forge.attached) != "forge" {
		t.Fatal("daily loop attached wrong remote")
	}
	h.expect("forge detach returns to navigator", h.send("\x02q"), "daily-forge", "Enter attach")
	hooks.emit(t, "PermissionRequest")
	h.expect("native forge attention", h.send("R"), "B daily-forge")
	if err := syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("lost client did not join")
	}
	for _, id := range []string{captured.ParentSessionID, childID} {
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(f.local, "eggs", id)); !alive {
			t.Fatal("client loss stopped native work")
		}
	}
	if err := gate.Release(); err != nil {
		t.Fatal(err)
	}
	client, err := controlsocket.Dial(ctx, f.local, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, denied, err := client.Call(ctx, "agent_wait", mustSliceJSON(map[string]any{"wing_id": wingID, "run_id": runID, "timeout_seconds": 30}))
	if err != nil || denied || result["status"] != "done" {
		t.Fatalf("daily child result: %v %v", result, err)
	}
	fresh := northstarStartPTY(t, f, binary, 160, 36)
	fresh.expect("fresh navigator after client loss", 0, "daily-parent", "B daily-forge")
	fresh.expect("discover original durable run", fresh.send("h/"+runID+"\r"), "Recent runs", wingID+":"+runID)
	fresh.expect("terminal run metadata and explicit recovery", fresh.send("i"), "Run details: "+wingID+":"+runID, "wt agent recover --json", "--snapshot PATH")
	fresh.expect("bounded same-run result", fresh.send("o"), "Result: "+wingID+":"+runID, "Fake Codex fixture-model")
	fresh.expect("return from result", fresh.send("\x1b"), "Recent runs")
	fresh.exit("q")
	recovery := exec.CommandContext(t.Context(), binary, "agent", "recover", "--json")
	recovery.Env, recovery.Dir = f.env, f.work
	recovered, err := recovery.CombinedOutput()
	if err != nil || !strings.Contains(string(recovered), runID) || !strings.Contains(string(recovered), childID) {
		t.Fatalf("explicit durable recovery: %v %s", err, recovered)
	}
	northstarWrite(t, filepath.Join(f.evidence, "recovery.json"), string(recovered), 0600)
	if forge.killed.Load() != 0 {
		t.Fatal("daily client loss stopped forge")
	}
}

func receiveNorthstarID(t *testing.T, ids <-chan string) string {
	t.Helper()
	select {
	case id := <-ids:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("exact PTY attachment missing")
	}
	return ""
}
