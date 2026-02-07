package timeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestPollSkipsBlockedDeps(t *testing.T) {
	ag := &mockAgent{output: "Hello from the agent"}
	eng, s := setupEngine(t, ag)

	now := time.Now()

	// Create the dependency task, leave it pending
	dep := &store.Task{
		ID:    "t-dep-block",
		Type:  "prompt",
		What:  "dependency",
		RunAt: now.Add(time.Hour), // future, won't be picked up
		Agent: "test",
	}
	if err := s.CreateTask(dep); err != nil {
		t.Fatalf("create dep: %v", err)
	}

	// Create a task that depends on the pending dep
	deps, _ := json.Marshal([]string{"t-dep-block"})
	depsStr := string(deps)
	task := &store.Task{
		ID:        "t-blocked-poll",
		Type:      "prompt",
		What:      "blocked task",
		RunAt:     now.Add(-time.Second),
		Agent:     "test",
		DependsOn: &depsStr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create blocked task: %v", err)
	}

	// Poll should find nothing ready (dep is future/pending, blocked task has unresolved dep)
	if err := eng.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	got, err := s.GetTask("t-blocked-poll")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (should not have been picked up)", got.Status)
	}
}

func TestPollRunsTaskAfterDepDone(t *testing.T) {
	ag := &mockAgent{output: "Dep resolved output"}
	eng, s := setupEngine(t, ag)

	now := time.Now()

	// Create and complete the dependency
	dep := &store.Task{
		ID:    "t-dep-resolved",
		Type:  "prompt",
		What:  "dependency done",
		RunAt: now.Add(-2 * time.Minute),
		Agent: "test",
	}
	if err := s.CreateTask(dep); err != nil {
		t.Fatalf("create dep: %v", err)
	}
	s.UpdateTaskStatus("t-dep-resolved", "done")

	// Create the dependent task
	deps, _ := json.Marshal([]string{"t-dep-resolved"})
	depsStr := string(deps)
	task := &store.Task{
		ID:        "t-unblocked-poll",
		Type:      "prompt",
		What:      "unblocked task",
		RunAt:     now.Add(-time.Second),
		Agent:     "test",
		DependsOn: &depsStr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := eng.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}

	got, err := s.GetTask("t-unblocked-poll")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("status = %q, want done", got.Status)
	}
}
