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
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"golang.org/x/sys/unix"
)

func TestBuiltWTConnectRememberedSSHRunSurvivesDrop(t *testing.T) {
	testBuiltWTConnectRememberedSSHRun(t, false)
}

func TestBuiltWTConnectRememberedSSHNativeTaskSurvivesDrop(t *testing.T) {
	testBuiltWTConnectRememberedSSHRun(t, true)
}

func testBuiltWTConnectRememberedSSHRun(t *testing.T, nativeTask bool) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	h := testssh.New(t)
	binary := localOnlyFixtureBinary(t, repo, h.Root)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	home, bin := filepath.Join(h.Root, "home"), filepath.Join(h.Root, "bin")
	// Preserve short egg socket spellings while all data stays in this fixture.
	alias, err := os.MkdirTemp(filepath.Join(repo, ".scratch"), "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h.Root, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	localState, remoteState := filepath.Join(alias, "l"), filepath.Join(alias, "r")
	for _, dir := range []string{home, bin, localState, remoteState} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+fakeRunCodex), 0700); err != nil {
		t.Fatal(err)
	}
	// The remote PATH keeps an old wt for another pipeline; only the explicitly
	// selected fixture build understands the read-only inspect handshake.
	if err := os.WriteFile(filepath.Join(bin, "wt"), []byte("#!/bin/sh\necho 'Error: unknown flag: --client' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	remoteBinary := filepath.Join(bin, "wt dev")
	if err := os.Symlink(binary, remoteBinary); err != nil {
		t.Fatal(err)
	}
	gatePath := filepath.Join(h.Root, "completion-gate")
	if err := unix.Mkfifo(gatePath, 0600); err != nil {
		t.Fatal(err)
	}
	path := bin + string(os.PathListSeparator) + h.Root + string(os.PathListSeparator) + os.Getenv("PATH")
	work := config.CanonicalProviderPath(h.Root)
	environment := func(state string) []string {
		return []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + path, "SHELL=/bin/sh", "WT_FAKE_SSH_ROOT=" + h.Root, "WT_FAKE_SSH_EXECUTE=1"}
	}
	startWing := func(state string) {
		t.Helper()
		if err := config.SaveWingConfig(state, &config.WingConfig{AllowUnsandboxed: true, Roost: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
		process := exec.Command(binary, "wing", "start", "--local-only", "--foreground", "--paths", work)
		process.Env = environment(state)
		process.Dir = work
		process.Stdout = io.Discard
		logs, err := process.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "fixture.pid"), []byte(fmt.Sprint(process.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		ready, done := make(chan struct{}), make(chan error, 1)
		go func() {
			var diagnostics strings.Builder
			scanner := bufio.NewScanner(logs)
			announced := false
			for scanner.Scan() {
				diagnostics.WriteString(scanner.Text() + "\n")
				if !announced && strings.Contains(scanner.Text(), "wing: local control ready") {
					announced = true
					close(ready)
				}
			}
			err := process.Wait()
			if err != nil {
				err = fmt.Errorf("%w: %s", err, diagnostics.String())
			}
			done <- err
		}()
		t.Cleanup(func() { _ = process.Process.Signal(os.Interrupt); <-done })
		select {
		case <-ready:
		case err := <-done:
			done <- err
			t.Fatalf("wing startup: %v", err)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	startWing(localState)
	startWing(remoteState)
	meta, err := sshcontrol.InspectLocal(t.Context(), remoteState, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "forge", meta)
	add := exec.Command(binary, "mcp", "connect", "add", "forge", "--ssh", "forge", "--wingthing-dir", remoteState)
	add.Env = environment(localState)
	add.Dir = work
	if output, err := add.CombinedOutput(); err == nil || !strings.Contains(string(output), "remote wt is too old for this build") || !strings.Contains(string(output), "--wt-binary") {
		t.Fatalf("old PATH binary diagnostic: %v\n%s", err, output)
	}
	registry, err := config.LoadRemotes(localState)
	if err != nil || len(registry) != 0 {
		t.Fatalf("failed verification wrote registry: %v %v", registry, err)
	}
	add = exec.Command(binary, "mcp", "connect", "add", "forge", "--ssh", "forge", "--wingthing-dir", remoteState, "--wt-binary", remoteBinary)
	add.Env = environment(localState)
	add.Dir = work
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("built add: %v\n%s", err, output)
	}
	registry, err = config.LoadRemotes(localState)
	if err != nil || registry["forge"].WingID != meta.WingID || registry["forge"].WingthingDir != meta.WingthingDir || registry["forge"].WTBinary != remoteBinary {
		t.Fatalf("built registry: %v %v", registry, err)
	}
	process := exec.Command(binary, "mcp", "connect", "--unsandboxed")
	process.Env = environment(localState)
	process.Dir = work
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
	t.Cleanup(func() {
		_ = input.Close()
		if !finished {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	})
	decoder := json.NewDecoder(output)
	requestID := 0
	type reply struct {
		Result struct {
			Data    map[string]any `json:"structuredContent"`
			IsError bool           `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	rawCall := func(tool string, args map[string]any) reply {
		t.Helper()
		requestID++
		if err := json.NewEncoder(input).Encode(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}); err != nil {
			t.Fatal(err)
		}
		var response reply
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		response := rawCall(tool, args)
		if response.Error != nil || response.Result.IsError {
			t.Fatalf("aggregate %s: %+v", tool, response)
		}
		return response.Result.Data
	}
	rpc := func(method string, params map[string]any) map[string]any {
		t.Helper()
		requestID++
		if err := json.NewEncoder(input).Encode(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil || response.Error != nil {
			t.Fatalf("native %s: %+v %v", method, response, err)
		}
		return response.Result
	}
	directory := call("wing_list", map[string]any{})
	if directory["count"] != float64(2) {
		t.Fatalf("built directory: %v", directory)
	}
	localID := directory["wings"].([]any)[0].(map[string]any)["wing_id"].(string)
	if localID == meta.WingID {
		t.Fatal("independent wings share an ID")
	}
	args := map[string]any{"wing_id": meta.WingID, "prompt": "remote request", "agent": "codex", "model": "fixture-model", "cwd": work, "timeout_seconds": 90, "idempotency_key": "built-original"}
	var run map[string]any
	taskID := ""
	if nativeTask {
		created := rpc("tools/call", map[string]any{"name": "agent_run", "arguments": args, "task": map[string]any{"ttl": 60000}})
		task := created["task"].(map[string]any)
		if task["status"] != "working" || created["structuredContent"] != nil {
			t.Fatalf("not a CreateTaskResult: %v", created)
		}
		taskID = task["taskId"].(string)
		wingID, runID, err := control.SplitTaskID(taskID)
		if err != nil || wingID != meta.WingID {
			t.Fatalf("wrong owning wing: %s %v", taskID, err)
		}
		run = call("agent_status", map[string]any{"wing_id": meta.WingID, "run_id": runID})
		run["idempotency_key"] = "built-original"
	} else {
		run = call("agent_run", args)
	}
	id, session := run["run_id"].(string), run["session_id"].(string)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eggclient.KillOrphanEggContext(ctx, &config.Config{Dir: remoteState}, session)
	})
	if run["wing_id"] != meta.WingID || run["idempotency_key"] != "built-original" {
		t.Fatalf("unqualified remote receipt: %v", run)
	}
	// Pair with the provider's FIFO after native prompt submission, keeping the
	// run alive across the forced transport drop without timing assertions.
	gate, err := os.OpenFile(gatePath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	forward := <-h.Started
	h.Offline(t, "forge", true)
	forward.Drop()
	offline := rawCall("agent_status", map[string]any{"wing_id": meta.WingID, "run_id": id})
	if !offline.Result.IsError {
		t.Fatalf("offline call succeeded: %+v", offline)
	}
	directory = call("wing_list", map[string]any{})
	rows := directory["wings"].([]any)
	remote := rows[1].(map[string]any)
	if len(rows) != 2 || remote["online"] != false || remote["last_error"] == "" || remote["wing_id"] != meta.WingID {
		t.Fatalf("offline entry lost: %v", directory)
	}
	local := call("terminal_list", map[string]any{"wing_id": localID})
	if len(local["sessions"].([]any)) != 0 {
		t.Fatalf("remote provider ran locally: %v", local)
	}
	h.Offline(t, "forge", false)
	status := call("agent_status", map[string]any{"wing_id": meta.WingID, "run_id": id})
	if status["session_id"] != session || status["wing_id"] != meta.WingID {
		t.Fatalf("reconnect changed receipt: %v", status)
	}
	var retry map[string]any
	if nativeTask {
		created := rpc("tools/call", map[string]any{"name": "agent_run", "arguments": args, "task": map[string]any{"ttl": 60000}})
		if created["task"].(map[string]any)["taskId"] != taskID {
			t.Fatal("retry changed native task ID")
		}
		get := rpc("tasks/get", map[string]any{"taskId": taskID})
		if get["status"] != "working" || get["taskId"] != taskID {
			t.Fatalf("remote task routing lost: %v", get)
		}
		list := rpc("tasks/list", map[string]any{})
		tasks := list["tasks"].([]any)
		if len(tasks) != 1 || tasks[0].(map[string]any)["taskId"] != taskID {
			t.Fatalf("aggregate list: %v", list)
		}
		retry = call("agent_status", map[string]any{"wing_id": meta.WingID, "run_id": id})
	} else {
		retry = call("agent_run", args)
	}
	if retry["run_id"] != id || retry["session_id"] != session {
		t.Fatalf("retry launched twice: %v", retry)
	}
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = gate.Close()
	waited := call("agent_wait", map[string]any{"wing_id": meta.WingID, "run_id": id, "timeout_seconds": 30})
	if waited["status"] != "done" {
		t.Fatalf("remote completion: %v", waited)
	}
	result := call("agent_result", map[string]any{"wing_id": meta.WingID, "run_id": id})
	if result["output"] != "Fake Codex fixture-model: Ω🙂 remote request" || result["wing_id"] != meta.WingID || result["run_id"] != id {
		t.Fatalf("remote semantic result: %v", result)
	}
	if nativeTask {
		native := rpc("tasks/result", map[string]any{"taskId": taskID})
		if !reflect.DeepEqual(native["structuredContent"], result) || native["_meta"].(map[string]any)[control.MCPRelatedTask].(map[string]any)["taskId"] != taskID {
			t.Fatalf("aggregate task result differs: %v vs %v", native, result)
		}
		get := rpc("tasks/get", map[string]any{"taskId": taskID})
		if get["status"] != "completed" {
			t.Fatalf("remote task did not complete: %v", get)
		}
	}
	sessions := call("terminal_list", map[string]any{"wing_id": meta.WingID})["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["id"] != session {
		t.Fatalf("expected exactly one ordinary remote egg: %v", sessions)
	}
	for _, state := range []string{localState, remoteState} {
		for _, token := range []string{"device_token.yaml", "local_device_token.yaml"} {
			if _, err := os.Stat(filepath.Join(state, token)); !os.IsNotExist(err) {
				t.Fatalf("account-free flow wrote %s: %v", token, err)
			}
		}
	}
	call("terminal_stop", map[string]any{"wing_id": meta.WingID, "session": session})
	_ = input.Close()
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true
}
