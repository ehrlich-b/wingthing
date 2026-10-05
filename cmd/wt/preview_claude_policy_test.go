package main

import (
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewClaudeDoesNotImportHostModelPolicy(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	writePolicyFixture(t, path, "invalid host policy must not be opened")
	args, err := isolatedClaudePolicyArgs("claude", true)
	if err != nil || len(args) != 0 {
		t.Fatalf("preview imported host policy: %q %v", args, err)
	}
	assertPolicyFixture(t, path, "invalid host policy must not be opened")
}
