package transport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func setup(t *testing.T) (*store.Store, *Client, context.CancelFunc) {
	t.Helper()

	// Reset task counter for deterministic IDs within each test.
	atomic.StoreUint64(&taskCounter, 0)

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "wt.sock")
	srv := NewServer(s, sock)

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	go func() {
		// Signal readiness once socket exists.
		go func() {
			for {
				if _, err := os.Stat(sock); err == nil {
					close(ready)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
		srv.ListenAndServe(ctx)
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server did not start in time")
	}

	client := NewClient(sock)
	return s, client, func() {
		cancel()
		s.Close()
	}
}

func TestCreateAndGetTask(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	created, err := client.SubmitTask(SubmitTaskRequest{What: "hello world"})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	if created.What != "hello world" {
		t.Errorf("want what=hello world, got %s", created.What)
	}
	if created.Status != "pending" {
		t.Errorf("want status=pending, got %s", created.Status)
	}
	if created.Type != "prompt" {
		t.Errorf("want type=prompt, got %s", created.Type)
	}
	if created.Agent != "claude" {
		t.Errorf("want agent=claude, got %s", created.Agent)
	}

	got, err := client.GetTask(created.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("want id=%s, got %s", created.ID, got.ID)
	}
}

func TestCreateTaskWithOptions(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	created, err := client.SubmitTask(SubmitTaskRequest{
		What:  "run jira",
		Type:  "skill",
		Agent: "gemini",
		RunAt: "2026-03-01T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	if created.Type != "skill" {
		t.Errorf("want type=skill, got %s", created.Type)
	}
	if created.Agent != "gemini" {
		t.Errorf("want agent=gemini, got %s", created.Agent)
	}
	if created.RunAt != "2026-03-01T10:00:00Z" {
		t.Errorf("want run_at=2026-03-01T10:00:00Z, got %s", created.RunAt)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	_, err := client.SubmitTask(SubmitTaskRequest{})
	if err == nil {
		t.Fatal("expected error for empty what")
	}
}

func TestListTasks(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		_, err := client.SubmitTask(SubmitTaskRequest{What: "task"})
		if err != nil {
			t.Fatalf("submit task %d: %v", i, err)
		}
	}

	tasks, err := client.ListTasks("", 0)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("want 3 tasks, got %d", len(tasks))
	}
}

func TestListTasksWithStatusFilter(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	created, _ := client.SubmitTask(SubmitTaskRequest{What: "will fail"})
	s.SetTaskError(created.ID, "something broke")

	client.SubmitTask(SubmitTaskRequest{What: "still pending"})

	tasks, err := client.ListTasks("failed", 0)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("want 1 failed task, got %d", len(tasks))
	}
	if len(tasks) > 0 && tasks[0].ID != created.ID {
		t.Errorf("want id=%s, got %s", created.ID, tasks[0].ID)
	}
}

func TestListTasksWithLimit(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		client.SubmitTask(SubmitTaskRequest{What: "task"})
	}

	tasks, err := client.ListTasks("", 2)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("want 2 tasks, got %d", len(tasks))
	}
}

func TestGetTaskNotFound(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	_, err := client.GetTask("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestRetryTask(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	created, _ := client.SubmitTask(SubmitTaskRequest{What: "will fail"})
	s.SetTaskError(created.ID, "bad thing happened")

	retried, err := client.RetryTask(created.ID)
	if err != nil {
		t.Fatalf("retry task: %v", err)
	}
	if retried.Status != "pending" {
		t.Errorf("want status=pending after retry, got %s", retried.Status)
	}
	if retried.Error != nil {
		t.Errorf("want nil error after retry, got %v", retried.Error)
	}
}

func TestRetryTaskNotFailed(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	created, _ := client.SubmitTask(SubmitTaskRequest{What: "pending task"})

	_, err := client.RetryTask(created.ID)
	if err == nil {
		t.Fatal("expected error when retrying non-failed task")
	}
}

func TestGetThread(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	now := time.Now().UTC()
	entry := &store.ThreadEntry{
		MachineID: "test",
		Summary:   "Did something cool",
	}
	agent := "claude"
	entry.Agent = &agent
	s.AppendThreadAt(entry, now)

	rendered, err := client.GetThread(now.Format("2006-01-02"), 0)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if rendered == "" {
		t.Error("expected non-empty thread")
	}
}

func TestGetThreadEmpty(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	rendered, err := client.GetThread("2020-01-01", 0)
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if rendered != "" {
		t.Errorf("expected empty thread, got %q", rendered)
	}
}

func TestListAgents(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	s.UpsertAgent(&store.Agent{
		Name:             "claude",
		Adapter:          "claude",
		Command:          "claude",
		ContextWindow:    200000,
		DefaultIsolation: "standard",
		Healthy:          true,
	})

	agents, err := client.ListAgents()
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	var a struct {
		Name    string `json:"name"`
		Healthy bool   `json:"healthy"`
	}
	json.Unmarshal(agents[0], &a)
	if a.Name != "claude" {
		t.Errorf("want name=claude, got %s", a.Name)
	}
	if !a.Healthy {
		t.Error("want healthy=true")
	}
}

func TestStatus(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	client.SubmitTask(SubmitTaskRequest{What: "task1"})
	client.SubmitTask(SubmitTaskRequest{What: "task2"})
	s.UpsertAgent(&store.Agent{
		Name:             "claude",
		Adapter:          "claude",
		Command:          "claude",
		ContextWindow:    200000,
		DefaultIsolation: "standard",
	})

	status, err := client.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Pending != 2 {
		t.Errorf("want pending=2, got %d", status.Pending)
	}
	if status.Running != 0 {
		t.Errorf("want running=0, got %d", status.Running)
	}
	if status.Agents != 1 {
		t.Errorf("want agents=1, got %d", status.Agents)
	}
}

func TestGetLog(t *testing.T) {
	s, client, cleanup := setup(t)
	defer cleanup()

	created, _ := client.SubmitTask(SubmitTaskRequest{What: "logged task"})
	detail := "full prompt text here"
	s.AppendLog(created.ID, "prompt_built", &detail)
	s.AppendLog(created.ID, "completed", nil)

	entries, err := client.GetLog(created.ID)
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 log entries, got %d", len(entries))
	}
	var e struct {
		Event  string  `json:"event"`
		Detail *string `json:"detail"`
	}
	json.Unmarshal(entries[0], &e)
	if e.Event != "prompt_built" {
		t.Errorf("want event=prompt_built, got %s", e.Event)
	}
	if e.Detail == nil || *e.Detail != detail {
		t.Errorf("want detail=%q, got %v", detail, e.Detail)
	}
}

func TestGetLogEmpty(t *testing.T) {
	_, client, cleanup := setup(t)
	defer cleanup()

	entries, err := client.GetLog("nonexistent")
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want 0 entries, got %d", len(entries))
	}
}
