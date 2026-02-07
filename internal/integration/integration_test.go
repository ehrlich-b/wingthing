package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/timeline"
	"github.com/ehrlich-b/wingthing/internal/transport"
)

// mockAgent implements agent.Agent with configurable responses.
type mockAgent struct {
	output     func(prompt string) string
	lastPrompt string
	mu         sync.Mutex
}

func (m *mockAgent) Run(ctx context.Context, prompt string, opts agent.RunOpts) (*agent.Stream, error) {
	m.mu.Lock()
	m.lastPrompt = prompt
	m.mu.Unlock()
	text := "mock output"
	if m.output != nil {
		text = m.output(prompt)
	}
	return agent.NewTestStream(text), nil
}

func (m *mockAgent) Health() error      { return nil }
func (m *mockAgent) ContextWindow() int { return 200000 }

func (m *mockAgent) getLastPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPrompt
}

// harness wires up all daemon components in a temp dir with a mock agent.
type harness struct {
	t      *testing.T
	dir    string
	cfg    *config.Config
	store  *store.Store
	client *transport.Client
	cancel context.CancelFunc
	mock   *mockAgent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "memory"), 0755)
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)

	// Seed memory files
	os.WriteFile(filepath.Join(dir, "memory", "index.md"), []byte("# Memory Index\nYou are wingthing.\n"), 0644)
	os.WriteFile(filepath.Join(dir, "memory", "identity.md"), []byte("---\nname: \"test-user\"\n---\n# Identity\nTest user.\n"), 0644)

	cfg := &config.Config{
		Dir:          dir,
		DefaultAgent: "mock",
		MachineID:    "test-machine",
		PollInterval: "100ms",
		Vars:         map[string]string{"HOME": dir, "WINGTHING_DIR": dir},
	}

	s, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}

	s.UpsertAgent(&store.Agent{
		Name:          "mock",
		Adapter:       "mock",
		Command:       "mock",
		ContextWindow: 200000,
	})

	mock := &mockAgent{}
	agents := map[string]agent.Agent{"mock": mock}
	mem := memory.New(cfg.MemoryDir())

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: agents,
	}

	engine := &timeline.Engine{
		Store:        s,
		Builder:      builder,
		Config:       cfg,
		Agents:       agents,
		PollInterval: 100 * time.Millisecond,
		MemoryDir:    cfg.MemoryDir(),
	}

	srv := transport.NewServer(s, agents, cfg.SocketPath())

	ctx, cancel := context.WithCancel(context.Background())

	go engine.Run(ctx)
	go srv.ListenAndServe(ctx)

	// Wait for socket to be ready
	time.Sleep(200 * time.Millisecond)

	client := transport.NewClient(cfg.SocketPath())

	h := &harness{
		t:      t,
		dir:    dir,
		cfg:    cfg,
		store:  s,
		client: client,
		cancel: cancel,
		mock:   mock,
	}

	t.Cleanup(func() {
		cancel()
		s.Close()
		time.Sleep(100 * time.Millisecond)
	})

	return h
}

func (h *harness) waitForTask(taskID string, timeout time.Duration) *store.Task {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := h.store.GetTask(taskID)
		if err != nil {
			h.t.Fatal(err)
		}
		if task != nil && (task.Status == "done" || task.Status == "failed") {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("task %s did not complete within %v", taskID, timeout)
	return nil
}

// Test 1: wt "hello" -> task runs, thread updated, log recorded.
func TestTaskLifecycle(t *testing.T) {
	h := newHarness(t)

	h.mock.output = func(prompt string) string {
		return "Hello from wingthing!"
	}

	resp, err := h.client.SubmitTask(transport.SubmitTaskRequest{
		What:  "say hello",
		Type:  "prompt",
		Agent: "mock",
	})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	task := h.waitForTask(resp.ID, 5*time.Second)
	if task.Status != "done" {
		var errMsg string
		if task.Error != nil {
			errMsg = *task.Error
		}
		t.Fatalf("expected status done, got %s (error: %s)", task.Status, errMsg)
	}

	// Verify output saved
	if task.Output == nil || *task.Output != "Hello from wingthing!" {
		t.Fatalf("expected output 'Hello from wingthing!', got %v", task.Output)
	}

	// Verify thread entry created
	entries, err := h.store.ListThreadByDate(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected thread entry, got none")
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Summary, "Hello from wingthing!") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("thread entry does not contain expected summary")
	}

	// Verify log events
	logs, err := h.store.ListLogByTask(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
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

// Test 2: install skill -> wt --skill greet -> memory loaded, template interpolated, output parsed.
func TestSkillTask(t *testing.T) {
	h := newHarness(t)

	skillContent := `---
name: greet
description: Greet the user
agent: mock
memory:
  - index
memory_write: false
---
You are a greeter. Memory says: {{memory.index}}
The user wants: {{task.what}}
Their name is: {{identity.name}}
`
	os.WriteFile(filepath.Join(h.dir, "skills", "greet.md"), []byte(skillContent), 0644)

	h.mock.output = func(prompt string) string {
		return "Greetings, test-user!"
	}

	resp, err := h.client.SubmitTask(transport.SubmitTaskRequest{
		What:  "greet",
		Type:  "skill",
		Agent: "mock",
	})
	if err != nil {
		t.Fatalf("submit skill task: %v", err)
	}

	task := h.waitForTask(resp.ID, 5*time.Second)
	if task.Status != "done" {
		var errMsg string
		if task.Error != nil {
			errMsg = *task.Error
		}
		t.Fatalf("expected status done, got %s (error: %s)", task.Status, errMsg)
	}

	prompt := h.mock.getLastPrompt()

	// Verify memory was loaded into prompt
	if !strings.Contains(prompt, "Memory Index") {
		t.Errorf("prompt should contain memory index content, got:\n%s", prompt[:min(len(prompt), 500)])
	}

	// Verify skill template present
	if !strings.Contains(prompt, "You are a greeter") {
		t.Errorf("prompt should contain skill body")
	}

	// Verify identity interpolation
	if !strings.Contains(prompt, "test-user") {
		t.Errorf("prompt should contain interpolated identity name")
	}

	// Verify task.what interpolation
	if !strings.Contains(prompt, "greet") {
		t.Errorf("prompt should contain interpolated task.what")
	}
}

// Test 3: agent schedules follow-up via wt:schedule -> follow-up task created and executes.
func TestFollowUpScheduling(t *testing.T) {
	h := newHarness(t)

	var callCount int
	var mu sync.Mutex
	h.mock.output = func(prompt string) string {
		mu.Lock()
		n := callCount
		callCount++
		mu.Unlock()
		if n == 0 {
			return "Deploying now.\n<!-- wt:schedule delay=\"1s\" -->\ncheck the build status\n<!-- /wt:schedule -->"
		}
		return "Build is green!"
	}

	resp, err := h.client.SubmitTask(transport.SubmitTaskRequest{
		What:  "deploy the app",
		Type:  "prompt",
		Agent: "mock",
	})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for first task to complete
	task := h.waitForTask(resp.ID, 5*time.Second)
	if task.Status != "done" {
		var errMsg string
		if task.Error != nil {
			errMsg = *task.Error
		}
		t.Fatalf("first task: expected done, got %s (error: %s)", task.Status, errMsg)
	}

	// Find the follow-up task
	var followUp *store.Task
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := h.store.ListRecent(10)
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range tasks {
			if tt.ParentID != nil && *tt.ParentID == resp.ID {
				followUp = tt
				break
			}
		}
		if followUp != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if followUp == nil {
		t.Fatal("follow-up task not created")
	}
	if followUp.What != "check the build status" {
		t.Errorf("follow-up what = %q, want %q", followUp.What, "check the build status")
	}

	// Wait for follow-up to execute (1s delay + poll time)
	completed := h.waitForTask(followUp.ID, 5*time.Second)
	if completed.Status != "done" {
		var errMsg string
		if completed.Error != nil {
			errMsg = *completed.Error
		}
		t.Fatalf("follow-up: expected done, got %s (error: %s)", completed.Status, errMsg)
	}
	if completed.Output == nil || *completed.Output != "Build is green!" {
		t.Errorf("follow-up output = %v, want 'Build is green!'", completed.Output)
	}
}
