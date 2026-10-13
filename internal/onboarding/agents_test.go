package onboarding

import (
	"errors"
	"testing"
)

func TestOnboardingDetectionIsOnlyPATHLookup(t *testing.T) {
	var calls []string
	agents := Detect(func(id string) (string, error) {
		calls = append(calls, id)
		if id == "claude" {
			return "/fixture/bin/claude", nil
		}
		return "", errors.New("missing")
	})
	if len(calls) != 2 || calls[0] != "claude" || calls[1] != "codex" || agents[0].Path != "/fixture/bin/claude" || agents[1].Path != "" {
		t.Fatalf("detection: %v %v", calls, agents)
	}
	if agents[0].Installer != "curl -fsSL https://claude.ai/install.sh | bash" || agents[1].Installer != "curl -fsSL https://chatgpt.com/codex/install.sh | sh" {
		t.Fatal("installer contract changed")
	}
}
