package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/fsnotify/fsnotify"
)

var fakeScopedClaude = testprovider.ScopedClaude

type sliceCProcess struct {
	cmd  *exec.Cmd
	done chan error
	mu   sync.Mutex
	logs strings.Builder
}

func startSliceCProcess(t *testing.T, binary string, args, environment []string, cwd, readyText string, input io.Reader) (*sliceCProcess, <-chan struct{}) {
	t.Helper()
	p := &sliceCProcess{cmd: exec.Command(binary, args...), done: make(chan error, 1)}
	p.cmd.Env, p.cmd.Dir, p.cmd.Stdin = environment, cwd, input
	reader, writer := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = writer, writer
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() {
		announced := false
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			p.mu.Lock()
			p.logs.WriteString(line + "\n")
			p.mu.Unlock()
			if !announced && strings.Contains(line, readyText) {
				announced = true
				close(ready)
			}
		}
		_ = reader.Close()
	}()
	go func() { err := p.cmd.Wait(); _ = writer.Close(); p.done <- err }()
	return p, ready
}
func (p *sliceCProcess) diagnostics() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logs.String()
}

func sliceCArtifact(t *testing.T, dir, name string, p *sliceCProcess) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		t.Fatal(err)
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return data
		}
		select {
		case <-w.Events:
		case err := <-w.Errors:
			t.Fatal(err)
		case err := <-p.done:
			p.done <- err
			diagnostics := p.diagnostics()
			if strings.Contains(diagnostics, "sandbox_apply: Operation not permitted") {
				t.Skip("host sandbox forbids nested sandbox-exec: " + diagnostics)
			}
			t.Fatalf("process ended before %s: %v\n%s", name, err, diagnostics)
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v\n%s", name, ctx.Err(), p.diagnostics())
		}
	}
}

func TestBuiltWTClaudeScopedMailboxTwoLocalOnlyWings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("scoped parent's protected-write sandbox contract currently requires macOS")
	}
	probe := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
	if output, err := probe.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "sandbox_apply: Operation not permitted") {
			t.Skip("host sandbox forbids nested sandbox-exec: " + strings.TrimSpace(string(output)))
		}
		t.Fatalf("sandbox preflight: %v %s", err, output)
	}
	for _, exitParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent_exit=%t", exitParent), func(t *testing.T) { testBuiltWTClaudeScopedMailbox(t, exitParent) })
	}
}

func testBuiltWTClaudeScopedMailbox(t *testing.T, exitParent bool) {
	h := testssh.New(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(h.Root, "home")
	bin := filepath.Join(home, "bin")
	work := filepath.Join(home, "work")
	remoteWork := filepath.Join(home, "remote-work")
	for _, dir := range []string{home, bin, work, remoteWork} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	keyDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("fake fixture key; not a credential"), 0600); err != nil {
		t.Fatal(err)
	}
	built := localOnlyFixtureBinary(t, repo, h.Root)
	binary := filepath.Join(home, "wt")
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"claude": fakeScopedClaude, "codex": testprovider.Codex} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!"+python+"\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	localGate := testprovider.NewCompletionGate(t, work)
	remoteGate := testprovider.NewCompletionGate(t, remoteWork)
	// Short state spellings keep egg sockets within Darwin sockaddr_un while
	// their canonical paths remain outside the parent's writable workspace.
	alias, err := os.MkdirTemp(filepath.Join(repo, ".scratch"), "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	localState, remoteState := filepath.Join(alias, "l"), filepath.Join(alias, "r")
	path := bin + string(os.PathListSeparator) + h.Root + string(os.PathListSeparator) + os.Getenv("PATH")
	environment := func(state string) []string {
		return []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + path, "SHELL=/bin/sh", "WT_FAKE_SSH_ROOT=" + h.Root, "WT_FAKE_SSH_EXECUTE=1"}
	}
	startWing := func(state, workspace string) *sliceCProcess {
		t.Helper()
		if err := config.SaveWingConfig(state, &config.WingConfig{Roost: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
		p, ready := startSliceCProcess(t, binary, []string{"wing", "start", "--local-only", "--foreground", "--paths", workspace}, environment(state), workspace, "wing: local control ready", nil)
		t.Cleanup(func() { _ = p.cmd.Process.Signal(os.Interrupt); <-p.done })
		select {
		case <-ready:
		case err := <-p.done:
			p.done <- err
			t.Fatalf("wing startup: %v\n%s", err, p.diagnostics())
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		return p
	}
	startWing(localState, work)
	startWing(remoteState, remoteWork)
	// Clean all fixture eggs even if the parent fails before publishing receipts.
	// Wing shutdown itself deliberately leaves accepted eggs alive.
	t.Cleanup(func() {
		for _, state := range []string{localState, remoteState} {
			entries, _ := os.ReadDir(filepath.Join(state, "eggs"))
			for _, entry := range entries {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = eggclient.KillOrphanEggContext(stopCtx, &config.Config{Dir: state}, entry.Name())
				stopCancel()
			}
		}
	})
	meta, err := sshcontrol.InspectLocal(t.Context(), remoteState, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	add := exec.Command(binary, "mcp", "connect", "add", "forge", "--ssh", "forge", "--wingthing-dir", remoteState, "--wt-binary", binary)
	add.Env, add.Dir = environment(localState), work
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("remember fake SSH wing: %v\n%s", err, output)
	}
	controlPath, err := controlsocket.Path(localState)
	if err != nil {
		t.Fatal(err)
	}
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	viewer, _ := startSliceCProcess(t, binary, []string{"claude", "--name", "weekend", "--", "--model", "fake-parent", "--fixture-control", controlPath, "--fixture-state-file", filepath.Join(localState, "wing.yaml"), "--fixture-ssh-key", keyFile}, environment(localState), work, "PARENT_READY", in)
	t.Cleanup(func() { _ = out.Close(); _ = viewer.cmd.Process.Kill(); <-viewer.done })
	t.Cleanup(func() {
		if t.Failed() {
			for _, state := range []string{localState, remoteState} {
				entries, _ := os.ReadDir(filepath.Join(state, "eggs"))
				for _, entry := range entries {
					testprovider.LogEggTails(t, filepath.Join(state, "eggs", entry.Name()))
				}
			}
		}
	})
	receiptData := sliceCArtifact(t, work, "receipts.json", viewer)
	var captured struct {
		Receipts        []map[string]any `json:"receipts"`
		MailboxPID      int              `json:"mailbox_pid"`
		Mailbox         string           `json:"mailbox"`
		ParentSessionID string           `json:"parent_session_id"`
	}
	if err := json.Unmarshal(receiptData, &captured); err != nil {
		t.Fatal(err)
	}
	if len(captured.Receipts) != 2 {
		t.Fatalf("parent did not spawn two children: %s", receiptData)
	}
	owner, err := controlsocket.Dial(t.Context(), localState, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	list, denied, err := owner.Call(t.Context(), "terminal_list", mustSliceJSON(map[string]any{"wing_id": owner.Welcome.WingID}))
	if err != nil || denied {
		t.Fatalf("inventory: %v %v", list, err)
	}
	parentID := ""
	for _, raw := range list["sessions"].([]any) {
		session := raw.(map[string]any)
		if session["name"] == "weekend" {
			parentID = session["id"].(string)
		}
	}
	if parentID == "" || parentID != captured.ParentSessionID || captured.Mailbox == "" {
		t.Fatalf("parent is not a named wing egg: %v", list)
	}
	childStates := map[string]string{owner.Welcome.WingID: localState, meta.WingID: remoteState}
	childGates := map[string]*testprovider.CompletionGate{owner.Welcome.WingID: localGate, meta.WingID: remoteGate}
	childPIDs := map[string]int{}
	assertWorking := func(receipt map[string]any) {
		t.Helper()
		state, id := childStates[receipt["wing_id"].(string)], receipt["session_id"].(string)
		pid, alive := eggclient.ReadAliveEggPID(filepath.Join(state, "eggs", id))
		if !alive || childPIDs[id] != 0 && childPIDs[id] != pid {
			t.Fatalf("child did not survive: receipt=%v pid=%d original_pid=%d alive=%t", receipt, pid, childPIDs[id], alive)
		}
		view, err := eggclient.ReadSessionLifecycleView(t.Context(), &config.Config{Dir: state}, id, 0, 50)
		if err != nil || !view.ProcessAlive || view.State != "working" || view.StateSource != "codex_hook" {
			t.Fatalf("unreleased child is not working: receipt=%v lifecycle=%+v err=%v", receipt, view, err)
		}
		childPIDs[id] = pid
	}
	readyCtx, readyCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer readyCancel()
	seenWings := map[string]bool{}
	for _, receipt := range captured.Receipts {
		wing := receipt["wing_id"].(string)
		gate := childGates[wing]
		if gate == nil || seenWings[wing] {
			t.Fatalf("expected one child per fixture wing: %v", captured.Receipts)
		}
		seenWings[wing] = true
		if err := gate.WaitReady(readyCtx); err != nil {
			t.Fatal(err)
		}
		assertWorking(receipt)
	}
	for range 2 {
		forward := <-h.Started
		forward.Drop()
	}
	var reattached *sliceCProcess
	var reattachOut *os.File
	if exitParent {
		exitCtx, exitCancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer exitCancel()
		if _, err := io.WriteString(out, "exit\r"); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-viewer.done:
			viewer.done <- err
			if err != nil {
				t.Fatalf("parent exit: %v\n%s", err, viewer.diagnostics())
			}
		case <-exitCtx.Done():
			t.Fatalf("parent exit: %v\n%s", exitCtx.Err(), viewer.diagnostics())
		}
		if err := testprovider.WaitParentMailboxExit(exitCtx, captured.Mailbox, parentID); err != nil {
			t.Fatal(err)
		}
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(localState, "eggs", parentID)); alive {
			t.Fatal("parent egg is still alive after mailbox exit")
		}
	} else {
		_ = viewer.cmd.Process.Kill()
		<-viewer.done
		viewer.done <- nil
		_ = out.Close()
		var reattachIn *os.File
		reattachIn, reattachOut, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reattached, _ = startSliceCProcess(t, binary, []string{"attach", "weekend"}, environment(localState), work, "PARENT_READY", reattachIn)
		t.Cleanup(func() { _ = reattachOut.Close(); _ = reattached.cmd.Process.Kill(); <-reattached.done })
		if _, err := io.WriteString(reattachOut, "reconnect\r"); err != nil {
			t.Fatal(err)
		}
		connection := sliceCArtifact(t, work, "mailbox-reconnected.json", reattached)
		var reconnected struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(connection, &reconnected); err != nil || reconnected.PID == captured.MailboxPID {
			t.Fatalf("mailbox did not reconnect: %s", connection)
		}
	}
	// Both eggs must retain their original processes and native working state
	// after parent exit or viewer/mailbox replacement, before either release.
	for _, receipt := range captured.Receipts {
		assertWorking(receipt)
	}
	_ = owner.Close()
	fresh, err := controlsocket.Dial(t.Context(), localState, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	for _, receipt := range captured.Receipts {
		// In particular, releasing the first child must not release the second.
		assertWorking(receipt)
		pending, denied, err := fresh.Call(t.Context(), "agent_result", mustSliceJSON(map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"]}))
		if err != nil || denied || pending["ready"] != false || pending["status"] != "pending" {
			t.Fatalf("child completed before its release: %v %v", pending, err)
		}
		if err := childGates[receipt["wing_id"].(string)].Release(); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"], "timeout_seconds": 60}
		if data, denied, err := fresh.Call(t.Context(), "agent_wait", mustSliceJSON(args)); err != nil || denied || data["status"] != "done" || data["timed_out"] == true {
			t.Fatalf("fresh client wait: %v %v", data, err)
		}
		delete(args, "timeout_seconds")
		result, denied, err := fresh.Call(t.Context(), "agent_result", mustSliceJSON(args))
		if err != nil || denied || result["ready"] != true || result["status"] != "done" || result["run_id"] != receipt["run_id"] || result["session_id"] != receipt["session_id"] || result["wing_id"] != receipt["wing_id"] || !strings.Contains(fmt.Sprint(result["output"]), "Fake Codex fixture-model") {
			t.Fatalf("fresh client lost result: %v %v", result, err)
		}
	}
	if !exitParent {
		if _, err := io.WriteString(reattachOut, "recover\r"); err != nil {
			t.Fatal(err)
		}
		var results []map[string]any
		if err := json.Unmarshal(sliceCArtifact(t, work, "results.json", reattached), &results); err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("recovered %d results", len(results))
		}
		for i, result := range results {
			if result["run_id"] != captured.Receipts[i]["run_id"] || result["session_id"] != captured.Receipts[i]["session_id"] || result["wing_id"] != captured.Receipts[i]["wing_id"] || result["ready"] != true {
				t.Fatalf("reattachment changed child identity: %v", result)
			}
		}
	}
}

func mustSliceJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
