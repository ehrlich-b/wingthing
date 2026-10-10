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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wing"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"golang.org/x/sys/unix"
)

const fakeRunCodex = `
import json, os, re, subprocess, sys, tty
args=sys.argv[1:]
if args==['--version']:
 print('codex-cli 0.159.3'); sys.exit(0)
hooks={}; notify=None
for i,a in enumerate(args):
 if a=='-c' and i+1<len(args):
  value=args[i+1]
  if value.startswith('hooks.') and 'command=' in value:
   event=value.split('=',1)[0].split('.',1)[1]
   command=re.search(r'command=("(?:\\.|[^"\\])*")',value).group(1)
   hooks[event]=json.loads(command)
  elif value.startswith('notify='): notify=json.loads(value.split('=',1)[1])
assert '--no-daemon' in args and notify and 'SessionStart' in hooks
thread='fake-thread'
def hook(event, **extra):
 data={'hook_event_name':event,'session_id':thread};data.update(extra)
 subprocess.run(['/bin/sh','-c',hooks[event]],input=json.dumps(data).encode(),check=True)
tty.setraw(0)
model=args[args.index('-m')+1]
if model=='fixture-modal':
 os.write(1,b'Update available: private-startup-canary-token\r\n1. Update now 2. Skip\r\n')
 if os.read(0,1): raise RuntimeError('Wingthing typed into a startup modal')
 sys.exit(0)
initial=args[args.index('--')+1] if '--' in args else None
os.write(1,b'OpenAI Codex\r\nAsk Codex to do anything\r\n')
buffer=b''
while True:
 if initial is not None:
  prompt=initial; initial=None
 else:
  chunk=os.read(0,1)
  if not chunk: break
  if chunk!=b'\r': buffer+=chunk;continue
  prompt=buffer.decode().removeprefix('\x1b[200~').removesuffix('\x1b[201~');buffer=b''
 hook('SessionStart',source='startup')
 turn='fake-turn'
 hook('UserPromptSubmit',turn_id=turn,prompt=prompt)
 with open(os.path.join(os.environ['HOME'],'..','completion-gate'),'rb') as gate: gate.read(1)
 payload={'type':'agent-turn-complete','thread-id':thread,'turn-id':turn,'input-messages':[prompt],'last-assistant-message':'Fake Codex '+model+': Ω🙂 '+prompt}
 subprocess.run(notify+[json.dumps(payload)],check=True)
 hook('Stop',turn_id=turn)
`

func TestFakeCodexDoesNotAnnounceReadinessAtEmptyStartup(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	args, enabled, err := egg.CodexRunArgs([]string{"-m", "fixture-model"}, home, "empty-startup")
	if err != nil || !enabled {
		t.Fatalf("native fake setup: %v", err)
	}
	cmd := exec.Command("nice", append([]string{"-n", "15", python, "-c", fakeRunCodex}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = terminal.Close() }()
	scan := bufio.NewScanner(terminal)
	for scan.Scan() {
		if strings.Contains(scan.Text(), "Ask Codex") {
			entries, err := os.ReadDir(filepath.Join(home, ".codex", "wingthing-events", "empty-startup"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("fake emitted a pre-prompt hook: %v %v", entries, err)
			}
			return
		}
	}
	t.Fatalf("fake did not reach empty composer: %v", scan.Err())
}

func TestBuiltWTStdioWingEggFakeCodexResult(t *testing.T) {
	for _, modal := range []bool{false, true} {
		t.Run(fmt.Sprintf("startup_modal=%t", modal), func(t *testing.T) { testBuiltWTStdioWingEggFakeCodex(t, modal) })
	}
}

func TestBuiltWTStdioWingEggFakeCodexTaskResult(t *testing.T) {
	testBuiltWTStdioWingEggFakeCodexMode(t, false, true)
}

func testBuiltWTStdioWingEggFakeCodex(t *testing.T, modal bool) {
	testBuiltWTStdioWingEggFakeCodexMode(t, modal, false)
}

func testBuiltWTStdioWingEggFakeCodexMode(t *testing.T, modal, nativeTask bool) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	if nativeTask {
		scratch := filepath.Join(repo, ".scratch")
		if err := os.MkdirAll(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		fixture, err = os.MkdirTemp(scratch, "task-codex-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	}
	// Linux test images carry this checkout's built fixture, without Go or
	// source. Otherwise build it directly, without requiring make.
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(fixture, "wt")
		build := exec.Command("nice", "-n", "15", "go", "build", "-p", "2", "-buildvcs=false", "-o", binary, "./cmd/wt")
		build.Dir = repo
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fixture wt: %v\n%s", err, output)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(fixture, "home")
	bin := filepath.Join(fixture, "bin")
	stateReal := filepath.Join(fixture, "s")
	for _, dir := range []string{home, bin, stateReal} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gatePath := filepath.Join(fixture, "completion-gate")
	if err := unix.Mkfifo(gatePath, 0600); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(bin, "codex")
	if err := os.WriteFile(provider, []byte("#!"+python+"\n"+fakeRunCodex), 0700); err != nil {
		t.Fatal(err)
	}
	alias := shortFixtureStateAlias(t, repo, fixture)
	state := filepath.Join(alias, "s")
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WT_MCP_CLIENT", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{Dir: state, WingID: "fixture-wing", DefaultAgent: "codex"}
	wc := &config.WingConfig{WingID: cfg.WingID, AllowUnsandboxed: true}
	service := &wingsession.Service{Config: cfg, Home: home, Inventory: wing.ListAliveEggSessions, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.UnsandboxedEggConfig()} }, Register: func(string) error { return nil }}
	var mu sync.Mutex
	var children []*exec.Cmd
	var exited []chan error
	spawnErrors := make(chan error, 8)
	eggReady := make(chan struct{})
	var eggReadyOnce sync.Once
	service.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (client *egg.Client, spawnErr error) {
		defer func() {
			if spawnErr != nil {
				spawnErrors <- spawnErr
			}
		}()
		if launch.Identity.UserID != "fixture-owner" || opts.Egg.Principal != wingsession.UserPrincipal("fixture-owner") {
			return nil, fmt.Errorf("fixture launch lost ownership")
		}
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
		args := []string{"egg", "run", "--session-id", opts.SessionID, "--agent", opts.Agent, "--kind", "agent", "--cwd", launch.CWD, "--outer-boundary", "--network", "*", "--agent-domains", "none", "--omit-browser-bridge", "--env", "HOME=" + home, "--env", "PATH=" + os.Getenv("PATH")}
		for _, arg := range opts.Egg.AgentArgs {
			args = append(args, "--agent-arg="+arg)
		}
		if opts.Egg.InitialRun != nil {
			wire, err := json.Marshal(opts.Egg.InitialRun)
			if err != nil {
				return nil, err
			}
			args = append(args, "--initial-run="+string(wire))
		}
		child := exec.Command(binary, args...)
		child.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + os.Getenv("PATH")}
		child.Stdout = io.Discard
		logs, err := child.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := child.Start(); err != nil {
			return nil, err
		}
		ready := make(chan struct{})
		done := make(chan error, 1)
		mu.Lock()
		children = append(children, child)
		exited = append(exited, done)
		mu.Unlock()
		go func() {
			scan := bufio.NewScanner(logs)
			announced := false
			var diagnostics strings.Builder
			for scan.Scan() {
				diagnostics.WriteString(scan.Text() + "\n")
				if !announced && strings.Contains(scan.Text(), "egg: serving on ") {
					announced = true
					close(ready)
					eggReadyOnce.Do(func() { close(eggReady) })
				}
			}
			err := child.Wait()
			if err != nil {
				err = fmt.Errorf("%v: %s", err, diagnostics.String())
			}
			done <- err
		}()
		select {
		case <-ready:
			return egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		case err := <-done:
			done <- err
			return nil, fmt.Errorf("fixture egg exited before socket readiness: %v", err)
		}
	}
	if err := service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if service.RunManager != nil {
			service.RunManager.Close()
		}
		mu.Lock()
		defer mu.Unlock()
		for i, child := range children {
			child.Process.Signal(os.Interrupt)
			<-exited[i]
		}
	})
	listener, err := localmcp.ListenLocalWingControl(t.Context(), "fixture", service, "fixture-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { listener.Close() }()
	process := exec.Command(binary, "mcp", "stdio", "--unsandboxed")
	process.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + os.Getenv("PATH")}
	process.Stderr = os.Stderr
	input, err := process.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		input.Close()
		if !finished {
			process.Process.Kill()
			process.Wait()
		}
	}()
	model, timeout := "fixture-model", 30
	if modal {
		model, timeout = "fixture-modal", 10
	}
	args, _ := json.Marshal(map[string]any{"prompt": "fixture request", "agent": "codex", "model": model, "cwd": fixture, "timeout_seconds": timeout})
	params := map[string]any{"name": "agent_run", "arguments": json.RawMessage(args)}
	if nativeTask {
		params["task"] = map[string]any{"ttl": 60000}
	}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	if _, err := input.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Task    control.MCPTask `json:"task"`
			Data    map[string]any  `json:"structuredContent"`
			IsError bool            `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.IsError {
		t.Fatalf("stdio admission: %+v", response)
	}
	id, session := "", ""
	if nativeTask {
		if response.Result.Task.Status != "working" || response.Result.Data != nil {
			t.Fatalf("not a CreateTaskResult: %+v", response)
		}
		id = response.Result.Task.TaskID
		run, err := service.RunManager.Get(wingsession.Authority{UserID: "fixture-owner", Principal: wingsession.UserPrincipal("fixture-owner")}, id)
		if err != nil {
			t.Fatal(err)
		}
		session = run.SessionID
	} else {
		id = response.Result.Data["run_id"].(string)
		session = response.Result.Data["session_id"].(string)
	}
	input.Close()
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true
	// Admission precedes the asynchronous spawn. Socket readiness is emitted
	// after the egg has created its directory and persisted launch metadata.
	select {
	case <-eggReady:
	case err := <-spawnErrors:
		t.Fatalf("fixture egg startup: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	if modal {
		client, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{Unsandboxed: true})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		wire, _ := json.Marshal(map[string]any{"run_id": id, "timeout_seconds": 30})
		data, denied, err := client.Call(t.Context(), "agent_wait", wire)
		if err != nil || denied || data["status"] != "failed" || data["failure_kind"] != string(agent.ProviderNotReady) {
			t.Fatalf("startup modal result: %v %v", data, err)
		}
		wire, _ = json.Marshal(map[string]any{"run_id": id})
		data, denied, err = client.Call(t.Context(), "agent_result", wire)
		encoded, _ := json.Marshal(data)
		if err != nil || denied || !strings.Contains(string(encoded), "update prompt") || strings.Contains(string(encoded), "private-startup-canary-token") {
			t.Fatalf("startup diagnostic was lost or not redacted: %v %v", data, err)
		}
		return
	}
	// The provider receipt is authoritative; hold its completion while the
	// wing loses every in-memory observer and reloads the durable run map.
	if _, _, err := eggclient.WaitSessionLifecycle(t.Context(), cfg, eggclient.LocalSession{ID: session, Agent: "codex", CWD: fixture}, 0, "working"); err != nil {
		t.Fatal(err)
	}
	meta := eggclient.ReadEggMetaValues(filepath.Join(state, "eggs", session))
	started, err := strconv.ParseInt(meta["started_at_nanos"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := eggclient.RunTurnStatus(t.Context(), cfg, eggclient.LocalSession{ID: session, Agent: "codex"}, id)
	admitted, err := service.RunManager.Get(wingsession.Authority{UserID: "fixture-owner", Principal: wingsession.UserPrincipal("fixture-owner")}, id)
	if err != nil || !reserved.Deadline.Equal(admitted.Result.Deadline) || reserved.Deadline.Before(time.Unix(0, started)) {
		t.Fatalf("egg deadline did not start at provider launch: %+v, %v", reserved, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	service.RunManager = nil
	if err := service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	listener, err = localmcp.ListenLocalWingControl(t.Context(), "fixture", service, "fixture-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	var nativeResult map[string]any
	var restartedDecoder *json.Decoder
	if nativeTask {
		process = exec.Command(binary, "mcp", "stdio", "--unsandboxed")
		process.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + os.Getenv("PATH")}
		process.Stderr = os.Stderr
		input, err = process.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err = process.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		finished = false
		restartedDecoder = json.NewDecoder(output)
		encoder := json.NewEncoder(input)
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tasks/get", "params": map[string]any{"taskId": id}}); err != nil {
			t.Fatal(err)
		}
		var get struct {
			Result control.MCPTask `json:"result"`
			Error  any             `json:"error"`
		}
		if err := restartedDecoder.Decode(&get); err != nil || get.Error != nil || get.Result.Status != "working" {
			t.Fatalf("new client lost running task: %+v %v", get, err)
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tasks/result", "params": map[string]any{"taskId": id}}); err != nil {
			t.Fatal(err)
		}
	}
	gate, err := os.OpenFile(gatePath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	gate.Close()
	if nativeTask {
		var result struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		if err := restartedDecoder.Decode(&result); err != nil || result.Error != nil {
			t.Fatalf("native task result: %+v %v", result, err)
		}
		nativeResult = result.Result
		if nativeResult["_meta"].(map[string]any)[control.MCPRelatedTask].(map[string]any)["taskId"] != id {
			t.Fatal("task result correlation lost")
		}
		_ = input.Close()
		if err := process.Wait(); err != nil {
			t.Fatal(err)
		}
		finished = true
	}
	// A new MCP client observes the same wing-owned outcome after host exit.
	client, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{Unsandboxed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wire, _ := json.Marshal(map[string]any{"run_id": id, "timeout_seconds": 30})
	if data, denied, err := client.Call(t.Context(), "agent_wait", wire); err != nil || denied || data["status"] != "done" {
		select {
		case reason := <-spawnErrors:
			t.Logf("fixture spawn: %v", reason)
		default:
		}
		t.Fatalf("native completion: %v denied=%v error=%v", data, denied, err)
	}
	wire, _ = json.Marshal(map[string]any{"run_id": id})
	result, denied, err := client.Call(t.Context(), "agent_result", wire)
	if err != nil || denied || result["output"] != "Fake Codex fixture-model: Ω🙂 fixture request" || result["turn_id"] != "fake-turn" {
		t.Fatalf("native result: %v %v", result, err)
	}
	if nativeTask && !reflect.DeepEqual(nativeResult["structuredContent"], result) {
		t.Fatalf("tasks/result differs from agent_result: %v vs %v", nativeResult, result)
	}
	visible, err := service.ListWeb(context.Background(), wingsession.Authority{UserID: "fixture-owner", Role: "owner", Browser: true})
	if err != nil || len(visible) != 1 || visible[0].SessionID != session {
		t.Fatalf("ordinary web egg: %v %v", visible, err)
	}
}
