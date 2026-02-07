package timeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// mockAgent implements agent.Agent for testing.
type mockAgent struct {
	output    string
	healthErr error
	ctxWindow int
}

func (m *mockAgent) Run(_ context.Context, _ string, _ agent.RunOpts) (*agent.Stream, error) {
	return agent.NewTestStream(m.output), nil
}

func (m *mockAgent) Health() error {
	return m.healthErr
}

func (m *mockAgent) ContextWindow() int {
	if m.ctxWindow == 0 {
		return 200000
	}
	return m.ctxWindow
}

// nullThread implements orchestrator.ThreadRenderer.
type nullThread struct{}

func (nullThread) Render(_ context.Context, _ *store.Store, _ time.Time, _ int) string {
	return ""
}

func setupEngine(t *testing.T, ag *mockAgent) (*Engine, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Register the agent in the store
	s.UpsertAgent(&store.Agent{
		Name:    "test",
		Adapter: "test",
		Command: "test",
	})

	cfg := &config.Config{
		Dir:          t.TempDir(),
		DefaultAgent: "test",
		MachineID:    "test-machine",
	}

	memStore := memory.New(cfg.MemoryDir())
	agents := map[string]agent.Agent{"test": ag}

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: memStore,
		Config: cfg,
		Agents: agents,
		Thread: nullThread{},
	}

	eng := &Engine{
		Store:        s,
		Builder:      builder,
		Config:       cfg,
		Agents:       agents,
		PollInterval: 10 * time.Millisecond,
		MemoryDir:    cfg.MemoryDir(),
	}
	return eng, s
}

func TestDispatchSuccess(t *testing.T) {
	ag := &mockAgent{output: "Hello from the agent"}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:        "t-test-001",
		Type:      "prompt",
		What:      "say hello",
		RunAt:     time.Now().Add(-time.Second),
		Agent:     "test",
		Isolation: "standard",
		Status:    "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx := context.Background()
	if err := eng.poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}

	got, err := s.GetTask("t-test-001")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("status = %q, want done", got.Status)
	}
	if got.Output == nil || *got.Output != "Hello from the agent" {
		t.Errorf("output = %v, want 'Hello from the agent'", got.Output)
	}

	logs, _ := s.ListLogByTask("t-test-001")
	events := make(map[string]bool)
	for _, l := range logs {
		events[l.Event] = true
	}
	for _, want := range []string{"started", "prompt_built", "output_received", "markers_parsed", "thread_appended", "completed"} {
		if !events[want] {
			t.Errorf("missing log event %q", want)
		}
	}
}

func TestDispatchAgentUnhealthy(t *testing.T) {
	ag := &mockAgent{
		output:    "won't run",
		healthErr: fmt.Errorf("agent down"),
	}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-test-002",
		Type:  "prompt",
		What:  "say hello",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
	}
	s.CreateTask(task)

	eng.poll(context.Background())

	got, _ := s.GetTask("t-test-002")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "unhealthy") {
		t.Errorf("error = %v, want unhealthy message", got.Error)
	}
}

func TestDispatchEmptyOutput(t *testing.T) {
	ag := &mockAgent{output: ""}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-test-003",
		Type:  "prompt",
		What:  "say hello",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
	}
	s.CreateTask(task)

	eng.poll(context.Background())

	got, _ := s.GetTask("t-test-003")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "empty output") {
		t.Errorf("error = %v, want 'empty output'", got.Error)
	}
}

func TestDispatchScheduleDirective(t *testing.T) {
	output := "Build started.\n<!-- wt:schedule delay=10m -->Check build status<!-- /wt:schedule -->"
	ag := &mockAgent{output: output}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-test-004",
		Type:  "prompt",
		What:  "deploy",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
	}
	s.CreateTask(task)

	eng.poll(context.Background())

	got, _ := s.GetTask("t-test-004")
	if got.Status != "done" {
		t.Fatalf("status = %q, want done", got.Status)
	}

	// Check follow-up task was created
	pending, _ := s.ListPending(time.Now().Add(11 * time.Minute))
	found := false
	for _, p := range pending {
		if p.ParentID != nil && *p.ParentID == "t-test-004" {
			found = true
			if p.What != "Check build status" {
				t.Errorf("follow-up what = %q, want 'Check build status'", p.What)
			}
		}
	}
	if !found {
		t.Error("expected follow-up task to be created")
	}
}

func TestDispatchAgentNotFound(t *testing.T) {
	ag := &mockAgent{output: "x"}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-test-005",
		Type:  "prompt",
		What:  "test",
		RunAt: time.Now().Add(-time.Second),
		Agent: "nonexistent",
	}
	s.CreateTask(task)

	eng.poll(context.Background())

	got, _ := s.GetTask("t-test-005")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestRunCancelledContext(t *testing.T) {
	ag := &mockAgent{output: "x"}
	eng, _ := setupEngine(t, ag)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := eng.Run(ctx)
	if err != context.Canceled {
		t.Errorf("Run error = %v, want context.Canceled", err)
	}
}

func TestDispatchCronReschedule(t *testing.T) {
	ag := &mockAgent{output: "cron task done"}
	eng, s := setupEngine(t, ag)

	cronExpr := "*/5 * * * *"
	task := &store.Task{
		ID:    "t-test-cron",
		Type:  "prompt",
		What:  "recurring job",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
		Cron:  &cronExpr,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ctx := context.Background()
	if err := eng.poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}

	got, err := s.GetTask("t-test-cron")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("status = %q, want done", got.Status)
	}

	// Check that a follow-up recurring task was created
	pending, _ := s.ListPending(time.Now().Add(10 * time.Minute))
	found := false
	for _, p := range pending {
		if p.Cron != nil && *p.Cron == cronExpr && p.ParentID != nil && *p.ParentID == "t-test-cron" {
			found = true
			if p.What != "recurring job" {
				t.Errorf("follow-up what = %q, want 'recurring job'", p.What)
			}
		}
	}
	if !found {
		t.Error("expected cron follow-up task to be created")
	}
}

func TestDispatchThreadEntrySummaryTruncated(t *testing.T) {
	long := strings.Repeat("x", 300)
	ag := &mockAgent{output: long}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-test-006",
		Type:  "prompt",
		What:  "verbose",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
	}
	s.CreateTask(task)
	eng.poll(context.Background())

	entries, _ := s.ListRecentThread(1)
	if len(entries) == 0 {
		t.Fatal("expected thread entry")
	}
	if len(entries[0].Summary) != 200 {
		t.Errorf("summary length = %d, want 200", len(entries[0].Summary))
	}
}
