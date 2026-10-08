package localmcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

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
