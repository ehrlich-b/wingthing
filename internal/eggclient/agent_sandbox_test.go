package eggclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
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

func TestBrowserWingDefaultsValidateLoaderAliasesAgainstSessionWorkspace(t *testing.T) {
	for _, loader := range []string{"wing.yaml", "egg.yaml", "custom.yaml"} {
		t.Run(loader, func(t *testing.T) {
			home := config.CanonicalProviderPath(t.TempDir())
			state, dotfiles, work := filepath.Join(home, ".wingthing"), filepath.Join(home, "dotfiles"), filepath.Join(home, "work")
			for _, dir := range []string{state, dotfiles, work} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", state)
			t.Chdir(home) // Wing startup has no session workspace yet.
			target, alias := filepath.Join(dotfiles, loader), filepath.Join(state, loader)
			if err := os.WriteFile(target, []byte("fs: [rw:./]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			wingDefault := egg.DefaultEggConfig()
			if loader != "wing.yaml" {
				var err error
				wingDefault, err = egg.ResolveEggConfig(alias)
				if err != nil {
					t.Fatalf("wing startup validated against controller CWD: %v", err)
				}
			}
			if err := wingDefault.ResolutionError(); err != nil {
				t.Fatalf("wing startup validated against controller CWD: %v", err)
			}
			for _, workspace := range []string{work, dotfiles, home, work} {
				start := ws.PTYStart{CWD: workspace, OrgRole: "admin"}
				policy, _, err := PrepareBrowserLaunch(&config.WingConfig{}, &start, home, false, wingDefault)
				if workspace != work {
					if err == nil || !strings.Contains(err.Error(), "replaceable symlink") {
						t.Fatalf("session-writable loader alias admitted in %s: %v", workspace, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("read-only dotfiles alias refused: %v", err)
				}
				if !ContainsExactPath(policy.FS, "deny:"+target) && !ContainsExactPath(policy.FS, "deny-write:"+target) {
					t.Fatalf("resolved loader remains unprotected: %v", policy.FS)
				}
			}
		})
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
