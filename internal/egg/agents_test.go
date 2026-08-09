package egg

import (
	"slices"
	"testing"
)

func TestGeminiProfileMatchesCurrentCLIStorage(t *testing.T) {
	profile := Profile("gemini")
	if profile.ResumeFlag != "--resume" {
		t.Fatalf("ResumeFlag = %q", profile.ResumeFlag)
	}
	if profile.SessionDir != ".gemini/tmp" {
		t.Fatalf("SessionDir = %q", profile.SessionDir)
	}
	if !slices.Contains(profile.WriteDirs, ".gemini") {
		t.Fatalf("WriteDirs = %q", profile.WriteDirs)
	}
}

func TestOpenCodeProfileMatchesCurrentXDGStorage(t *testing.T) {
	profile := Profile("opencode")
	for _, dir := range []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".cache/opencode"} {
		if !slices.Contains(profile.WriteDirs, dir) {
			t.Errorf("WriteDirs missing %q: %q", dir, profile.WriteDirs)
		}
	}
	if profile.ResumeFlag != "--session" {
		t.Fatalf("ResumeFlag = %q", profile.ResumeFlag)
	}
	if profile.SessionDir != ".local/share/opencode" {
		t.Fatalf("SessionDir = %q", profile.SessionDir)
	}
}

func TestHermesProfileDeclaresDynamicProviderBoundary(t *testing.T) {
	profile := Profile("hermes")
	if !slices.Equal(profile.Domains, []string{"*"}) {
		t.Fatalf("Domains = %q, want explicit full network", profile.Domains)
	}
	if !slices.Contains(profile.EnvVars, "HERMES_HOME") || !slices.Contains(profile.WriteDirs, ".hermes") {
		t.Fatalf("Hermes profile is incomplete: %+v", profile)
	}
	if profile.ResumeFlag != "--resume" {
		t.Fatalf("ResumeFlag = %q", profile.ResumeFlag)
	}
}
