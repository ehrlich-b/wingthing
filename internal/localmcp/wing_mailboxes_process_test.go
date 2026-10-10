package localmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/fsnotify/fsnotify"
)

// Built CLI and egg processes use fake providers and the ordinary wing services.
// Spawn is the declared protocol-fixture seam: it uses an outer process boundary,
// so this test makes no OS sandbox claim. The separate native built-wing test
// exercises the real spawner and checks denial of the unrestricted socket.
func TestBuiltClientScopedParentFakeProvidersSurviveDisconnect(t *testing.T) {
	for _, exitParent := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent_exit=%t", exitParent), func(t *testing.T) { builtScopedParentProtocol(t, exitParent) })
	}
}

type mailboxProcess struct {
	cmd  *exec.Cmd
	done chan error
	mu   sync.Mutex
	logs strings.Builder
}

func mailboxProcessStart(t *testing.T, binary string, args, env []string, cwd string, input *os.File, readyText string) (*mailboxProcess, <-chan struct{}) {
	t.Helper()
	p := &mailboxProcess{cmd: exec.Command(binary, args...), done: make(chan error, 1)}
	p.cmd.Env, p.cmd.Dir, p.cmd.Stdin = env, cwd, input
	reader, writer := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = writer, writer
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() {
		scan := bufio.NewScanner(reader)
		announced := false
		for scan.Scan() {
			line := scan.Text()
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
func (p *mailboxProcess) diagnostics() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logs.String()
}
func mailboxProcessArtifact(t *testing.T, p *mailboxProcess, workspace, name string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Add(workspace); err != nil {
		t.Fatal(err)
	}
	for {
		if data, err := os.ReadFile(filepath.Join(workspace, name)); err == nil {
			return data
		}
		select {
		case <-w.Events:
		case err := <-w.Errors:
			t.Fatal(err)
		case err := <-p.done:
			p.done <- err
			t.Fatalf("before %s: %v\n%s", name, err, p.diagnostics())
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v\n%s", name, ctx.Err(), p.diagnostics())
		}
	}
}

func builtScopedParentProtocol(t *testing.T, exitParent bool) {
	h := testssh.New(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	home, bin := filepath.Join(h.Root, "home"), filepath.Join(h.Root, "home", "bin")
	work, remoteWork := filepath.Join(home, "work"), filepath.Join(home, "remote-work")
	for _, dir := range []string{home, bin, work, remoteWork} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(home, "wt")
	if prebuilt := os.Getenv("WT_TEST_BINARY"); prebuilt != "" {
		data, err := os.ReadFile(prebuilt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, data, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		build := exec.Command("nice", "-n", "15", "go", "build", "-p", "2", "-buildvcs=false", "-o", binary, "./cmd/wt")
		build.Dir = repo
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, output)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"claude": testprovider.ScopedClaude, "codex": testprovider.Codex} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!"+python+"\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	localGate := testprovider.NewCompletionGate(t, work)
	remoteGate := testprovider.NewCompletionGate(t, remoteWork)
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
	t.Setenv("HOME", home)
	t.Setenv("PATH", path)
	oldExecutable, oldProtection := conversationBrokerExecutable, conversationBrokerProtection
	conversationBrokerExecutable = func() (string, error) { return binary, nil }
	// The test fixture has no OS sandbox. Policy, grants and ownership are
	// checked by the production wing code; sandbox enforcement is a native gate.
	conversationBrokerProtection = func(*config.Config, *egg.EggConfig, string, string, string, eggclient.EggIdentity, []string) error {
		return nil
	}
	t.Cleanup(func() { conversationBrokerExecutable, conversationBrokerProtection = oldExecutable, oldProtection })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	var mu sync.Mutex
	var eggs []*mailboxProcess
	env := func(state string) []string {
		return []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + path, "SHELL=/bin/sh", "WT_FAKE_SSH_ROOT=" + h.Root}
	}
	newWing := func(state, workspace string) *wingsession.Service {
		t.Helper()
		wc := &config.WingConfig{WingID: filepath.Base(state) + "-wing", Paths: config.PathList{{Path: workspace}}}
		if err := config.SaveWingConfig(state, wc); err != nil {
			t.Fatal(err)
		}
		owner, err := config.EnsureLocalOwner(state, "")
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Dir: state, WingID: wc.WingID, DefaultAgent: "codex"}
		service := &wingsession.Service{Config: cfg, Home: home, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }, Register: func(string) error { return nil }}
		service.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
			dir := filepath.Join(state, "eggs", opts.SessionID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
			if err := eggclient.WriteEggOwner(dir, launch.Identity.UserID, ""); err != nil {
				return nil, err
			}
			if err := eggclient.WriteSessionPrincipal(dir, opts.Egg.Principal); err != nil {
				return nil, err
			}
			if opts.Egg.Label != "" {
				if err := eggclient.WriteSessionName(dir, opts.Egg.Label); err != nil {
					return nil, err
				}
			}
			args := []string{"egg", "run", "--session-id", opts.SessionID, "--agent", opts.Agent, "--kind", "agent", "--cwd", launch.CWD, "--outer-boundary", "--network", "*", "--agent-domains", "none", "--omit-browser-bridge", "--env", "HOME=" + home, "--env", "PATH=" + path}
			for _, arg := range opts.Egg.AgentArgs {
				args = append(args, "--agent-arg="+arg)
			}
			if opts.Egg.InitialRun != nil {
				runPath, err := egg.WriteInitialRunFile(dir, opts.Egg.InitialRun)
				if err != nil {
					return nil, err
				}
				defer os.Remove(runPath)
				args = append(args, "--initial-run-file-required")
			}
			p, ready := mailboxProcessStart(t, binary, args, env(state), launch.CWD, nil, "egg: serving on")
			mu.Lock()
			eggs = append(eggs, p)
			mu.Unlock()
			select {
			case <-ready:
			case err := <-p.done:
				p.done <- err
				return nil, fmt.Errorf("egg startup: %v %s", err, p.diagnostics())
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		}
		if err := service.StartRuns(ctx); err != nil {
			t.Fatal(err)
		}
		listener, err := ListenLocalWingControl(ctx, "test", service, owner.ID, NewMCPAdmissionState())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close(); _ = service.RunManager.Close() })
		return service
	}
	local, remote := newWing(localState, work), newWing(remoteState, remoteWork)
	t.Cleanup(func() {
		for _, service := range []*wingsession.Service{local, remote} {
			entries, _ := os.ReadDir(filepath.Join(service.Config.Dir, "eggs"))
			for _, entry := range entries {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = eggclient.KillOrphanEggContext(stopCtx, service.Config, entry.Name())
				stopCancel()
			}
		}
		cancel()
		mu.Lock()
		defer mu.Unlock()
		for _, p := range eggs {
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	meta, err := sshcontrol.InspectLocal(t.Context(), remoteState, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	if err := config.SaveRemotes(localState, map[string]config.Remote{"forge": {SSHTarget: "forge", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	controlPath, err := controlsocket.Path(localState)
	if err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	viewer, _ := mailboxProcessStart(t, binary, []string{"claude", "--name", "weekend", "--", "--model", "fake-parent", "--fixture-control", controlPath, "--fixture-unconfined"}, env(localState), work, input, "PARENT_READY")
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close(); _ = viewer.cmd.Process.Kill(); <-viewer.done })
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
	var captured struct {
		Receipts        []map[string]any `json:"receipts"`
		MailboxPID      int              `json:"mailbox_pid"`
		Mailbox         string           `json:"mailbox"`
		ParentSessionID string           `json:"parent_session_id"`
	}
	if err := json.Unmarshal(mailboxProcessArtifact(t, viewer, work, "receipts.json"), &captured); err != nil {
		t.Fatal(err)
	}
	if len(captured.Receipts) != 2 {
		t.Fatalf("parent children: %+v", captured)
	}
	if captured.Mailbox == "" || captured.ParentSessionID == "" || eggclient.ReadSessionName(filepath.Join(localState, "eggs", captured.ParentSessionID)) != "weekend" {
		t.Fatalf("missing named parent mailbox: %+v", captured)
	}
	childStates := map[string]string{local.Config.WingID: localState, remote.Config.WingID: remoteState}
	childGates := map[string]*testprovider.CompletionGate{local.Config.WingID: localGate, remote.Config.WingID: remoteGate}
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
	for _, receipt := range captured.Receipts {
		if receipt["wing_id"] != local.Config.WingID {
			continue
		}
		id := receipt["session_id"].(string)
		_, ec, err := eggclient.OpenLocalEgg(t.Context(), local.Config, id)
		if err != nil {
			t.Fatal(err)
		}
		before, err := ec.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		observer, _, err := controlsocket.DialAttachment(t.Context(), localState, controlsocket.Hello{Attach: &controlsocket.Attachment{Session: id, ReadOnly: true}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := observer.Recv(); err != nil {
			t.Fatal(err)
		}
		after, err := ec.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if before.WriterId != after.WriterId || before.InputEpoch != after.InputEpoch {
			t.Fatalf("read-only viewer stole a native turn: before=%v after=%v", before, after)
		}
		if err := observer.Send(&pb.SessionMsg{SessionId: id, Payload: &pb.SessionMsg_Detach{Detach: true}}); err != nil {
			t.Fatal(err)
		}
		_ = observer.Close()
		_ = ec.Close()
	}
	for range 2 {
		forward := <-h.Started
		forward.Drop()
	}
	var reattached *mailboxProcess
	var reattachWriter *os.File
	if exitParent {
		exitCtx, exitCancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer exitCancel()
		if _, err := io.WriteString(writer, "exit\r"); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-viewer.done:
			viewer.done <- err
			if err != nil {
				t.Fatalf("parent exit: %v %s", err, viewer.diagnostics())
			}
		case <-exitCtx.Done():
			t.Fatalf("parent exit: %v\n%s", exitCtx.Err(), viewer.diagnostics())
		}
		if err := testprovider.WaitParentMailboxExit(exitCtx, captured.Mailbox, captured.ParentSessionID); err != nil {
			t.Fatal(err)
		}
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(localState, "eggs", captured.ParentSessionID)); alive {
			t.Fatal("parent egg is still alive after mailbox exit")
		}
	} else {
		_ = viewer.cmd.Process.Kill()
		<-viewer.done
		viewer.done <- nil
		var reattachInput *os.File
		reattachInput, reattachWriter, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reattached, _ = mailboxProcessStart(t, binary, []string{"attach", "weekend"}, env(localState), work, reattachInput, "PARENT_READY")
		t.Cleanup(func() {
			_ = reattachInput.Close()
			_ = reattachWriter.Close()
			_ = reattached.cmd.Process.Kill()
			<-reattached.done
		})
		if _, err := io.WriteString(reattachWriter, "reconnect\r"); err != nil {
			t.Fatal(err)
		}
		var connection struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(mailboxProcessArtifact(t, reattached, work, "mailbox-reconnected.json"), &connection); err != nil {
			t.Fatal(err)
		}
		if connection.PID == captured.MailboxPID {
			t.Fatal("mailbox connection was not replaced")
		}
	}
	// Both eggs must retain their original processes and native working state
	// after parent exit or viewer/mailbox replacement, before either release.
	for _, receipt := range captured.Receipts {
		assertWorking(receipt)
	}
	fresh, err := controlsocket.Dial(t.Context(), localState, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	for _, receipt := range captured.Receipts {
		// In particular, releasing the first child must not release the second.
		assertWorking(receipt)
		pending := scopedCall(t, fresh, "agent_result", map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"]})
		if pending["ready"] != false || pending["status"] != "pending" {
			t.Fatalf("child completed before its release: %v", pending)
		}
		if err := childGates[receipt["wing_id"].(string)].Release(); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"], "timeout_seconds": 60}
		if data := scopedCall(t, fresh, "agent_wait", args); data["status"] != "done" || data["timed_out"] == true {
			t.Fatalf("fresh client wait: %v", data)
		}
		delete(args, "timeout_seconds")
		result := scopedCall(t, fresh, "agent_result", args)
		if result["ready"] != true || result["status"] != "done" || result["run_id"] != receipt["run_id"] || result["session_id"] != receipt["session_id"] || result["wing_id"] != receipt["wing_id"] || !strings.Contains(fmt.Sprint(result["output"]), "Fake Codex fixture-model") {
			t.Fatalf("fresh result: %v", result)
		}
	}
	if !exitParent {
		if _, err := io.WriteString(reattachWriter, "recover\r"); err != nil {
			t.Fatal(err)
		}
		var results []map[string]any
		if err := json.Unmarshal(mailboxProcessArtifact(t, reattached, work, "results.json"), &results); err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("recovery: %v", results)
		}
		for i, result := range results {
			if result["run_id"] != captured.Receipts[i]["run_id"] || result["session_id"] != captured.Receipts[i]["session_id"] || result["wing_id"] != captured.Receipts[i]["wing_id"] || result["ready"] != true {
				t.Fatalf("changed receipt: %v", result)
			}
		}
	}
}
