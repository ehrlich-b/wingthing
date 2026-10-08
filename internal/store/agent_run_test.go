package store

import (
	"strings"
	"testing"
)

func TestAgentSupervisorClaimAndOrphanRaces(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	partial, knownError := "partial transcript", "provider exit status 1"
	task := &Task{ID: "run", Type: "agent_run", Status: "pending", RunnerPID: 10, Output: &partial, Error: &knownError}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskOutput(task.ID, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("UPDATE tasks SET error = ? WHERE id = ?", knownError, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAgentRunSupervisor(task.ID, 10, 20, "supervisor-start"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAgentRunSupervisor(task.ID, 10, 30, "supervisor-start"); err == nil {
		t.Fatal("duplicate supervisor claimed the same run")
	}
	if err := s.MarkAgentRunOrphaned(task.ID, 10, "wrong supervisor"); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetTask(task.ID)
	if err != nil || current.Status != "pending" || current.RunnerPID != 20 || current.RunnerIdentity != "supervisor-start" {
		t.Fatalf("stale supervisor changed run: %#v %v", current, err)
	}
	if err := s.MarkAgentRunOrphaned(task.ID, 20, "supervisor lost; provider exit unknown"); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetTask(task.ID)
	if err != nil || current.Status != "orphaned" || current.FinishedAt == nil || current.Output == nil || *current.Output != partial || current.Error == nil || !strings.Contains(*current.Error, knownError) || !strings.Contains(*current.Error, "provider exit unknown") {
		t.Fatalf("orphan lost evidence: %#v %v", current, err)
	}
	if err := s.ClaimAgentRunSupervisor(task.ID, 20, 30, "supervisor-start"); err == nil {
		t.Fatal("terminal run was restarted")
	}
	if err := s.UpdateTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAgentRunOrphaned(task.ID, 20, "stale liveness probe"); err != nil {
		t.Fatal(err)
	}
	current, err = s.GetTask(task.ID)
	if err != nil || current.Status != "done" || strings.Contains(*current.Error, "stale liveness probe") {
		t.Fatalf("orphan probe overwrote completion: %#v %v", current, err)
	}
	if err := s.CreateTask(&Task{ID: "prompt", Type: "prompt", RunnerPID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAgentRunSupervisor("prompt", 10, 20, "supervisor-start"); err == nil {
		t.Fatal("supervisor claimed a non-agent task")
	}
}

func TestAgentRunOrphanPreservesLongestOutput(t *testing.T) {
	for _, snapshot := range []string{"", "first", "first second third", "first second third fourth", strings.Repeat("界", 7)} {
		t.Run(snapshot, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			task := &Task{ID: "run", Type: "agent_run", Status: "running", RunnerPID: 10}
			if err := s.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			if snapshot != "" {
				if err := s.SetTaskOutput(task.ID, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			for _, text := range []string{"first", " second", " third"} {
				if err := s.AppendLog(task.ID, "agent_message", &text); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.MarkAgentRunOrphaned(task.ID, 10, "supervisor lost; provider exit unknown"); err != nil {
				t.Fatal(err)
			}
			want := "first second third"
			if len(snapshot) > len(want) {
				want = snapshot
			}
			current, err := s.GetTask(task.ID)
			if err != nil || current.Output == nil || *current.Output != want {
				t.Fatalf("orphan lost output: %#v, want %q (%v)", current, want, err)
			}
		})
	}
}

func TestTaskProviderFailureAndSessionRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := &Task{ID: "failure", Type: "agent_run", ErrorKind: "provider_refused", ProviderThreadID: "thread", ProviderRolloutPath: "/fixture/rollout.jsonl"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskFailure(task.ID, "provider_refused: codex exited with status 1", "provider_refused"); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetTask(task.ID)
	if err != nil || current.ErrorKind != task.ErrorKind || current.ProviderThreadID != task.ProviderThreadID || current.ProviderRolloutPath != task.ProviderRolloutPath {
		t.Fatalf("provider metadata lost: %#v %v", current, err)
	}
	tasks, err := s.ListRecent(1)
	if err != nil || len(tasks) != 1 || tasks[0].ErrorKind != task.ErrorKind || tasks[0].ProviderThreadID != task.ProviderThreadID {
		t.Fatalf("listed provider metadata lost: %#v %v", tasks, err)
	}
}
