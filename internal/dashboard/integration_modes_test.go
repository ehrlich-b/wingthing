package dashboard

import (
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsCoexistsWithOnboardingAndMachineDetails(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := setupFixture()
	s.handle("D")
	if s.mode != diagnostics {
		t.Fatal("diagnostics did not open")
	}
	if screen := render(s, 120, 36, time.Now()); !strings.Contains(screen, "Runs / diagnostics") {
		t.Fatalf("diagnostics rendered as another mode: %s", screen)
	}
	s.handle("1")
	if s.mode != diagnosticInput {
		t.Fatal("onboarding captured a diagnostic input key")
	}
	if screen := render(s, 120, 36, time.Now()); !strings.Contains(screen, "Route: terminal_start, agent_start or agent_run") {
		t.Fatalf("diagnostic input was not visible: %s", screen)
	}
	s.handle("escape")
	s.handle("M")
	if s.mode != machineDetails || !strings.Contains(render(s, 120, 36, time.Now()), "Machines · This machine") {
		t.Fatal("machine details were captured by onboarding")
	}
	s.handle("escape")
	if s.mode != navigate {
		t.Fatal("machine details did not return to the navigator")
	}
	s.handle("a")
	s.handle("1")
	if s.mode != installConsent || !strings.Contains(strings.Join(s.setupLines(), "\n"), s.setupAgent.Installer) {
		t.Fatal("onboarding lost the exact installer consent")
	}
	if action, quit := s.handle("escape"); action != nil || quit || s.mode != navigate {
		t.Fatal("canceling installer consent changed execution")
	}
}
