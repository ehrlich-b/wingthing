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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
	t.Chdir(t.TempDir()) // The controller's CWD is not the session workspace.
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
	for _, configPath := range []string{"", filepath.Join(root, "egg.yaml")} {
		if _, err := LoadSpawnEggConfig(configPath, root, false); err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
			t.Fatalf("CLI discovery (%q) admitted replaceable policy: %v", configPath, err)
		}
		if _, _, err := LoadEggConfigForExplain(configPath, root); err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
			t.Fatalf("explain (%q) admitted replaceable policy: %v", configPath, err)
		}
	}
	if _, _, err := PrepareBrowserLaunch(&config.WingConfig{}, &ws.PTYStart{CWD: root, OrgRole: "admin"}, root, false, nil); err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
		t.Fatalf("browser discovery admitted replaceable policy: %v", err)
	}
}

func TestLaunchDiscoveryAllowsReadOnlyPolicyAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
	workspace, dotfiles := t.TempDir(), t.TempDir()
	t.Chdir(dotfiles)
	base := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(base, []byte("fs: [rw:./]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dotfiles, "base.yaml")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "egg.yaml"), []byte("base: "+alias+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, configPath := range []string{"", alias} {
		policy, err := LoadSpawnEggConfig(configPath, workspace, false)
		if err != nil {
			t.Fatalf("CLI discovery (%q) refused read-only alias: %v", configPath, err)
		}
		if !ContainsExactPath(policy.FS, "deny-write:"+alias) || !ContainsExactPath(policy.FS, "deny-write:"+config.CanonicalProviderPath(base)) {
			t.Fatalf("read-only alias lacks protection: %v", policy.FS)
		}
		if _, _, err := LoadEggConfigForExplain(configPath, workspace); err != nil {
			t.Fatalf("explain (%q) refused read-only alias: %v", configPath, err)
		}
	}
	if _, _, err := PrepareBrowserLaunch(&config.WingConfig{}, &ws.PTYStart{CWD: workspace, OrgRole: "admin"}, home, false, nil); err != nil {
		t.Fatalf("browser discovery refused read-only alias: %v", err)
	}
}
