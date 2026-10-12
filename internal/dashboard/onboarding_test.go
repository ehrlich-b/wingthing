package dashboard

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func onboardingProvider(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, name)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return binary
}

func TestFirstRunDetectionAndCancelNeverInvokeProvider(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("ONBOARDING_MARKER", marker)
	binary := onboardingProvider(t, "claude", "touch \"$ONBOARDING_MARKER\"\n")
	s := &state{onboarding: true, defaultCWD: t.TempDir(), machines: map[string]machine{"local": {}}}
	s.rescanProviders()
	if s.providerPaths["claude"] != binary {
		t.Fatal("detection did not preserve executable path")
	}
	s.handle("n")
	if s.mode != providerVerify {
		t.Fatal("detection should lead to explicit version consent")
	}
	s.handle("escape")
	if s.mode != navigate || s.agent != "claude" {
		t.Fatal("cancel lost selected provider or navigator")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("detection or cancellation invoked provider")
	}
	if len(s.machines["local"].sessions) != 0 {
		t.Fatal("detection admitted a session")
	}
}

func TestOnboardingVerificationAndLoginAreSeparateConsents(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("ONBOARDING_MARKER", marker)
	onboardingProvider(t, "codex", "printf '%s\\n' \"$*\" >> \"$ONBOARDING_MARKER\"\n")
	s := &state{agent: "codex", defaultCWD: t.TempDir(), target: row{machine: "local"}}
	streams := remotepkg.IO{Out: io.Discard, ErrOut: io.Discard}
	if err := s.runOnboarding(t.Context(), providerVerify, streams); err != nil || s.mode != providerLogin {
		t.Fatalf("verification: mode=%v err=%v", s.mode, err)
	}
	data, _ := os.ReadFile(marker)
	if string(data) != "--version\n" {
		t.Fatalf("version check also invoked login: %s", data)
	}
	if err := s.runOnboarding(t.Context(), providerLogin, streams); err != nil || s.mode != newCWD || s.input != s.defaultCWD {
		t.Fatalf("login did not return to cwd review: mode=%v err=%v", s.mode, err)
	}
	data, _ = os.ReadFile(marker)
	if string(data) != "--version\nlogin\n" {
		t.Fatalf("wrong provider handoff: %s", data)
	}
	// Login itself never returns a session action or fabricates admission.
	if act, _ := s.handle("escape"); act != nil {
		t.Fatal("leaving cwd review launched work")
	}
}

func TestInstallerDownloadFailureNeedsFreshConsent(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "downloads")
	t.Setenv("ONBOARDING_MARKER", marker)
	onboardingProvider(t, "curl", "printf 'attempt\\n' >> \"$ONBOARDING_MARKER\"\nexit 22\n")
	s := &state{agent: "claude"}
	if err := s.runOnboarding(t.Context(), installerConsent, remotepkg.IO{Out: io.Discard, ErrOut: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if s.mode != onboardingRecovery || !strings.Contains(s.message, "Install stage failed") {
		t.Fatalf("pipeline concealed failed download: mode=%v message=%s", s.mode, s.message)
	}
	if act, _ := s.handle("enter"); act != nil || s.mode != installerConsent {
		t.Fatal("retry must redisplay consent before invoking an installer")
	}
	s.handle("escape")
	data, _ := os.ReadFile(marker)
	if string(data) != "attempt\n" || s.agent != "claude" {
		t.Fatalf("silent retry or lost provider: %s %s", data, s.agent)
	}
}

func TestOnboardingSkipAndRemoteScope(t *testing.T) {
	s := &state{onboarding: true, defaultCWD: "/local/work", machines: map[string]machine{"local": {}, "forge": {}}}
	s.handle("s")
	act, _ := s.handle("enter")
	if act == nil || act.agent != "" || act.row.machine != "local" || act.cwd != "/local/work" {
		t.Fatal("skip did not use the normal local shell route")
	}
	s.machineFilter, s.mode = "forge", navigate
	s.handle("n")
	if s.mode != newAgent || s.target.machine != "forge" {
		t.Fatal("local onboarding replaced saved remote creation")
	}
	s.mode, s.machineFilter = navigate, ""
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{{ID: "admitted"}}}
	s.handle("n")
	if s.mode != newAgent {
		t.Fatal("existing inventory replaced by first-run onboarding")
	}
}
