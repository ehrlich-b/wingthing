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

func TestAuthenticatedBrowserLaunchUsesRoleRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root := filepath.Join(home, "eng")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	policy := "base: none\nfs: [deny:/, ro:/usr, rw:" + root + ", deny:/opt/wingthing/support, deny:/opt/wingthing/.ssh, deny-write:./egg.yaml]\nenv: [HOME, WT_USER, WT_USER_EMAIL, WT_SESSION_ID, DISABLE_TELEMETRY=1]\nresources: {cpu: 7200s, max_fds: 2048}\nshell: /bin/bash\naudit: true\ndangerously_skip_permissions: true\nnetwork: [role.example]\n"
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\nenv: ['*']\nnetwork: ['*']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wingDefault := &egg.EggConfig{Shell: "/bin/wing-default", Network: egg.NetworkField{Domains: []string{"wing.example"}}}
	for _, shared := range []bool{false, true} {
		org := "org"
		if shared {
			org = ""
		}
		wc := &config.WingConfig{Org: org, Paths: config.PathList{{Path: root, Members: []string{"eng@example.com"}}}}
		for _, cwd := range []string{root, sub} {
			start := ws.PTYStart{UserID: "eng", Email: "eng@example.com", OrgRole: "member", CWD: cwd}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, wingDefault)
			if err != nil {
				t.Fatal(err)
			}
			if start.CWD != cwd {
				t.Fatalf("cwd changed from %s to %s", cwd, start.CWD)
			}
			if !cfg.Audit || !cfg.DangerouslySkipPermissions || cfg.Shell != "/bin/bash" || cfg.Resources.CPU != "7200s" || cfg.Resources.MaxFDs != 2048 || !ContainsExactPath(cfg.Network.Domains, "role.example") || cfg.IsAllEnv() {
				t.Fatalf("role settings lost (shared=%v cwd=%s): %#v", shared, cwd, cfg)
			}
			for _, env := range []string{"WT_USER", "WT_USER_EMAIL", "WT_SESSION_ID", "DISABLE_TELEMETRY=1"} {
				if !ContainsExactPath(cfg.Env, env) {
					t.Fatalf("missing role env %s", env)
				}
			}
			sandbox := cfg.ToSandboxConfig(home)
			for _, mount := range sandbox.Mounts {
				if mount.Source == "/opt/wingthing/support" {
					t.Fatalf("planted support mount admitted: %#v", cfg)
				}
			}
			if !ContainsExactPath(sandbox.Deny, "/opt/wingthing/support") || !ContainsExactPath(sandbox.Deny, "/opt/wingthing/.ssh") || !ContainsExactPath(sandbox.DenyWrite, filepath.Join(root, "egg.yaml")) {
				t.Fatalf("role denies lost: %#v", sandbox)
			}
		}
	}
}

func TestAuthenticatedBrowserRolePolicyDiscoveryBounds(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	root := filepath.Join(home, "eng")
	nested := filepath.Join(home, "other")
	sub := filepath.Join(nested, "sub")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{root, sub, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{home, root, sub, outside} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nnetwork: ['*']\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wingDefault := &egg.EggConfig{Shell: "/bin/default"}
	for _, shared := range []bool{false, true} {
		org := "org"
		if shared {
			org = ""
		}
		wc := &config.WingConfig{Org: org, Admins: []string{"admin@example.com"}, Paths: config.PathList{{Path: root}, {Path: nested}}}
		start := ws.PTYStart{UserID: "eng", Email: "eng@example.com", OrgRole: "member", CWD: sub}
		if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, wingDefault); err == nil || !strings.Contains(err.Error(), "ask the wing owner") {
			t.Fatalf("missing selected root policy: %v", err)
		}
		start.CWD = nested
		if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, wingDefault); err == nil || !strings.Contains(err.Error(), "ask the wing owner") {
			t.Fatalf("missing role-root policy: %v", err)
		}
		start.CWD = outside
		if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, wingDefault); err == nil {
			t.Fatal("member admitted outside configured roots")
		}
		start.Email = "admin@example.com"
		for _, cwd := range []string{sub, outside} {
			start.CWD = cwd
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, wingDefault)
			if err != nil || cfg.Shell != wingDefault.Shell || len(cfg.Network.Domains) != 0 {
				t.Fatalf("admin fallback discovered child/ancestor policy: %#v, %v", cfg, err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(outside, "egg.yaml"), filepath.Join(nested, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: root}, {Path: nested}}}
	for _, role := range []string{"member", "admin"} {
		start := ws.PTYStart{UserID: "eng", OrgRole: role, CWD: sub}
		if _, _, err := PrepareBrowserLaunch(wc, &start, home, false, wingDefault); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("external policy symlink accepted: %v", err)
		}
	}
	if err := os.Remove(filepath.Join(nested, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "egg.yaml"), []byte("base: none\naudit: true\nnetwork: [deepest.example]\ndangerously_skip_permissions: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := ws.PTYStart{UserID: "eng", OrgRole: "member", CWD: sub}
	cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, wingDefault)
	if err != nil || !cfg.Audit || !cfg.DangerouslySkipPermissions || !ContainsExactPath(cfg.Network.Domains, "deepest.example") || ContainsExactPath(cfg.Network.Domains, "*") {
		t.Fatalf("selected root policy lost: %#v, %v", cfg, err)
	}
}

func TestPersonalBrowserLaunchDiscoversWorkspacePolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	sub := filepath.Join(home, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "egg.yaml"), []byte("base: none\nnetwork: [workspace.example]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := ws.PTYStart{UserID: "owner", OrgRole: "owner", CWD: sub}
	admin := &egg.EggConfig{Network: egg.NetworkField{Domains: []string{"admin.example"}}}
	cfg, _, err := PrepareBrowserLaunch(&config.WingConfig{}, &start, home, false, admin)
	if err != nil || !ContainsExactPath(cfg.Network.Domains, "workspace.example") {
		t.Fatalf("personal wing lost workspace discovery: %#v, %v", cfg, err)
	}
}

func TestAuthenticatedBrowserRoleRootsUseCanonicalPaths(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	root := filepath.Join(home, "eng")
	nested := filepath.Join(home, "other")
	sub := filepath.Join(nested, "sub")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{root, sub, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{root, nested} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nshell: "+dir+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(nested, alias); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: root}, {Path: nested}}}
	start := ws.PTYStart{UserID: "eng", OrgRole: "member", CWD: filepath.Join(alias, "sub")}
	cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
	if err != nil || cfg.Shell != nested || ContainsExactPath(cfg.FS, "deny-rename:"+nested) || !ContainsExactPath(cfg.FS, "deny-write:"+filepath.Join(nested, "egg.yaml")) {
		t.Fatalf("canonical selected root not selected: %#v, %v", cfg, err)
	}
	start.CWD = escape
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig()); err == nil {
		t.Fatal("cwd symlink escaped configured roots")
	}
}
