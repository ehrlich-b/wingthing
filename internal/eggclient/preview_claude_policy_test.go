package eggclient

import (
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient/testutil"
)

func TestPreviewClaudeDoesNotImportHostModelPolicy(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	testutil.WritePolicyFixture(t, path, "invalid host policy must not be opened")
	args, err := IsolatedClaudePolicyArgs("claude", true)
	if err != nil || len(args) != 0 {
		t.Fatalf("preview imported host policy: %q %v", args, err)
	}
	testutil.AssertPolicyFixture(t, path, "invalid host policy must not be opened")
}
