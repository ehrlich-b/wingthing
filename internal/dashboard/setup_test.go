package dashboard

import (
	"context"
	"github.com/ehrlich-b/wingthing/internal/onboarding"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"strings"
	"testing"
	"time"
)

func setupFixture() *state {
	return &state{machines: map[string]machine{"local": {}}, defaultCWD: "/fixture/project", agents: onboarding.Detect(func(string) (string, error) { return "", nil })}
}
func TestSetupConsentCancelRetryAndCWD(t *testing.T) {
	s := setupFixture()
	s.mode = agentSetup
	s.handle("1")
	if s.mode != installConsent {
		t.Fatal("missing agent did not require consent")
	}
	if !strings.Contains(strings.Join(s.setupLines(), "\n"), s.setupAgent.Installer) {
		t.Fatal("exact command absent")
	}
	a, _ := s.handle("escape")
	if a != nil || s.mode != navigate {
		t.Fatal("cancel executed")
	}
	s.mode = agentSetup
	s.handle("2")
	a, _ = s.handle("enter")
	if a == nil || a.kind != setupInvoke || a.setupStage != "install" {
		t.Fatal("consent not explicit")
	}
	s.mode, s.setupStage = setupFailure, "install"
	a, _ = s.handle("r")
	if a != nil || s.mode != installConsent {
		t.Fatal("retry bypassed consent")
	}
	s.mode = providerReady
	s.setupAgent.Path = "/fixture/bin/codex"
	s.handle("enter")
	if s.mode != newCWD || s.input != "/fixture/project" || s.agent != "codex" {
		t.Fatal("cwd review lost")
	}
}
func TestSetupMissingAgentFromNewPreservesWorkspace(t *testing.T) {
	s := setupFixture()
	s.mode = newAgent
	s.input = "claude"
	s.target = row{machine: "local"}
	s.target.CWD = "/fixture/selected"
	s.handle("enter")
	if s.mode != installConsent {
		t.Fatal("no installer review")
	}
	s.mode = providerReady
	s.setupAgent.Path = "/fixture/bin/claude"
	s.handle("enter")
	if s.input != "/fixture/selected" {
		t.Fatal("workspace lost")
	}
	s = setupFixture()
	s.mode = newAgent
	s.input = "claude"
	s.target.machine = "remote"
	s.handle("enter")
	if s.mode != newCWD {
		t.Fatal("local install offered for remote")
	}
}
func TestSetupRejectsNonInteractiveExecution(t *testing.T) {
	if err := setupCommand(context.Background(), onboarding.Agent{ID: "claude"}, "install", remotepkg.IO{}); err == nil {
		t.Fatal("piped install allowed")
	}
}
func TestSetupEmptyNavigatorAndConnectionGuide(t *testing.T) {
	s := setupFixture()
	s.machines["local"] = machine{updated: time.Now()}
	screen := render(s, 120, 36, time.Now())
	for _, want := range []string{"Install an agent", "a install an agent", "1 machine", "Claude Code   Missing"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
