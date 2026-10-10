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
	"golang.org/x/sys/unix"
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
		case <-t.Context().Done():
			t.Fatalf("waiting for %s: %v\n%s", name, t.Context().Err(), p.diagnostics())
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
	if err := unix.Mkfifo(filepath.Join(h.Root, "completion-gate"), 0600); err != nil {
		t.Fatal(err)
	}
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
				wire, _ := json.Marshal(opts.Egg.InitialRun)
				args = append(args, "--initial-run="+string(wire))
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
	var captured struct {
		Receipts   []map[string]any `json:"receipts"`
		MailboxPID int              `json:"mailbox_pid"`
	}
	if err := json.Unmarshal(mailboxProcessArtifact(t, viewer, work, "receipts.json"), &captured); err != nil {
		t.Fatal(err)
	}
	if len(captured.Receipts) != 2 {
		t.Fatalf("parent children: %+v", captured)
	}
	gate, err := os.OpenFile(filepath.Join(h.Root, "completion-gate"), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
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
	if exitParent {
		if _, err := io.WriteString(writer, "exit\r"); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-viewer.done:
			viewer.done <- err
			if err != nil {
				t.Fatalf("parent exit: %v %s", err, viewer.diagnostics())
			}
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	} else {
		_ = viewer.cmd.Process.Kill()
		<-viewer.done
		viewer.done <- nil
		reattachInput, reattachWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reattached, _ := mailboxProcessStart(t, binary, []string{"attach", "weekend"}, env(localState), work, reattachInput, "PARENT_READY")
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
		if _, err := gate.Write([]byte{1, 1}); err != nil {
			t.Fatal(err)
		}
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
	if exitParent {
		if _, err := gate.Write([]byte{1, 1}); err != nil {
			t.Fatal(err)
		}
	}
	_ = gate.Close()
	fresh, err := controlsocket.Dial(t.Context(), localState, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	for _, receipt := range captured.Receipts {
		args := map[string]any{"wing_id": receipt["wing_id"], "run_id": receipt["run_id"], "timeout_seconds": 60}
		scopedCall(t, fresh, "agent_wait", args)
		delete(args, "timeout_seconds")
		result := scopedCall(t, fresh, "agent_result", args)
		if result["ready"] != true || result["session_id"] != receipt["session_id"] || !strings.Contains(fmt.Sprint(result["output"]), "Fake Codex fixture-model") {
			t.Fatalf("fresh result: %v", result)
		}
	}
}
