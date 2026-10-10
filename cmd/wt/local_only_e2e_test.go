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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

func localOnlyFixtureBinary(t *testing.T, repo, fixture string) string {
	t.Helper()
	if binary := os.Getenv("WT_TEST_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(fixture, "wt")
	build := exec.Command("nice", "-n", "15", "go", "build", "-p", "2", "-buildvcs=false", "-o", binary, "./cmd/wt")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture wt: %v\n%s", err, output)
	}
	return binary
}

// This fixture uses the actual CLI wing, stdio adapter, spawner and egg. Only
// Codex is fake, with a FIFO barrier holding its native completion receipt.
func TestBuiltWTLocalOnlyStdioWingEggFakeCodexResult(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	binary := localOnlyFixtureBinary(t, repo, fixture)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	home, bin := filepath.Join(fixture, "home"), filepath.Join(fixture, "bin")
	for _, dir := range []string{home, bin, filepath.Join(fixture, "s")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+fakeRunCodex), 0700); err != nil {
		t.Fatal(err)
	}
	gatePath := filepath.Join(fixture, "completion-gate")
	if err := unix.Mkfifo(gatePath, 0600); err != nil {
		t.Fatal(err)
	}
	// The egg Unix sockets need a short spelling on Darwin. State and provider
	// homes remain disposable; no installed wt or real wing is used.
	alias := shortFixtureStateAlias(t, repo, fixture)
	state := filepath.Join(alias, "s")
	work := config.CanonicalProviderPath(fixture)
	childWork := filepath.Join(work, "runs", "batch")
	if err := os.MkdirAll(childWork, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WT_MCP_CLIENT", "")
	t.Setenv("WT_CONVERSATION_EXECUTION_ID", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := config.SaveWingConfig(state, &config.WingConfig{AllowUnsandboxed: true, Roost: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	environment := []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh"}
	var wingProcesses []*exec.Cmd
	var wingExits []chan error
	startWing := func() {
		child := exec.Command(binary, "wing", "start", "--local-only", "--foreground", "--paths", work)
		child.Env = environment
		child.Dir = work
		child.Stdout = io.Discard
		logs, err := child.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		ready, done := make(chan struct{}), make(chan error, 1)
		wingProcesses = append(wingProcesses, child)
		wingExits = append(wingExits, done)
		go func() {
			var diagnostics strings.Builder
			scan := bufio.NewScanner(logs)
			announced := false
			for scan.Scan() {
				diagnostics.WriteString(scan.Text() + "\n")
				if !announced && strings.Contains(scan.Text(), "wing: local control ready") {
					announced = true
					close(ready)
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
		case err := <-done:
			done <- err
			t.Fatalf("local-only wing failed before readiness: %v", err)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	t.Cleanup(func() {
		for i, process := range wingProcesses {
			_ = process.Process.Signal(os.Interrupt)
			<-wingExits[i]
		}
	})
	startWing()
	process := exec.Command(binary, "mcp", "stdio", "--unsandboxed")
	process.Env = environment
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
	call := func(tool string, arguments map[string]any) map[string]any {
		t.Helper()
		requestID++
		request := map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}}
		if err := json.NewEncoder(input).Encode(request); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result struct {
				Data    map[string]any `json:"structuredContent"`
				IsError bool           `json:"isError"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil || response.Result.IsError {
			t.Fatalf("stdio %s: %+v", tool, response)
		}
		return response.Result.Data
	}
	call("wingthing_capabilities", map[string]any{})
	directory := call("wing_list", map[string]any{})
	wingID := directory["wings"].([]any)[0].(map[string]any)["wing_id"].(string)
	shell := call("terminal_start", map[string]any{"command": []string{"/bin/sh"}, "cwd": childWork})["session"].(string)
	// Always stop actual fixture eggs, even when a later assertion fails.
	var sessions = []string{shell}
	var runs []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if client, err := controlsocket.Dial(ctx, state, controlsocket.Hello{Unsandboxed: true}); err == nil {
			for _, id := range runs {
				wire, _ := json.Marshal(map[string]any{"run_id": id})
				_, _, _ = client.Call(ctx, "agent_stop", wire)
			}
			_ = client.Close()
		}
		cfg := &config.Config{Dir: state}
		for _, id := range sessions {
			_ = eggclient.KillOrphanEggContext(ctx, cfg, id)
		}
	})
	call("terminal_send", map[string]any{"session": shell, "input": "printf 'fixture-cwd=%s\\n' \"$PWD\"", "enter": true})
	call("terminal_wait", map[string]any{"session": shell, "contains": "fixture-cwd=" + childWork, "timeout_seconds": 10})
	listed := call("terminal_list", map[string]any{})
	if len(listed["sessions"].([]any)) != 1 {
		t.Fatalf("shell not listed: %v", listed)
	}
	// Kill acknowledges the shell's exit before the egg finishes shutdown.
	// Wait for its endpoint cleanup before testing the restart inventory.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	shellDir := filepath.Join(state, "eggs", shell)
	if err := watcher.Add(shellDir); err != nil {
		t.Fatal(err)
	}
	call("terminal_stop", map[string]any{"session": shell})
	for {
		if _, err := os.Stat(filepath.Join(shellDir, "egg.pid")); os.IsNotExist(err) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		select {
		case <-watcher.Events:
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	_ = watcher.Close()
	run := call("agent_run", map[string]any{"prompt": "local request", "agent": "codex", "model": "fixture-model", "cwd": childWork, "timeout_seconds": 30})
	id, session := run["run_id"].(string), run["session_id"].(string)
	sessions = append(sessions, session)
	runs = append(runs, id)
	if run["cwd"] != childWork {
		t.Fatalf("run lost subdirectory cwd: %v", run)
	}
	if run["wing_id"] != wingID {
		t.Fatalf("run did not retain local wing identity: %v", run)
	}
	// Drop the MCP adapter while the provider holds its foreground turn.
	_ = input.Close()
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true
	client, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{Unsandboxed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// The FIFO open pairs with the provider only after UserPromptSubmit. No
	// timer or output-silence heuristic is used to decide when to release it.
	gate, err := os.OpenFile(gatePath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	meta, err := os.ReadFile(filepath.Join(state, "eggs", session, "egg.meta"))
	if err != nil || !strings.Contains(string(meta), "\ncwd="+childWork+"\n") {
		t.Fatalf("fake agent egg lost subdirectory cwd: %s, %v", meta, err)
	}
	ownerBefore := eggclient.ReadEggOwner(filepath.Join(state, "eggs", session))
	if ownerBefore == "" {
		t.Fatal("running egg has no local owner")
	}
	_ = client.Close()
	if err := wingProcesses[0].Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	// Preserve the completion for cleanup while replacing the entire wing
	// process, with the provider still held at the native completion barrier.
	firstExit := <-wingExits[0]
	wingExits[0] <- firstExit
	startWing()
	client, err = controlsocket.Dial(t.Context(), state, controlsocket.Hello{Unsandboxed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Welcome.WingID != wingID || eggclient.ReadEggOwner(filepath.Join(state, "eggs", session)) != ownerBefore {
		t.Fatal("built wing restart changed wing or egg ownership")
	}
	reclaimed, denied, err := client.Call(t.Context(), "terminal_list", json.RawMessage(`{}`))
	if err != nil || denied || len(reclaimed["sessions"].([]any)) != 1 || reclaimed["sessions"].([]any)[0].(map[string]any)["id"] != session {
		t.Fatalf("built local-only restart did not reclaim egg: %v denied=%v error=%v", reclaimed, denied, err)
	}
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	_ = gate.Close()
	wire, _ := json.Marshal(map[string]any{"run_id": id, "timeout_seconds": 30})
	if data, denied, err := client.Call(t.Context(), "agent_wait", wire); err != nil || denied || data["status"] != "done" {
		t.Fatalf("local-only native completion: %v denied=%v error=%v", data, denied, err)
	}
	wire, _ = json.Marshal(map[string]any{"run_id": id})
	result, denied, err := client.Call(t.Context(), "agent_result", wire)
	if err != nil || denied || result["output"] != "Fake Codex fixture-model: Ω🙂 local request" || result["turn_id"] != "fake-turn" {
		t.Fatalf("local-only native result: %v denied=%v error=%v", result, denied, err)
	}
	wire, _ = json.Marshal(map[string]any{"session": session})
	if _, denied, err := client.Call(t.Context(), "terminal_read", wire); err != nil || denied {
		t.Fatalf("completed agent egg is not attachable: denied=%v error=%v", denied, err)
	}
	if _, denied, err := client.Call(t.Context(), "terminal_stop", wire); err != nil || denied {
		t.Fatalf("stop local-only egg: denied=%v error=%v", denied, err)
	}
	for _, name := range []string{"device_token.yaml", "local_device_token.yaml"} {
		if _, err := os.Stat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatalf("account-free flow wrote a relay token %s: %v", name, err)
		}
	}
}

func TestBuiltWTLocalOnlyDaemonBootsWithoutTokens(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := localOnlyFixtureBinary(t, repo, t.TempDir())
	scratch := filepath.Join(repo, ".scratch")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, state := filepath.Join(root, "home"), filepath.Join(root, "s")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=/bin:/usr/bin"}
	start := exec.CommandContext(t.Context(), binary, "wing", "start", "--local-only", "--paths", home)
	start.Env = environment
	output, err := start.CombinedOutput()
	// If a later assertion fails, terminate only the daemon admitted into this
	// fixture's private state. It is the child launched by the command above.
	t.Cleanup(func() {
		if data, err := os.ReadFile(filepath.Join(state, "wing.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Signal(os.Interrupt)
				}
			}
		}
	})
	if err != nil || !strings.Contains(string(output), "local control: ready") {
		t.Fatalf("default local-only daemon start: %v\n%s", err, output)
	}
	client, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, tool := range []string{"wingthing_capabilities", "wing_list"} {
		if result, denied, err := client.Call(t.Context(), tool, json.RawMessage(`{}`)); err != nil || denied {
			t.Fatalf("default daemon %s: %v denied=%v error=%v", tool, result, denied, err)
		}
	}
	stop := exec.CommandContext(t.Context(), binary, "wing", "stop")
	stop.Env = environment
	if output, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop fixture daemon: %v\n%s", err, output)
	}
}
