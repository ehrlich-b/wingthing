//go:build e2e

package integ

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/store"
)

type runFixture struct {
	binary, root, state, work string
	env                       []string
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		t.Fatal("run make gate GATE=agent-run")
	}
	root, err := os.MkdirTemp(os.Getenv("TMPDIR"), "ar-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &runFixture{binary: binary, root: root, state: filepath.Join(root, "s"), work: filepath.Join(root, "w")}
	for _, dir := range []string{f.work, filepath.Join(root, "h"), filepath.Join(root, "b"), filepath.Join(f.state, "memory")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.state, "memory", "index.md"), []byte("# Memory Index\n\nThis file is always loaded into every prompt.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The first completed message precedes the barrier. The second message and
	// artifact prove the provider kept working after its submitting host exited.
	script := `#!/bin/sh
if [ "$1" = --version ]; then echo codex-fixture; exit 0; fi
echo $$ > provider.pid
if [ "$1" != exec ]; then
    [ "$OPENAI_API_KEY" = 'egg-secret-canary' ] || exit 2
    touch secret.received
    while :; do sleep 1; done
fi
for arg; do prompt=$arg; done
printf '%s' "$prompt" > received-prompt
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"partial transcript"}}'
touch ready
while [ ! -f release ]; do sleep 0.05; done
if [ -f secret-output ]; then
    printf '{"type":"item.completed","item":{"type":"agent_message","text":"last provider message %s"}}\n' "$OPENAI_API_KEY"
    if [ -f secret-failure ]; then
        printf '{"type":"turn.failed","error":{"message":"provider refused %s"}}\n' "$OPENAI_API_KEY"
        printf 'provider stderr %s\n' "$OPENAI_API_KEY" >&2
        exit 1
    fi
fi
if [ -f refuse ]; then
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"last provider message"}}' '{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}'
    echo 'Reading additional input from stdin...' >&2
    exit 1
fi
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":" final transcript"}}' '{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":5}}'
touch completed
`
	if err := os.WriteFile(filepath.Join(root, "b", "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.env = []string{"HOME=" + filepath.Join(root, "h"), "WINGTHING_DIR=" + f.state, "PATH=" + filepath.Join(root, "b") + ":/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + os.Getenv("TMPDIR"), "LANG=en_US.UTF-8", "WT_PROVIDER_BASE_URL=http://127.0.0.1"}
	t.Cleanup(func() {
		// Failed assertions must not strand a detached fixture or its provider.
		if db, err := store.Open(filepath.Join(f.state, "wt.db")); err == nil {
			rows, err := db.DB().Query("SELECT runner_pid FROM tasks WHERE type = 'agent_run'")
			if err == nil {
				var pids []int
				for rows.Next() {
					var pid int
					if rows.Scan(&pid) == nil {
						pids = append(pids, pid)
					}
				}
				_ = rows.Close()
				for _, pid := range pids {
					argv, err := procinfo.ProcessArgv(pid)
					if err == nil && strings.Contains(strings.Join(argv, "\x00"), "supervise-run") && strings.Contains(strings.Join(argv, "\x00"), f.state) {
						_ = syscall.Kill(pid, syscall.SIGTERM)
						deadline := time.Now().Add(2 * time.Second)
						for procinfo.OwnedProcessIsAlive(pid) && time.Now().Before(deadline) {
							time.Sleep(20 * time.Millisecond)
						}
						if procinfo.OwnedProcessIsAlive(pid) {
							t.Errorf("fixture supervisor %d did not stop", pid)
						}
					}
				}
			}
			_ = db.Close()
		}
		if data, err := os.ReadFile(filepath.Join(f.work, "provider.pid")); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			argv, err := procinfo.ProcessArgv(pid)
			if pid > 0 && err == nil && strings.Contains(strings.Join(argv, "\x00"), filepath.Join(f.root, "b", "codex")) {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	return f
}

type runMCP struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	scanner *bufio.Scanner
	stderr  bytes.Buffer
	id      int
}

func (f *runFixture) host(t *testing.T, principal string, sandboxed bool) *runMCP {
	t.Helper()
	args := []string{"mcp", "stdio", "--client", principal}
	if !sandboxed {
		args = append(args, "--unsandboxed")
	}
	h := &runMCP{cmd: exec.Command(f.binary, args...)}
	h.cmd.Env = f.env
	h.cmd.Dir = f.root
	h.cmd.Stderr = &h.stderr
	var err error
	h.in, err = h.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.scanner = bufio.NewScanner(out)
	h.scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h.cmd.ProcessState == nil {
			_ = h.cmd.Process.Kill()
			_ = h.in.Close()
			_ = h.cmd.Wait()
		}
	})
	return h
}

func (h *runMCP) call(t *testing.T, name string, args any) (map[string]any, bool) {
	t.Helper()
	h.id++
	request := map[string]any{"jsonrpc": "2.0", "id": h.id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	if err := json.NewEncoder(h.in).Encode(request); err != nil {
		t.Fatal(err)
	}
	if !h.scanner.Scan() {
		t.Fatalf("MCP response missing: %v", h.scanner.Err())
	}
	var response struct {
		Error  any `json:"error"`
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(h.scanner.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("MCP protocol error: %#v", response.Error)
	}
	return response.Result.Structured, response.Result.IsError
}

func (h *runMCP) exit(t *testing.T, kill bool) {
	t.Helper()
	if kill {
		if err := h.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.in.Close()
	if err := h.cmd.Wait(); err != nil && !kill {
		t.Fatalf("MCP exit: %v %s", err, h.stderr.String())
	}
}

func (f *runFixture) task(t *testing.T, id string) *store.Task {
	t.Helper()
	s, err := store.Open(filepath.Join(f.state, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err := s.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("task: %#v %v", task, err)
	}
	return task
}

func waitRunFile(t *testing.T, path string, details ...func() any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(details) > 0 {
		t.Fatalf("provider did not create %s: %#v", path, details[0]())
	}
	t.Fatalf("provider did not create %s", path)
}

func TestMCPAgentRunSurvivesHostExit(t *testing.T) {
	for _, mode := range []string{"eof", "killed"} {
		t.Run(mode, func(t *testing.T) { testMCPAgentRunSurvivesHostExit(t, mode) })
	}
}

// Native enforcement belongs to e2e-mac (or the full integration profile on
// a capable Linux host), separately from the credential-free process gate.
func TestSandboxedMCPAgentRunSurvivesHostExit(t *testing.T) {
	testMCPAgentRunSurvivesHostExit(t, "sandboxed")
}

func testMCPAgentRunSurvivesHostExit(t *testing.T, mode string) {
	f := newRunFixture(t)
	h := f.host(t, "owner", mode == "sandboxed")
	label := "Continue after MCP host exit"
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "continue after host exit", "agent": "codex", "cwd": f.work, "label": label, "timeout_seconds": 10})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	wantIsolation := "privileged"
	if mode == "sandboxed" {
		wantIsolation = "standard"
	}
	if started["label"] != label || started["isolation"] != wantIsolation {
		t.Fatalf("creation metadata: %#v", started)
	}
	// Ensure any live fixture is stopped even if an assertion fails.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(f.work, "release"), nil, 0600) })
	waitRunFile(t, filepath.Join(f.work, "ready"), func() any { result, _ := h.call(t, "agent_result", map[string]any{"run_id": id}); return result })
	if prompt, err := os.ReadFile(filepath.Join(f.work, "received-prompt")); err != nil || string(prompt) != "continue after host exit" {
		t.Fatalf("worker prompt = %q, %v", prompt, err)
	}
	h.exit(t, mode == "killed")
	reconnected := f.host(t, "owner", mode == "sandboxed")
	partial, bad := reconnected.call(t, "agent_result", map[string]any{"run_id": id})
	if bad || partial["status"] != "running" || partial["ready"] != false || partial["output"] != "partial transcript" || partial["isolation"] != wantIsolation {
		t.Fatalf("reattached partial result: %#v", partial)
	}
	outsider := f.host(t, "outsider", false)
	for _, tool := range []string{"agent_status", "agent_result", "agent_events", "agent_stop"} {
		if data, bad := outsider.call(t, tool, map[string]any{"run_id": id}); !bad {
			t.Fatalf("outsider %s read run: %#v", tool, data)
		}
	}
	outsider.exit(t, false)
	if err := os.WriteFile(filepath.Join(f.work, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waited, bad := reconnected.call(t, "agent_wait", map[string]any{"run_id": id, "timeout_seconds": 5})
	if bad || waited["status"] != "done" {
		t.Fatalf("reattached wait: %#v", waited)
	}
	result, bad := reconnected.call(t, "agent_result", map[string]any{"run_id": id})
	if bad || result["output"] != "partial transcript final transcript" {
		t.Fatalf("reattached result: %#v", result)
	}
	events, bad := reconnected.call(t, "agent_events", map[string]any{"run_id": id})
	encoded, _ := json.Marshal(events)
	if bad || !bytes.Contains(encoded, []byte("partial transcript")) {
		t.Fatalf("reattached transcript: %s", encoded)
	}
	waitRunFile(t, filepath.Join(f.work, "completed"))
	reconnected.exit(t, false)
}

func TestMCPAgentRunPolicyDenialNamesRule(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt deny:/ masks mounted binaries; Linux uses an allowlist jail")
	}
	f := newRunFixture(t)
	if err := os.WriteFile(filepath.Join(f.work, "egg.yaml"), []byte("base: none\nfs: [deny:/, rw:./]\nnetwork: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := f.host(t, "owner", true)
	created, bad := h.call(t, "agent_run", map[string]any{"prompt": "must not execute", "agent": "codex", "cwd": f.work})
	if bad || created["isolation"] != "standard" {
		t.Fatalf("create denied run: %#v", created)
	}
	waited, bad := h.call(t, "agent_wait", map[string]any{"run_id": created["run_id"], "timeout_seconds": 5})
	if bad || waited["status"] != "failed" {
		t.Fatalf("denied run status: %#v", waited)
	}
	result, bad := h.call(t, "agent_result", map[string]any{"run_id": created["run_id"]})
	message, _ := result["error"].(string)
	for _, want := range []string{"egg.yaml", f.work, filepath.Join(f.root, "b", "codex"), "deny:/", "sandbox_explain"} {
		if bad || !strings.Contains(message, want) {
			t.Errorf("denied run missing %q: %#v", want, result)
		}
	}
	if _, err := os.Stat(filepath.Join(f.work, "provider.pid")); !os.IsNotExist(err) {
		t.Fatalf("denied provider started: %v", err)
	}
	h.exit(t, false)
}

func TestMCPAgentRunProviderRefusal(t *testing.T) {
	f := newRunFixture(t)
	for _, name := range []string{"refuse"} {
		if err := os.WriteFile(filepath.Join(f.work, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := f.host(t, "owner", false)
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "review", "agent": "codex", "cwd": f.work})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	waitRunFile(t, filepath.Join(f.work, "ready"))
	h.exit(t, false)
	h = f.host(t, "owner", false)
	if err := os.WriteFile(filepath.Join(f.work, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.call(t, "agent_wait", map[string]any{"run_id": id, "timeout_seconds": 5})
	result, bad := h.call(t, "agent_result", map[string]any{"run_id": id})
	if bad || result["status"] != "failed" || !strings.Contains(fmt.Sprint(result["error"]), "This content was flagged for possible cybersecurity risk.") || !strings.Contains(fmt.Sprint(result["output"]), "last provider message") {
		t.Fatalf("refusal result: %#v", result)
	}
	h.exit(t, false)
}

func TestEggSecretAbsentFromProcessArgv(t *testing.T) {
	f := newRunFixture(t)
	cmd := exec.Command(f.binary, "egg", "codex", "--detach", "--json", "--unsandboxed", "--cwd", f.work)
	cmd.Env = append(f.env, "OPENAI_API_KEY=egg-secret-canary")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("egg start: %v %s", err, output)
	}
	var started struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(output, &started); err != nil || started.Session == "" {
		t.Fatalf("egg result: %v %s", err, output)
	}
	t.Cleanup(func() {
		stop := exec.Command(f.binary, "egg", "stop", started.Session)
		stop.Env = f.env
		if out, err := stop.CombinedOutput(); err != nil {
			t.Errorf("stop fixture: %v %s", err, out)
		}
	})
	waitRunFile(t, filepath.Join(f.work, "secret.received"))
	dir := filepath.Join(f.state, "eggs", started.Session)
	data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	argv, err := procinfo.ProcessArgv(pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range argv {
		if strings.Contains(arg, "egg-secret-canary") || arg == "--env" {
			t.Fatal("egg environment leaked into argv")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".egg.env")); !os.IsNotExist(err) {
		t.Fatalf("secret payload survived read: %v", err)
	}
}

func TestMCPAgentRunStopAfterReconnect(t *testing.T) {
	f := newRunFixture(t)
	// State selectors may be relative to the submitting host's cwd. A later
	// client can select that same state through an absolute symlink alias.
	for i, value := range f.env {
		if strings.HasPrefix(value, "WINGTHING_DIR=") {
			f.env[i] = "WINGTHING_DIR=s"
		}
	}
	h := f.host(t, "owner", false)
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "wait", "agent": "codex", "cwd": f.work})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(f.work, "release"), nil, 0600) })
	waitRunFile(t, filepath.Join(f.work, "ready"))
	h.exit(t, false)
	alias := filepath.Join(f.root, "alias")
	if err := os.Symlink(f.state, alias); err != nil {
		t.Fatal(err)
	}
	for i, value := range f.env {
		if strings.HasPrefix(value, "WINGTHING_DIR=") {
			f.env[i] = "WINGTHING_DIR=" + alias
		}
	}
	h = f.host(t, "owner", false)
	stopped, bad := h.call(t, "agent_stop", map[string]any{"run_id": id})
	if bad || stopped["stopped"] != true || stopped["status"] != "failed" {
		t.Fatalf("reconnected stop: %#v", stopped)
	}
	result, bad := h.call(t, "agent_result", map[string]any{"run_id": id})
	if bad || !strings.Contains(fmt.Sprint(result["output"]), "partial transcript") || !strings.Contains(fmt.Sprint(result["error"]), "stopped by MCP principal owner") {
		t.Fatalf("stopped result: %#v", result)
	}
	h.exit(t, false)
}

func TestMCPAgentRunLostSupervisorKeepsTranscript(t *testing.T) {
	f := newRunFixture(t)
	h := f.host(t, "owner", false)
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "wait", "agent": "codex", "cwd": f.work})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(f.work, "release"), nil, 0600) })
	waitRunFile(t, filepath.Join(f.work, "ready"))
	task := f.task(t, id)
	if task.RunnerPID == h.cmd.Process.Pid {
		t.Fatal("run is still supervised by MCP host")
	}
	if err := syscall.Kill(task.RunnerPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	result, bad := h.call(t, "agent_wait", map[string]any{"run_id": id, "timeout_seconds": 5})
	if bad || result["status"] != "orphaned" {
		t.Fatalf("lost supervisor wait: %#v", result)
	}
	result, bad = h.call(t, "agent_result", map[string]any{"run_id": id})
	if bad || result["ready"] != true || result["output"] != "partial transcript" || !strings.Contains(fmt.Sprint(result["error"]), "provider exit unknown") {
		t.Fatalf("lost supervisor result: %#v", result)
	}
	h.exit(t, false)
}

func TestMCPAgentRunRedactsStoredAndReturnedSecrets(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			f := newRunFixture(t)
			const canary = "opaque-run-secret-canary-7Qn3"
			f.env = append(f.env, "OPENAI_API_KEY="+canary)
			for _, marker := range []string{"secret-output", "release"} {
				if err := os.WriteFile(filepath.Join(f.work, marker), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if failure {
				if err := os.WriteFile(filepath.Join(f.work, "secret-failure"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			h := f.host(t, "owner", false)
			started, bad := h.call(t, "agent_run", map[string]any{"prompt": "review", "agent": "codex", "cwd": f.work})
			if bad {
				t.Fatalf("start: %#v", started)
			}
			id := started["run_id"].(string)
			waited, bad := h.call(t, "agent_wait", map[string]any{"run_id": id, "timeout_seconds": 5})
			wantStatus := "done"
			if failure {
				wantStatus = "failed"
			}
			if bad || waited["status"] != wantStatus {
				t.Fatalf("wait: %#v", waited)
			}
			h.exit(t, false)
			// A new host must not depend on retaining the launch environment in memory.
			h = f.host(t, "owner", false)
			for _, tool := range []string{"agent_status", "agent_wait", "agent_result", "agent_events", "agent_stop"} {
				result, bad := h.call(t, tool, map[string]any{"run_id": id})
				data, err := json.Marshal(result)
				if bad || err != nil || bytes.Contains(data, []byte(canary)) {
					t.Errorf("%s exposed a credential or failed", tool)
				}
				if tool == "agent_result" && !strings.Contains(fmt.Sprint(result["output"]), "last provider message [redacted]") {
					t.Error("redaction lost the last provider message")
				}
			}
			task := f.task(t, id)
			data, err := json.Marshal(task)
			if err != nil || bytes.Contains(data, []byte(canary)) {
				t.Error("task storage exposed a credential")
			}
			db, err := store.Open(filepath.Join(f.state, "wt.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.DB().QueryRow("SELECT count(*) FROM task_log WHERE task_id = ? AND instr(COALESCE(detail, ''), ?) > 0", id, canary).Scan(&count); err != nil || count != 0 {
				t.Errorf("event storage exposed a credential: count=%d error=%v", count, err)
			}
			h.exit(t, false)
		})
	}
}

func TestMCPAgentRunReusedSupervisorPIDBecomesOrphaned(t *testing.T) {
	f := newRunFixture(t)
	h := f.host(t, "owner", false)
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "review", "agent": "codex", "cwd": f.work})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(f.work, "release"), nil, 0600) })
	waitRunFile(t, filepath.Join(f.work, "ready"))
	task := f.task(t, id)
	if task.RunnerIdentity == "" {
		t.Fatal("supervisor start identity was not saved")
	}
	h.exit(t, false)
	if err := syscall.Kill(task.RunnerPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(f.state, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB().Exec("UPDATE tasks SET runner_pid = ? WHERE id = ?", os.Getpid(), id); err != nil {
		t.Fatal(err)
	}
	h = f.host(t, "owner", false)
	for _, tool := range []string{"agent_status", "agent_wait", "agent_result", "agent_stop"} {
		result, bad := h.call(t, tool, map[string]any{"run_id": id})
		if bad || result["status"] != "orphaned" || result["timed_out"] == true {
			t.Fatalf("reused PID via %s: %#v", tool, result)
		}
	}
	if task := f.task(t, id); task.Status != "orphaned" || task.Output == nil || *task.Output != "partial transcript" {
		t.Fatalf("orphan lost its partial transcript: %#v", task)
	}
	h.exit(t, false)
}

func TestMCPAgentRunLegacySupervisorIdentity(t *testing.T) {
	f := newRunFixture(t)
	h := f.host(t, "owner", false)
	started, bad := h.call(t, "agent_run", map[string]any{"prompt": "review", "agent": "codex", "cwd": f.work})
	if bad {
		t.Fatalf("start: %#v", started)
	}
	id := started["run_id"].(string)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(f.work, "release"), nil, 0600) })
	waitRunFile(t, filepath.Join(f.work, "ready"))
	db, err := store.Open(filepath.Join(f.state, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB().Exec("UPDATE tasks SET runner_identity = '' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	h.exit(t, false)
	h = f.host(t, "owner", false)
	result, bad := h.call(t, "agent_status", map[string]any{"run_id": id})
	if bad || result["status"] != "running" {
		t.Fatalf("legacy supervisor incorrectly orphaned: %#v", result)
	}
	result, bad = h.call(t, "agent_stop", map[string]any{"run_id": id})
	if bad || result["stopped"] != true {
		t.Fatalf("verified legacy supervisor cannot be stopped: %#v", result)
	}
	h.exit(t, false)
}
