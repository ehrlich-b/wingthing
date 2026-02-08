package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// mockAgent implements agent.Agent for testing.
type mockAgent struct {
	contextWindow int
}

func (m *mockAgent) Run(_ context.Context, _ string, _ agent.RunOpts) (*agent.Stream, error) {
	return nil, nil
}
func (m *mockAgent) Health() error       { return nil }
func (m *mockAgent) ContextWindow() int  { return m.contextWindow }

// mockThread implements ThreadRenderer for testing.
type mockThread struct {
	content string
}

func (m *mockThread) Render(_ context.Context, _ *store.Store, _ time.Time, _ int) string {
	return m.content
}

func setupMemory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	write("index.md", "# Memory Index\n- identity: who you are\n- projects: active work\n")
	write("identity.md", "---\nname: Test User\nrole: engineer\n---\n# Identity\nI am a test user.\n")
	write("projects.md", "---\ntags: [work, deploy, pr]\n---\n# Projects\n## Current\n- Working on deploy pipeline\n")

	return dir
}

func setupSkills(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	skillContent := `---
name: test-skill
description: A test skill
agent: gemini
isolation: network
timeout: 60s
memory:
  - identity
  - projects
memory_write: false
tags: [test]
thread: true
---
# Test Skill

Hello {{identity.name}}, here is your context:

{{memory.projects}}

## Today
{{thread.summary}}

## Task
{{task.what}}
`
	if err := os.WriteFile(filepath.Join(dir, "test-skill.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func setupBuilder(t *testing.T, memDir, skillsDir string, threadContent string) (*Builder, *store.Store) {
	t.Helper()

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Register an agent
	s.UpsertAgent(&store.Agent{
		Name:             "claude",
		Adapter:          "claude",
		Command:          "claude",
		ContextWindow:    200000,
		DefaultIsolation: "standard",
	})
	s.UpsertAgent(&store.Agent{
		Name:             "gemini",
		Adapter:          "gemini",
		Command:          "gemini",
		ContextWindow:    100000,
		DefaultIsolation: "standard",
	})

	cfg := &config.Config{
		Dir:          filepath.Dir(memDir), // parent of memory
		DefaultAgent: "claude",
		Vars:         map[string]string{},
	}
	// Override to point at our temp dirs
	// config.SkillsDir() returns cfg.Dir + "/skills" so we need to match
	// We'll set Dir such that SkillsDir() returns skillsDir
	// Actually config.SkillsDir() = filepath.Join(c.Dir, "skills")
	// So set Dir to parent of skills dir, then create skills subdir
	// Easier: just set Dir to a temp dir and copy skills there
	wtDir := t.TempDir()
	os.MkdirAll(filepath.Join(wtDir, "skills"), 0755)
	os.MkdirAll(filepath.Join(wtDir, "memory"), 0755)

	// Copy skill files
	entries, _ := os.ReadDir(skillsDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(skillsDir, e.Name()))
		os.WriteFile(filepath.Join(wtDir, "skills", e.Name()), data, 0644)
	}

	cfg.Dir = wtDir

	mem := memory.New(memDir)

	var tr ThreadRenderer
	if threadContent != "" {
		tr = &mockThread{content: threadContent}
	}

	b := &Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: map[string]agent.Agent{
			"claude": &mockAgent{contextWindow: 200000},
			"gemini": &mockAgent{contextWindow: 100000},
		},
		Thread: tr,
	}

	return b, s
}

func TestBuildAdHocTask(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	b, s := setupBuilder(t, memDir, skillsDir, "")

	// Create an ad-hoc task
	task := &store.Task{
		ID:     "t-test-001",
		Type:   "prompt",
		What:   "did that deploy land?",
		RunAt:  time.Now(),
		Agent:  "claude",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-001")
	if err != nil {
		t.Fatal(err)
	}

	// Should use claude (default for ad-hoc)
	if result.Agent != "claude" {
		t.Errorf("agent = %q, want claude", result.Agent)
	}
	if result.Isolation != "standard" {
		t.Errorf("isolation = %q, want standard", result.Isolation)
	}

	// Should include identity memory (always for ad-hoc)
	hasIdentity := false
	for _, m := range result.MemoryLoaded {
		if m == "identity" {
			hasIdentity = true
		}
	}
	if !hasIdentity {
		t.Error("ad-hoc task should always load identity memory")
	}

	// Should include index (always)
	hasIndex := false
	for _, m := range result.MemoryLoaded {
		if m == "index" {
			hasIndex = true
		}
	}
	if !hasIndex {
		t.Error("should always load index memory")
	}

	// Should keyword-match projects.md (tag "deploy" matches "deploy" in prompt)
	hasProjects := false
	for _, m := range result.MemoryLoaded {
		if m == "projects" {
			hasProjects = true
		}
	}
	if !hasProjects {
		t.Error("should keyword-match projects memory for 'deploy' prompt")
	}

	// Prompt should contain the task text
	if !strings.Contains(result.Prompt, "did that deploy land?") {
		t.Error("prompt should contain the task text")
	}

	// Prompt should contain format docs
	if !strings.Contains(result.Prompt, "wt:schedule") {
		t.Error("prompt should contain format docs")
	}

	if result.BudgetUsed <= 0 {
		t.Error("budget used should be positive")
	}
	if result.BudgetTotal != 200000 {
		t.Errorf("budget total = %d, want 200000", result.BudgetTotal)
	}
}

func TestBuildSkillTask(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	b, s := setupBuilder(t, memDir, skillsDir, "08:00 - Did some morning work")

	// task.Agent set → CLI flag wins over skill's agent: gemini
	task := &store.Task{
		ID:     "t-test-002",
		Type:   "skill",
		What:   "test-skill",
		RunAt:  time.Now(),
		Agent:  "claude",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-002")
	if err != nil {
		t.Fatal(err)
	}

	// CLI flag (task.Agent=claude) wins over skill's agent: gemini
	if result.Agent != "claude" {
		t.Errorf("agent = %q, want claude (CLI flag wins over skill)", result.Agent)
	}
	// Skill says isolation: network (still applies, only agent was overridden)
	if result.Isolation != "network" {
		t.Errorf("isolation = %q, want network (from skill)", result.Isolation)
	}

	// Budget total should be claude's window since CLI flag won
	if result.BudgetTotal != 200000 {
		t.Errorf("budget total = %d, want 200000 (claude via CLI flag)", result.BudgetTotal)
	}

	// Should include identity and projects (skill-declared memory)
	for _, want := range []string{"identity", "projects"} {
		found := false
		for _, m := range result.MemoryLoaded {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("should load skill-declared memory %q", want)
		}
	}

	// Prompt should have interpolated identity.name
	if !strings.Contains(result.Prompt, "Test User") {
		t.Error("prompt should contain interpolated identity name")
	}

	// Prompt should contain thread content
	if !strings.Contains(result.Prompt, "morning work") {
		t.Error("prompt should contain thread content")
	}
}

func TestBuildSkillTaskNoAgentFlag(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	b, s := setupBuilder(t, memDir, skillsDir, "")

	// task.Agent empty → skill's agent: gemini wins
	task := &store.Task{
		ID:     "t-test-skill-noagent",
		Type:   "skill",
		What:   "test-skill",
		RunAt:  time.Now(),
		Agent:  "",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-skill-noagent")
	if err != nil {
		t.Fatal(err)
	}

	// No CLI flag → skill's agent: gemini wins
	if result.Agent != "gemini" {
		t.Errorf("agent = %q, want gemini (from skill, no CLI flag)", result.Agent)
	}
	if result.BudgetTotal != 100000 {
		t.Errorf("budget total = %d, want 100000 (gemini)", result.BudgetTotal)
	}
}

func TestBuildSkillTaskMountsAndTimeout(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := t.TempDir()

	skillContent := `---
name: mount-skill
description: A skill with mounts
isolation: network
timeout: 45s
mounts:
  - /data/input
  - /data/output
memory:
  - identity
---
# Mount Skill
Process files in mounted directories.
`
	if err := os.WriteFile(filepath.Join(skillsDir, "mount-skill.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	b, s := setupBuilder(t, memDir, skillsDir, "")

	task := &store.Task{
		ID:     "t-test-mounts",
		Type:   "skill",
		What:   "mount-skill",
		RunAt:  time.Now(),
		Agent:  "",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-mounts")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Mounts) != 2 {
		t.Fatalf("mounts = %v, want 2 entries", result.Mounts)
	}
	if result.Mounts[0] != "/data/input" || result.Mounts[1] != "/data/output" {
		t.Errorf("mounts = %v, want [/data/input /data/output]", result.Mounts)
	}
	if result.Timeout != 45*time.Second {
		t.Errorf("timeout = %v, want 45s", result.Timeout)
	}
	if result.Isolation != "network" {
		t.Errorf("isolation = %q, want network", result.Isolation)
	}
}

func TestBuildAdHocTaskNoMounts(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	b, s := setupBuilder(t, memDir, skillsDir, "")

	task := &store.Task{
		ID:     "t-test-nomounts",
		Type:   "prompt",
		What:   "hello",
		RunAt:  time.Now(),
		Agent:  "claude",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-nomounts")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Mounts) != 0 {
		t.Errorf("ad-hoc task should have no mounts, got %v", result.Mounts)
	}
	if result.Timeout != 120*time.Second {
		t.Errorf("timeout = %v, want default 120s", result.Timeout)
	}
}

func TestConfigPrecedence(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "claude",
		Vars:         map[string]string{},
	}

	// No skill, no CLI flag: use defaults
	rc := ResolveConfig(nil, "", "", cfg)
	if rc.Agent != "claude" {
		t.Errorf("no skill: agent = %q, want claude", rc.Agent)
	}
	if rc.Isolation != "standard" {
		t.Errorf("no skill: isolation = %q, want standard", rc.Isolation)
	}

	// Agent isolation override
	rc = ResolveConfig(nil, "", "strict", cfg)
	if rc.Isolation != "strict" {
		t.Errorf("agent override: isolation = %q, want strict", rc.Isolation)
	}

	// Skill overrides config defaults
	sk := &skill.Skill{
		Agent:     "gemini",
		Isolation: "network",
		Timeout:   "30s",
	}
	rc = ResolveConfig(sk, "", "strict", cfg)
	if rc.Agent != "gemini" {
		t.Errorf("skill override: agent = %q, want gemini", rc.Agent)
	}
	if rc.Isolation != "network" {
		t.Errorf("skill override: isolation = %q, want network", rc.Isolation)
	}
	if rc.Timeout != 30*time.Second {
		t.Errorf("skill override: timeout = %v, want 30s", rc.Timeout)
	}

	// CLI flag (taskAgent) wins over skill
	rc = ResolveConfig(sk, "ollama", "strict", cfg)
	if rc.Agent != "ollama" {
		t.Errorf("CLI override: agent = %q, want ollama", rc.Agent)
	}

	// Skill with empty fields: fall through to agent/config defaults
	sk2 := &skill.Skill{}
	rc = ResolveConfig(sk2, "", "strict", cfg)
	if rc.Agent != "claude" {
		t.Errorf("empty skill: agent = %q, want claude (config default)", rc.Agent)
	}
	if rc.Isolation != "strict" {
		t.Errorf("empty skill: isolation = %q, want strict (agent default)", rc.Isolation)
	}
}

func TestBuildTaskNotFound(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	b, _ := setupBuilder(t, memDir, skillsDir, "")

	_, err := b.Build(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestBuildWithThread(t *testing.T) {
	memDir := setupMemory(t)
	skillsDir := setupSkills(t)
	threadContent := "## 09:15 - PR Status Check\n- PR #892 merged\n"
	b, s := setupBuilder(t, memDir, skillsDir, threadContent)

	task := &store.Task{
		ID:     "t-test-003",
		Type:   "prompt",
		What:   "what happened today?",
		RunAt:  time.Now(),
		Agent:  "claude",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	result, err := b.Build(context.Background(), "t-test-003")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result.Prompt, "PR #892 merged") {
		t.Error("prompt should contain thread content")
	}
	if !strings.Contains(result.Prompt, "Today So Far") {
		t.Error("ad-hoc prompt should have 'Today So Far' section header")
	}
}

func TestFormatDocsContent(t *testing.T) {
	if !strings.Contains(FormatDocs, "wt:schedule") {
		t.Error("FormatDocs should mention wt:schedule")
	}
	if !strings.Contains(FormatDocs, "wt:memory") {
		t.Error("FormatDocs should mention wt:memory")
	}
}
