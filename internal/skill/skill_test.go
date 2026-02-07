package skill

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSkill = `---
name: jira-briefing
description: Morning Jira briefing
agent: claude
isolation: network
mounts:
  - $JIRA_DIR:ro
  - $HOME/.config:ro
timeout: 120s
memory:
  - identity
  - projects
memory_write: false
schedule: "0 8 * * 1-5"
tags: [jira, work]
thread: true
---
You are {{identity.name}}'s Jira assistant.

{{memory.identity}}

{{memory.projects}}

## Today's thread
{{thread.summary}}

## Task
{{task.what}}
`

func TestParseSkill(t *testing.T) {
	s, err := Parse(sampleSkill)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "jira-briefing" {
		t.Errorf("name = %q, want jira-briefing", s.Name)
	}
	if s.Description != "Morning Jira briefing" {
		t.Errorf("description = %q", s.Description)
	}
	if s.Agent != "claude" {
		t.Errorf("agent = %q", s.Agent)
	}
	if s.Isolation != "network" {
		t.Errorf("isolation = %q", s.Isolation)
	}
	if len(s.Mounts) != 2 {
		t.Fatalf("mounts len = %d, want 2", len(s.Mounts))
	}
	if s.Mounts[0] != "$JIRA_DIR:ro" {
		t.Errorf("mounts[0] = %q", s.Mounts[0])
	}
	if s.Timeout != "120s" {
		t.Errorf("timeout = %q", s.Timeout)
	}
	if len(s.Memory) != 2 || s.Memory[0] != "identity" || s.Memory[1] != "projects" {
		t.Errorf("memory = %v", s.Memory)
	}
	if s.MemoryWrite {
		t.Error("memory_write should be false")
	}
	if s.Schedule != "0 8 * * 1-5" {
		t.Errorf("schedule = %q", s.Schedule)
	}
	if len(s.Tags) != 2 || s.Tags[0] != "jira" || s.Tags[1] != "work" {
		t.Errorf("tags = %v", s.Tags)
	}
	if !s.Thread {
		t.Error("thread should be true")
	}
	if s.Body == "" {
		t.Fatal("body should not be empty")
	}
}

func TestLoadSkillFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte(sampleSkill), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Name != "jira-briefing" {
		t.Errorf("name = %q", s.Name)
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	_, err := Parse("just some text without frontmatter")
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseNoClosingFence(t *testing.T) {
	_, err := Parse("---\nname: test\n")
	if err == nil {
		t.Fatal("expected error for missing closing fence")
	}
}

func TestParseEmptyBody(t *testing.T) {
	s, err := Parse("---\nname: minimal\n---\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "minimal" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Body != "" {
		t.Errorf("body = %q, want empty", s.Body)
	}
}

// --- Interpolation ---

func TestInterpolate(t *testing.T) {
	body := "Hello {{identity.name}}, here is {{memory.projects}}. Thread: {{thread.summary}}. Task: {{task.what}}."
	data := InterpolateData{
		Memory:   map[string]string{"projects": "project list here"},
		Identity: map[string]string{"name": "Bryan"},
		Thread:   "morning briefing done",
		Task:     "check PR status",
	}

	result, warnings := Interpolate(body, data)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	expected := "Hello Bryan, here is project list here. Thread: morning briefing done. Task: check PR status."
	if result != expected {
		t.Errorf("result = %q, want %q", result, expected)
	}
}

func TestInterpolateMissing(t *testing.T) {
	body := "{{memory.nonexistent}} and {{identity.missing}}"
	data := InterpolateData{
		Memory:   map[string]string{},
		Identity: map[string]string{},
	}

	result, warnings := Interpolate(body, data)
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2", len(warnings))
	}
	if result != " and " {
		t.Errorf("result = %q, want ' and '", result)
	}
}

func TestInterpolateUnrecognized(t *testing.T) {
	body := "{{custom.thing}} stays as-is"
	data := InterpolateData{}

	result, warnings := Interpolate(body, data)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if result != "{{custom.thing}} stays as-is" {
		t.Errorf("result = %q", result)
	}
}

func TestInterpolateNoNamespace(t *testing.T) {
	body := "{{nodot}} stays"
	data := InterpolateData{}

	result, warnings := Interpolate(body, data)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if result != "{{nodot}} stays" {
		t.Errorf("result = %q", result)
	}
}

// --- Variable resolution ---

func TestResolveVars(t *testing.T) {
	mounts := []string{"$JIRA_DIR:ro", "$HOME/.config:ro", "/fixed/path:rw"}
	vars := map[string]string{
		"JIRA_DIR": "/Users/bryan/repos/jira",
		"HOME":     "/Users/bryan",
	}

	got := ResolveVars(mounts, vars)
	want := []string{
		"/Users/bryan/repos/jira:ro",
		"/Users/bryan/.config:ro",
		"/fixed/path:rw",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveVarsNoMatch(t *testing.T) {
	mounts := []string{"$UNKNOWN:ro"}
	vars := map[string]string{}

	got := ResolveVars(mounts, vars)
	if got[0] != "$UNKNOWN:ro" {
		t.Errorf("got = %q, want $UNKNOWN:ro", got[0])
	}
}

func TestResolveVarsEmpty(t *testing.T) {
	got := ResolveVars(nil, nil)
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}
