package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCreateTaskWithDependsOn(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	deps, _ := json.Marshal([]string{"t-dep-001"})
	depsStr := string(deps)

	task := &Task{
		ID:        "t-with-deps",
		Type:      "prompt",
		What:      "depends on something",
		RunAt:     now,
		Agent:     "claude",
		DependsOn: &depsStr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetTask("t-with-deps")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DependsOn == nil {
		t.Fatal("depends_on should not be nil")
	}
	if *got.DependsOn != depsStr {
		t.Errorf("depends_on = %q, want %q", *got.DependsOn, depsStr)
	}
}

func TestListReadyNoDeps(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	task := &Task{
		ID:    "t-nodeps",
		Type:  "prompt",
		What:  "no dependencies",
		RunAt: now.Add(-time.Minute),
		Agent: "claude",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	ready, err := s.ListReady(now)
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("got %d ready, want 1", len(ready))
	}
	if ready[0].ID != "t-nodeps" {
		t.Errorf("ready[0].ID = %q, want t-nodeps", ready[0].ID)
	}
}

func TestListReadyDoneDeps(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Create the dependency task and mark it done
	dep := &Task{
		ID:    "t-dep-done",
		Type:  "prompt",
		What:  "dependency",
		RunAt: now.Add(-2 * time.Minute),
		Agent: "claude",
	}
	if err := s.CreateTask(dep); err != nil {
		t.Fatalf("create dep: %v", err)
	}
	if err := s.UpdateTaskStatus("t-dep-done", "done"); err != nil {
		t.Fatalf("update dep: %v", err)
	}

	// Create a task that depends on the done task
	deps, _ := json.Marshal([]string{"t-dep-done"})
	depsStr := string(deps)
	task := &Task{
		ID:        "t-after-done",
		Type:      "prompt",
		What:      "after done dep",
		RunAt:     now.Add(-time.Minute),
		Agent:     "claude",
		DependsOn: &depsStr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	ready, err := s.ListReady(now)
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("got %d ready, want 1", len(ready))
	}
	if ready[0].ID != "t-after-done" {
		t.Errorf("ready[0].ID = %q, want t-after-done", ready[0].ID)
	}
}

func TestListReadyPendingDeps(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Create the dependency task but leave it pending
	dep := &Task{
		ID:    "t-dep-pending",
		Type:  "prompt",
		What:  "still pending",
		RunAt: now.Add(-2 * time.Minute),
		Agent: "claude",
	}
	if err := s.CreateTask(dep); err != nil {
		t.Fatalf("create dep: %v", err)
	}

	// Create a task that depends on the pending task
	deps, _ := json.Marshal([]string{"t-dep-pending"})
	depsStr := string(deps)
	task := &Task{
		ID:        "t-blocked",
		Type:      "prompt",
		What:      "blocked by pending",
		RunAt:     now.Add(-time.Minute),
		Agent:     "claude",
		DependsOn: &depsStr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	ready, err := s.ListReady(now)
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	// Only the dep itself should be ready, not the blocked task
	for _, r := range ready {
		if r.ID == "t-blocked" {
			t.Error("t-blocked should not be ready while dependency is pending")
		}
	}
}

func TestListReadyDiamond(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Diamond: C depends on both A and B
	// A is done, B is pending -> C should not be ready
	a := &Task{ID: "t-a", Type: "prompt", What: "a", RunAt: now.Add(-3 * time.Minute), Agent: "claude"}
	b := &Task{ID: "t-b", Type: "prompt", What: "b", RunAt: now.Add(-2 * time.Minute), Agent: "claude"}
	s.CreateTask(a)
	s.CreateTask(b)
	s.UpdateTaskStatus("t-a", "done")

	deps, _ := json.Marshal([]string{"t-a", "t-b"})
	depsStr := string(deps)
	c := &Task{
		ID:        "t-c",
		Type:      "prompt",
		What:      "c",
		RunAt:     now.Add(-time.Minute),
		Agent:     "claude",
		DependsOn: &depsStr,
	}
	s.CreateTask(c)

	ready, err := s.ListReady(now)
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	for _, r := range ready {
		if r.ID == "t-c" {
			t.Error("t-c should not be ready while t-b is pending")
		}
	}

	// Now mark B as done, C should become ready
	s.UpdateTaskStatus("t-b", "done")
	ready, err = s.ListReady(now)
	if err != nil {
		t.Fatalf("list ready after b done: %v", err)
	}
	found := false
	for _, r := range ready {
		if r.ID == "t-c" {
			found = true
		}
	}
	if !found {
		t.Error("t-c should be ready after both deps are done")
	}
}
