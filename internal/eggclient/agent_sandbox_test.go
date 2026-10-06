package eggclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestAgentRuntimeCommandMatchesSupportedAgentCatalog(t *testing.T) {
	for _, definition := range agent.Definitions() {
		if got := agentRuntimeCommand(definition.Name); got != definition.Command {
			t.Errorf("agentRuntimeCommand(%q) = %q, want %q", definition.Name, got, definition.Command)
		}
	}
	if got := agentRuntimeCommand("cursor"); got != "agent" {
		t.Fatalf("Cursor isolated runtime command = %q, want agent", got)
	}
	if got := agentRuntimeCommand("custom-command"); got != "custom-command" {
		t.Fatalf("unknown command fallback = %q", got)
	}
}

func TestLaunchDiscoveryRefusesReplaceablePolicyAlias(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(base, []byte("fs: [rw:./]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, filepath.Join(root, "base.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("base: ./base.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpawnEggConfig("", root, false); err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
		t.Fatalf("CLI discovery admitted replaceable policy: %v", err)
	}
	if _, _, err := PrepareBrowserLaunch(&config.WingConfig{}, &ws.PTYStart{CWD: root, OrgRole: "admin"}, root, false, nil); err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
		t.Fatalf("browser discovery admitted replaceable policy: %v", err)
	}
}
