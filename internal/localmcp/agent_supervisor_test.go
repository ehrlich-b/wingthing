package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv("WT_TEST_LEGACY_MCP_HOST") == "1" {
		_, _ = os.Stdout.Write([]byte{1})
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	os.Exit(m.Run())
}

func TestAgentLegacyMCPHostRemainsRunningAndCannotBeStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	host := exec.CommandContext(ctx, os.Args[0], "mcp", "stdio", "--client", "owner", "--unsandboxed")
	host.Env = append(os.Environ(), "WT_TEST_LEGACY_MCP_HOST=1")
	input, err := host.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if err := host.Wait(); err != nil {
			t.Errorf("fixture MCP host was killed: %v", err)
		}
	})
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil {
		t.Fatal(err)
	}
	pid := host.Process.Pid
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "running", RunnerPID: pid})
	args := json.RawMessage(`{"run_id":"run"}`)
	status, err := s.toolAgentStatus(args)
	if err != nil || status["status"] != "running" {
		t.Errorf("live legacy host orphaned: %#v, %v", status, err)
	}
	result, err := s.toolAgentResult(args)
	if err != nil || result["status"] != "running" || result["ready"] != false {
		t.Errorf("legacy result reported terminal: %#v, %v", result, err)
	}
	waited, err := s.toolAgentWait(ctx, json.RawMessage(`{"run_id":"run","timeout_seconds":0.1}`))
	if err != nil || waited["status"] != "running" || waited["timed_out"] != true {
		t.Errorf("legacy wait reported terminal: %#v, %v", waited, err)
	}
	waitAny, err := s.toolAgentWaitAny(ctx, json.RawMessage(`{"run_ids":["run"],"timeout_seconds":0.1}`))
	if err != nil || len(waitAny["finished"].([]map[string]any)) != 0 || len(waitAny["pending"].([]string)) != 1 {
		t.Errorf("legacy wait_any reported terminal: %#v, %v", waitAny, err)
	}
	if _, err := s.toolAgentStop(args); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("MCP host PID %d", pid)) {
		t.Errorf("legacy stop did not refuse with owning host PID: %v", err)
	}
	if !procinfo.OwnedProcessIsAlive(pid) {
		t.Error("legacy stop killed its MCP host")
	}
	task, err := db.GetTask("run")
	if err != nil || task.Status != "running" || task.Error != nil {
		t.Fatalf("legacy tools changed the run: %#v, %v", task, err)
	}
}

func TestAgentSupervisorIdentityAllowsStateAliases(t *testing.T) {
	state := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"wt"}, agentSupervisorArgs("run", state)...)
	if !agentSupervisorArgvMatches(argv, "run", alias) {
		t.Fatal("same state directory alias rejected")
	}
	if agentSupervisorArgvMatches(argv, "other", alias) || agentSupervisorArgvMatches(argv, "run", t.TempDir()) {
		t.Fatal("different run or state accepted")
	}
}

func TestAgentWaitReconcilesSupervisorLostDuringWait(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "running", RunnerPID: os.Getpid()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- s.waitForAgentRunTerminal(ctx, "run") }()
	select {
	case err := <-finished:
		t.Fatalf("live run finished early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := db.DB().Exec("UPDATE tasks SET runner_pid = ? WHERE id = 'run'", 1<<30); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("wait missed a lost supervisor: %v", err)
	}
	task, err := db.GetTask("run")
	if err != nil || task.Status != "orphaned" {
		t.Fatalf("lost state: %#v %v", task, err)
	}
}

func TestDetachedAgentStopRefusesUnrelatedProcess(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "running", RunnerPID: os.Getpid()})
	_, err := s.toolAgentStop(json.RawMessage(`{"run_id":"run"}`))
	if err == nil || !strings.Contains(err.Error(), "verified Wingthing supervisor") {
		t.Fatalf("unrelated process accepted for signaling: %v", err)
	}
	task, err := db.GetTask("run")
	if err != nil || task.Status != "running" || task.Error != nil {
		t.Fatalf("unrelated process mutated run: %#v %v", task, err)
	}
}

func TestAgentSupervisorRejectsForeignAndDuplicateLaunches(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "pending", RunnerPID: os.Getpid()})
	for _, launch := range []agentRunLaunch{
		{Principal: "outsider", LauncherPID: os.Getpid()},
		{Principal: "owner", LauncherPID: 1 << 30},
	} {
		data, _ := json.Marshal(launch)
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		err = RunAgentSupervisor(context.Background(), s.Cfg, "run", strings.NewReader(string(data)), writer)
		_ = reader.Close()
		_ = writer.Close()
		if err == nil {
			t.Fatal("foreign or stale launcher claimed the run")
		}
		task, err := db.GetTask("run")
		if err != nil || task.RunnerPID != os.Getpid() || task.Status != "pending" {
			t.Fatalf("rejected launch changed run: %#v %v", task, err)
		}
	}
}

func TestAgentRunReconcilesReusedSupervisorPID(t *testing.T) {
	for _, tool := range []string{"status", "wait", "stop"} {
		t.Run(tool, func(t *testing.T) {
			// The recorded supervisor is gone; an unrelated same-UID process now
			// occupies its PID. Never signal that process or keep the run alive.
			s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "running", RunnerPID: 1 << 30})
			if _, err := db.DB().Exec("UPDATE tasks SET runner_pid = ? WHERE id = 'run'", os.Getpid()); err != nil {
				t.Fatal(err)
			}
			args := json.RawMessage(`{"run_id":"run"}`)
			var result map[string]any
			var err error
			switch tool {
			case "status":
				result, err = s.toolAgentStatus(args)
			case "wait":
				result, err = s.toolAgentWait(context.Background(), json.RawMessage(`{"run_id":"run","timeout_seconds":0.1}`))
			case "stop":
				result, err = s.toolAgentStop(args)
			}
			if err != nil || result["status"] != "orphaned" || result["timed_out"] == true {
				t.Errorf("reused supervisor PID via %s: %#v %v", tool, result, err)
			}
			task, err := db.GetTask("run")
			if err != nil || task.Status != "orphaned" || task.Error == nil || !strings.Contains(*task.Error, "provider exit unknown") {
				t.Errorf("reused PID did not persist orphaned state: %#v %v", task, err)
			}
		})
	}
}

func TestAgentRunRejectsMismatchedProcessStart(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: "running", RunnerPID: os.Getpid(), RunnerIdentity: "previous-process-start"})
	result, err := s.toolAgentStatus(json.RawMessage(`{"run_id":"run"}`))
	if err != nil || result["status"] != "orphaned" {
		t.Fatalf("mismatched start identity accepted: %#v %v", result, err)
	}
	task, err := db.GetTask("run")
	if err != nil || task.Status != "orphaned" {
		t.Fatalf("mismatched identity was not persisted: %#v %v", task, err)
	}
}

func TestAgentResultIncludesEventsAfterSnapshot(t *testing.T) {
	for _, status := range []string{"pending", "running", "orphaned", "failed", "timeout", "stopped", "done"} {
		for _, snapshot := range []string{"", "first", "first second third fourth"} {
			t.Run(status+"/"+snapshot, func(t *testing.T) {
				s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner", Status: status})
				if snapshot != "" {
					if err := db.SetTaskOutput("run", snapshot); err != nil {
						t.Fatal(err)
					}
				}
				for _, text := range []string{"first", " second", " third"} {
					if err := db.AppendLog("run", "agent_message", &text); err != nil {
						t.Fatal(err)
					}
				}
				want := snapshot
				if status != "done" && len(want) < len("first second third") {
					want = "first second third"
				}
				result, err := s.toolAgentResult(json.RawMessage(`{"run_id":"run"}`))
				if err != nil || result["ready"] != agentRunTerminal(status) || (want != "" && result["output"] != want) || (want == "" && result["output"] != nil) {
					t.Fatalf("result lost output: %#v, want %q (%v)", result, want, err)
				}
				stored, err := db.GetTask("run")
				if err != nil || (snapshot == "" && stored.Output != nil) || (snapshot != "" && (stored.Output == nil || *stored.Output != snapshot)) {
					t.Fatalf("reading result changed snapshot: %#v, %v", stored, err)
				}
			})
		}
	}
}

func TestAgentStatusAndResultExposeStructuredFailure(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner"})
	if err := db.SetTaskFailure("run", "provider_refused: codex exited with status 1", "provider_refused"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskProviderSession("run", "thread", "/fixture/rollout.jsonl"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"status", "result"} {
		var result map[string]any
		var err error
		if tool == "status" {
			result, err = s.toolAgentStatus(json.RawMessage(`{"run_id":"run"}`))
		} else {
			result, err = s.toolAgentResult(json.RawMessage(`{"run_id":"run"}`))
		}
		if err != nil || result["error_kind"] != "provider_refused" || result["thread_id"] != "thread" || result["rollout_path"] != "/fixture/rollout.jsonl" {
			t.Fatalf("%s lost failure/session metadata: %#v, %v", tool, result, err)
		}
	}
}
