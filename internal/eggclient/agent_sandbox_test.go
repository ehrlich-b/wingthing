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

func TestAuthenticatedBrowserLaunchUsesRoleRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root := filepath.Join(home, "eng")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	role := "base: none\nfs: [deny:/, ro:/usr, rw:., deny:/opt/wingthing/support, deny-write:./egg.yaml]\naudit: true\ndangerously_skip_permissions: true\nenv: [WT_USER, WT_USER_EMAIL, WT_SESSION_ID, DISABLE_TELEMETRY=1]\nnetwork: [role.example]\n"
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte(role), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	admin := &egg.EggConfig{FS: []string{"deny:/", "ro:/usr", "rw:./"}, Network: egg.NetworkField{Domains: []string{"wing.example"}}}
	for _, shared := range []bool{false, true} {
		wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: root, Members: []string{"eng@example.com"}}}}
		for _, cwd := range []string{root, sub} {
			start := ws.PTYStart{UserID: "eng", Email: "eng@example.com", OrgRole: "member", CWD: cwd}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, admin)
			if err != nil {
				t.Fatal(err)
			}
			if start.CWD != cwd || !cfg.Audit || !cfg.DangerouslySkipPermissions || !ContainsExactPath(cfg.Env, "WT_USER_EMAIL") || !ContainsExactPath(cfg.FS, "deny:/opt/wingthing/support") || !ContainsExactPath(cfg.FS, "rw:"+root) || ContainsExactPath(cfg.FS, "ro:/opt/wingthing/support") {
				t.Fatalf("lost role policy or trusted planted config: cwd=%s start=%s cfg=%#v", cwd, start.CWD, cfg)
			}
		}
	}
	if err := os.Remove(filepath.Join(root, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: root, Members: []string{"eng@example.com"}}}}
	start := ws.PTYStart{UserID: "eng", Email: "eng@example.com", OrgRole: "member", CWD: sub}
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, admin); err == nil || !strings.Contains(err.Error(), "ask the wing owner") {
		t.Fatalf("missing role policy admitted: %v", err)
	}
	start.OrgRole = "admin"
	cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, admin)
	if err != nil || !ContainsExactPath(cfg.Network.Domains, "wing.example") {
		t.Fatalf("admin default lost: %#v %v", cfg, err)
	}
	wc.Org = ""
	// Personal wings retain their existing exact-path selection; unrestricted
	// personal workspaces discover a subdirectory policy.
	wc.Paths = nil
	start.CWD = sub
	cfg, _, err = PrepareBrowserLaunch(wc, &start, home, false, admin)
	if err != nil || !ContainsExactPath(cfg.FS, "ro:/opt/wingthing/support") {
		t.Fatalf("personal discovery changed: %#v %v", cfg, err)
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

func TestRoostPolicySelectsDeepestRootAndRefusesEscapingAlias(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	outer, inner := filepath.Join(home, "eng"), filepath.Join(home, "eng", "nested")
	cwd := filepath.Join(inner, "sub")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		filepath.Join(outer, "egg.yaml"): "base: none\nnetwork: [outer.example]\n",
		filepath.Join(inner, "egg.yaml"): "base: none\nnetwork: [inner.example]\n",
		filepath.Join(cwd, "egg.yaml"):   "base: none\nnetwork: [planted.example]\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: outer}, {Path: inner}}}
	start := ws.PTYStart{UserID: "user", OrgRole: "admin", CWD: cwd}
	cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig())
	if err != nil || len(cfg.Network.Domains) != 1 || cfg.Network.Domains[0] != "inner.example" {
		t.Fatalf("wrong root: %#v %v", cfg, err)
	}
	if err := os.Remove(filepath.Join(inner, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outer, "egg.yaml"), filepath.Join(inner, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil {
		t.Fatal("accepted policy outside deepest root")
	}
}
