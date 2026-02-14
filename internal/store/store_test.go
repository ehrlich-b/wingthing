package store

import (
	"fmt"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Tasks ---

func TestCreateAndGetTask(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	task := &Task{
		ID:    "t-test-001",
		Type:  "prompt",
		What:  "hello world",
		RunAt: now,
		Agent: "claude",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetTask("t-test-001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil task")
	}
	if got.What != "hello world" {
		t.Errorf("what = %q, want %q", got.What, "hello world")
	}
	if got.Type != "prompt" {
		t.Errorf("type = %q, want %q", got.Type, "prompt")
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want %q", got.Status, "pending")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetTask("nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestListPending(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	tasks := []*Task{
		{ID: "t-1", Type: "prompt", What: "past", RunAt: now.Add(-time.Hour), Agent: "claude"},
		{ID: "t-2", Type: "prompt", What: "future", RunAt: now.Add(time.Hour), Agent: "claude"},
		{ID: "t-3", Type: "skill", What: "jira", RunAt: now.Add(-time.Minute), Agent: "claude"},
	}
	for _, task := range tasks {
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create %s: %v", task.ID, err)
		}
	}

	pending, err := s.ListPending(now)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending, want 2", len(pending))
	}
	if pending[0].ID != "t-1" {
		t.Errorf("first pending = %s, want t-1", pending[0].ID)
	}
	if pending[1].ID != "t-3" {
		t.Errorf("second pending = %s, want t-3", pending[1].ID)
	}
}

func TestListRecent(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 5; i++ {
		task := &Task{
			ID:    fmt.Sprintf("t-%d", i),
			Type:  "prompt",
			What:  fmt.Sprintf("task %d", i),
			RunAt: now,
			Agent: "claude",
		}
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	recent, err := s.ListRecent(3)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d recent, want 3", len(recent))
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	task := &Task{ID: "t-status", Type: "prompt", What: "test", RunAt: now, Agent: "claude"}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.UpdateTaskStatus("t-status", "running"); err != nil {
		t.Fatalf("update to running: %v", err)
	}
	got, _ := s.GetTask("t-status")
	if got.Status != "running" {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("started_at should be set")
	}

	if err := s.UpdateTaskStatus("t-status", "done"); err != nil {
		t.Fatalf("update to done: %v", err)
	}
	got, _ = s.GetTask("t-status")
	if got.Status != "done" {
		t.Errorf("status = %q, want done", got.Status)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set")
	}
}

func TestSetTaskOutput(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	task := &Task{ID: "t-out", Type: "prompt", What: "test", RunAt: now, Agent: "claude"}
	s.CreateTask(task)

	if err := s.SetTaskOutput("t-out", "the output"); err != nil {
		t.Fatalf("set output: %v", err)
	}
	got, _ := s.GetTask("t-out")
	if got.Output == nil || *got.Output != "the output" {
		t.Errorf("output = %v, want 'the output'", got.Output)
	}
}

func TestSetTaskError(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	task := &Task{ID: "t-err", Type: "prompt", What: "test", RunAt: now, Agent: "claude"}
	s.CreateTask(task)

	if err := s.SetTaskError("t-err", "something broke"); err != nil {
		t.Fatalf("set error: %v", err)
	}
	got, _ := s.GetTask("t-err")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || *got.Error != "something broke" {
		t.Errorf("error = %v, want 'something broke'", got.Error)
	}
}

// --- Thread ---

func TestAppendAndListThread(t *testing.T) {
	s := openTestStore(t)

	entry := &ThreadEntry{
		WingID: "test-machine",
		Summary:   "did a thing",
	}
	if err := s.AppendThread(entry); err != nil {
		t.Fatalf("append: %v", err)
	}
	if entry.ID == 0 {
		t.Error("expected non-zero ID after append")
	}

	entries, err := s.ListThreadByDate(time.Now().UTC())
	if err != nil {
		t.Fatalf("list by date: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Summary != "did a thing" {
		t.Errorf("summary = %q, want %q", entries[0].Summary, "did a thing")
	}
}

func TestListRecentThread(t *testing.T) {
	s := openTestStore(t)

	for i := 0; i < 5; i++ {
		s.AppendThread(&ThreadEntry{
			WingID: "test",
			Summary:   fmt.Sprintf("entry %d", i),
		})
	}

	recent, err := s.ListRecentThread(3)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("got %d, want 3", len(recent))
	}
}

func TestDeleteThreadOlderThan(t *testing.T) {
	s := openTestStore(t)

	old := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	s.AppendThreadAt(&ThreadEntry{WingID: "test", Summary: "old"}, old)
	s.AppendThreadAt(&ThreadEntry{WingID: "test", Summary: "new"}, recent)

	deleted, err := s.DeleteThreadOlderThan(cutoff)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d, want 1", deleted)
	}

	remaining, _ := s.ListRecentThread(10)
	if len(remaining) != 1 {
		t.Fatalf("remaining %d, want 1", len(remaining))
	}
	if remaining[0].Summary != "new" {
		t.Errorf("remaining = %q, want %q", remaining[0].Summary, "new")
	}
}

// --- Agents ---

func TestUpsertAndGetAgent(t *testing.T) {
	s := openTestStore(t)

	agent := &Agent{
		Name:          "claude",
		Adapter:       "claude",
		Command:       "claude",
		ContextWindow: 200000,
	}
	if err := s.UpsertAgent(agent); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetAgent("claude")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil agent")
	}
	if got.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want 200000", got.ContextWindow)
	}

	// upsert again with updated value
	agent.ContextWindow = 128000
	if err := s.UpsertAgent(agent); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, _ = s.GetAgent("claude")
	if got.ContextWindow != 128000 {
		t.Errorf("context_window after upsert = %d, want 128000", got.ContextWindow)
	}
}

func TestGetAgentNotFound(t *testing.T) {
	s := openTestStore(t)
	got, err := s.GetAgent("nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestListAgents(t *testing.T) {
	s := openTestStore(t)
	s.UpsertAgent(&Agent{Name: "claude", Adapter: "claude", Command: "claude", ContextWindow: 200000})
	s.UpsertAgent(&Agent{Name: "ollama", Adapter: "ollama", Command: "ollama run llama3.2", ContextWindow: 128000})

	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
}

func TestUpdateAgentHealth(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	s.UpsertAgent(&Agent{Name: "claude", Adapter: "claude", Command: "claude", ContextWindow: 200000})

	if err := s.UpdateAgentHealth("claude", true, now); err != nil {
		t.Fatalf("update health: %v", err)
	}
	got, _ := s.GetAgent("claude")
	if !got.Healthy {
		t.Error("expected healthy = true")
	}
}

// --- Log ---

func TestAppendAndListLog(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// need a task for FK
	s.CreateTask(&Task{ID: "t-log", Type: "prompt", What: "test", RunAt: now, Agent: "claude"})

	detail := "full prompt content here"
	if err := s.AppendLog("t-log", "prompt_built", &detail); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.AppendLog("t-log", "completed", nil); err != nil {
		t.Fatalf("append nil detail: %v", err)
	}

	entries, err := s.ListLogByTask("t-log")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Event != "prompt_built" {
		t.Errorf("event = %q, want prompt_built", entries[0].Event)
	}
	if entries[0].Detail == nil || *entries[0].Detail != detail {
		t.Errorf("detail = %v, want %q", entries[0].Detail, detail)
	}
	if entries[1].Detail != nil {
		t.Errorf("expected nil detail for second entry, got %v", entries[1].Detail)
	}
}

// --- Migration idempotency ---

func TestMigrationIdempotent(t *testing.T) {
	s := openTestStore(t)
	// Running migrate again should not fail
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// --- Schema verification ---

func TestAllTablesExist(t *testing.T) {
	s := openTestStore(t)
	tables := []string{"tasks", "thread_entries", "agents", "task_log", "schema_migrations"}
	for _, name := range tables {
		var count int
		err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count)
		if err != nil {
			t.Fatalf("check table %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("table %s not found", name)
		}
	}
}
