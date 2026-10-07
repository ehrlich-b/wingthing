//go:build linux

package eggclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// The Linux backend re-execs this test binary to install its namespace policy.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestRoostPolicyLinuxSiblingRolesStartWithPins(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	parent := config.CanonicalProviderPath(t.TempDir())
	roles := []string{filepath.Join(parent, "a"), filepath.Join(parent, "b")}
	// This configured sibling exists on the host, but its ancestor is masked
	// in each role's jail. Its pin must be skipped after the masks are applied.
	hidden := filepath.Join(parent, "hidden", "c")
	if err := os.MkdirAll(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	for i, role := range roles {
		if err := os.Mkdir(role, 0700); err != nil {
			t.Fatal(err)
		}
		policy := "base: none\nfs:\n  - deny:/\n"
		for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
			if _, err := os.Stat(path); err == nil {
				policy += "  - ro:" + path + "\n"
			}
		}
		policy += "  - rw:" + role + "\n  - deny:" + roles[1-i] + "\n  - deny:" + filepath.Dir(hidden) + "\n  - deny-write:./egg.yaml\n"
		if err := os.WriteFile(filepath.Join(role, "egg.yaml"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{
		{Path: roles[0], Members: []string{"alice@example.com"}},
		{Path: roles[1], Members: []string{"bob@example.com"}},
		{Path: hidden},
	}}
	for i, role := range roles {
		t.Run(filepath.Base(role), func(t *testing.T) {
			start := ws.PTYStart{UserID: "member", Email: []string{"alice@example.com", "bob@example.com"}[i], OrgRole: "member", CWD: role}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
			if err != nil {
				t.Fatal(err)
			}
			sandboxCfg := cfg.ToSandboxConfig(home)
			for _, path := range []string{role, parent, filepath.Dir(parent), hidden} {
				if !ContainsExactPath(sandboxCfg.DenyRename, path) {
					t.Fatalf("missing configured root or ancestor pin: %s", path)
				}
			}
			skipped := []string{hidden}
			sb, err := sandbox.New(sandboxCfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sb.Destroy(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `
set -e
printf ordinary > "$1/ordinary"
if cat "$2/egg.yaml" 2>/dev/null; then exit 42; fi
if printf replaced > "$1/egg.yaml" 2>/dev/null; then exit 43; fi
if mv "$1" "$1-old" 2>/dev/null; then exit 44; fi
if mv "$3" "$3-old" 2>/dev/null; then exit 45; fi
`, "sibling-roles", role, roles[1-i], parent})
			if err != nil {
				t.Fatal(err)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				if os.IsPermission(err) {
					t.Skipf("namespace creation unavailable: %v", err)
				}
				t.Fatalf("role policy failed to start or enforce its boundaries: %v, %s", err, output)
			}
			if data, err := os.ReadFile(filepath.Join(role, "ordinary")); err != nil || string(data) != "ordinary" {
				t.Fatalf("ordinary role-root write failed: %q, %v", data, err)
			}
			log, err := os.ReadFile(sb.DiagLog())
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range skipped {
				found := false
				for _, line := range strings.Split(string(log), "\n") {
					if strings.Contains(line, "level=DEBUG") && strings.Contains(line, "skipped unreachable policy directory") && strings.Contains(line, "path="+path) {
						found = true
					}
				}
				if !found {
					t.Fatalf("masked sibling pin was not skipped: %s\n%s", path, log)
				}
			}
		})
	}
}

// Use the runner's HOME rather than /tmp: /tmp is an intentional writable
// grant, whereas the browser canary's /opt/wingthing ancestors are scaffolding.
func TestRoostPolicyLinuxBrowserCanaryLayoutStarts(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	providerHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.MkdirTemp(providerHome, "wt-roost-canary-")
	if err != nil {
		t.Fatal(err)
	}
	parent = config.CanonicalProviderPath(parent)
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	home := filepath.Join(parent, "user-home")
	roles := []string{filepath.Join(parent, "eng"), filepath.Join(parent, "support")}
	exports := filepath.Join(parent, "exports")
	for _, path := range append([]string{home, exports}, roles...) {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	for _, role := range roles {
		policy := "base: none\nfs:\n  - deny:/\n"
		for _, path := range []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/var"} {
			if _, err := os.Stat(path); err == nil {
				policy += "  - ro:" + path + "\n"
			}
		}
		policy += "  - rw:.\n  - deny-write:./egg.yaml\nnetwork: '*'\n"
		if err := os.WriteFile(filepath.Join(role, "egg.yaml"), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{
		{Path: roles[0], Members: []string{"eng@example.com"}},
		{Path: roles[1], Members: []string{"support@example.com"}},
	}, Exports: []config.ExportTarget{{Name: "review", Path: exports, Members: []string{"support@example.com"}}}}
	// Export destinations aren't policy roots. Also exercise the reported CI
	// failure with that absent policy explicitly listed as a workspace root.
	for _, exportAsRoot := range []bool{false, true} {
		if exportAsRoot {
			wc.Paths = append(wc.Paths, config.PathEntry{Path: exports})
			wc.Exports = nil // a workspace cannot also be an export destination
		}
		for i, role := range roles {
			t.Run(fmt.Sprintf("export-root=%v/%s", exportAsRoot, filepath.Base(role)), func(t *testing.T) {
				start := ws.PTYStart{UserID: "member", Email: []string{"eng@example.com", "support@example.com"}[i], OrgRole: "member", CWD: role}
				cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
				if err != nil {
					t.Fatal(err)
				}
				sandboxCfg := cfg.ToSandboxConfig(home)
				if ContainsExactPath(sandboxCfg.DenyWrite, filepath.Join(exports, "egg.yaml")) != exportAsRoot {
					t.Fatal("export destination incorrectly classified as a policy root")
				}
				sb, err := sandbox.New(sandboxCfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sb.Destroy() })
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `
set -e
awk '$5 == "/" && $6 ~ /(^|,)ro(,|$)/ {found=1} END {exit !found}' /proc/self/mountinfo
printf ordinary > "$1/ordinary"
mkdir -p "$HOME/.claude" "$HOME/.codex"
printf config > "$HOME/.claude/config"
printf config > "$HOME/.codex/config"
printf temporary > /tmp/canary-temporary
if cat "$2/egg.yaml" 2>/dev/null; then exit 42; fi
if mkdir -p "$3" 2>/dev/null; then exit 43; fi
if printf replaced > "$1/egg.yaml" 2>/dev/null; then exit 44; fi
for path in / "$4" "$5"; do
  if chmod 0700 "$path" 2>/dev/null; then exit 45; fi
  if mkdir "$path/canary-planted" 2>/dev/null; then exit 46; fi
done
printf launched
`, "browser-canary", role, roles[1-i], exports, parent, filepath.Dir(parent)})
				if err != nil {
					t.Fatal(err)
				}
				output, runErr := cmd.CombinedOutput()
				if os.IsPermission(runErr) {
					t.Skipf("namespace creation unavailable: %v", runErr)
				}
				if runErr != nil || string(output) != "launched" {
					log, _ := os.ReadFile(sb.DiagLog())
					t.Fatalf("browser canary layout refused or weakened: %v, output=%q, log=%s", runErr, output, log)
				}
			})
		}
	}
}

func TestRoostPolicyBlocksLinuxRootReplacement(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	for _, ancestor := range []bool{false, true} {
		t.Run(fmt.Sprint("ancestor=", ancestor), func(t *testing.T) {
			home, parent, _, wc := roostPolicyFixture(t)
			root := filepath.Join(parent, "role")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("base: none\nfs: [rw:"+home+"]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(home, "replacement")
			if err := os.Mkdir(replacement, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(replacement, "egg.yaml"), []byte("base: none\nfs: [rw:/]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			wc.Paths[0].Path = root
			start := ws.PTYStart{UserID: "admin", OrgRole: "admin", CWD: root}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
			if err != nil {
				t.Fatal(err)
			}
			sb, err := sandbox.New(cfg.ToSandboxConfig(home))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sb.Destroy(); err != nil {
					t.Error(err)
				}
			})
			target := root
			if ancestor {
				target = parent
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `
set -e
printf ordinary > "$1/ordinary"
if mv "$2" "$2-old"; then
  mv "$3" "$2"
  exit 42
fi
`, "root-replacement", root, target, replacement})
			if err != nil {
				t.Fatal(err)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				if os.IsPermission(err) {
					t.Skipf("namespace creation unavailable: %v", err)
				}
				t.Fatalf("policy directory replacement or ordinary write failed: %v, %s", err, output)
			}
			if data, err := os.ReadFile(filepath.Join(root, "ordinary")); err != nil || string(data) != "ordinary" {
				t.Fatalf("ordinary role-root write failed: %q, %v", data, err)
			}
		})
	}
}

func TestRoostPolicyLinuxMissingPaths(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	for _, boundary := range []string{"writable", "mode0500", "unsearchable", "readonly", "masked"} {
		for _, missing := range []string{"root", "egg.yaml"} {
			t.Run(boundary+"/"+missing, func(t *testing.T) {
				home := config.CanonicalProviderPath(t.TempDir())
				t.Setenv("HOME", home)
				t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
				parent := config.CanonicalProviderPath(t.TempDir())
				role := filepath.Join(parent, "a")
				ancestor := filepath.Join(parent, "new")
				for _, path := range []string{role, ancestor} {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				sibling := filepath.Join(ancestor, "deeper", "b")
				if missing == "egg.yaml" {
					if err := os.MkdirAll(sibling, 0700); err != nil {
						t.Fatal(err)
					}
				}
				policy := "base: none\nfs:\n  - rw:" + parent + "\n"
				if boundary == "readonly" {
					policy += "  - deny-write:" + ancestor + "\n"
				} else if boundary == "masked" {
					policy += "  - deny:" + ancestor + "\n"
				}
				if err := os.WriteFile(filepath.Join(role, "egg.yaml"), []byte(policy), 0600); err != nil {
					t.Fatal(err)
				}
				wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: role}, {Path: sibling}}}
				start := ws.PTYStart{UserID: "admin", OrgRole: "admin", CWD: role}
				cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig())
				if err != nil {
					t.Fatal(err)
				}
				// Change permissions after policy loading so the namespace walker
				// must evaluate the barrier using the agent's filesystem view.
				if boundary == "mode0500" || boundary == "unsearchable" {
					mode := os.FileMode(0500)
					if boundary == "unsearchable" {
						mode = 0
					}
					if err := os.Chmod(ancestor, mode); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(ancestor, 0700) })
				}
				sb, err := sandbox.New(cfg.ToSandboxConfig(home))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := sb.Destroy(); err != nil {
						t.Error(err)
					}
				})
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `
set -e
printf ordinary > "$2/ordinary"
chmod 0700 "$3" 2>/dev/null || true
if mkdir -p "$1" 2>/dev/null && printf 'base: none\nfs: [rw:/]\n' > "$1/egg.yaml" 2>/dev/null; then exit 42; fi
printf launched
`, "missing-policy", sibling, role, ancestor})
				if err != nil {
					t.Fatal(err)
				}
				output, runErr := cmd.CombinedOutput()
				if os.IsPermission(runErr) {
					t.Skipf("namespace creation unavailable: %v", runErr)
				}
				log, err := os.ReadFile(sb.DiagLog())
				if err != nil {
					t.Fatal(err)
				}
				if boundary == "writable" || boundary == "mode0500" || boundary == "unsearchable" {
					writableParent := ancestor
					if missing == "egg.yaml" && (boundary != "unsearchable" || os.Geteuid() == 0) {
						writableParent = sibling
					}
					if runErr == nil || !strings.Contains(string(log), sibling) || !strings.Contains(string(log), writableParent+" is on writable mount ") {
						t.Fatalf("creatable absent policy did not refuse launch: %v, output=%q, log=%s", runErr, output, log)
					}
					if _, err := os.Stat(filepath.Join(role, "ordinary")); !os.IsNotExist(err) {
						t.Fatalf("agent ran before missing-policy refusal: %v", err)
					}
					if err := os.Chmod(ancestor, 0700); err != nil {
						t.Fatal(err)
					}
				} else if runErr != nil || string(output) != "launched" {
					t.Fatalf("non-writable missing policy blocked launch: %v, output=%q, log=%s", runErr, output, log)
				} else if !strings.Contains(string(log), "skipped unreachable policy directory") || !strings.Contains(string(log), "path="+sibling) {
					t.Fatalf("missing policy was not skipped in the agent's view: %s", log)
				}
				if _, err := os.Stat(filepath.Join(sibling, "egg.yaml")); !os.IsNotExist(err) {
					t.Fatalf("agent planted a sibling policy: %v", err)
				}
			})
		}
	}
}

func TestRoostPolicyLinuxPersonalDefaultWithoutEggYAMLStarts(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	home := config.CanonicalProviderPath(t.TempDir())
	project := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	t.Chdir(project)
	cfg := egg.DefaultEggConfig()
	// Resolve relative rules as SpawnEgg does before passing them to egg run.
	for i, rule := range cfg.FS {
		mode, path, _ := strings.Cut(rule, ":")
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			cfg.FS[i] = mode + ":" + filepath.Join(project, path)
		}
	}
	sb, err := sandbox.New(cfg.ToSandboxConfig(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", "set -e; printf ordinary > ordinary; printf launched"})
	if err != nil {
		t.Fatal(err)
	}
	output, runErr := cmd.CombinedOutput()
	if os.IsPermission(runErr) {
		t.Skipf("namespace creation unavailable: %v", runErr)
	}
	log, _ := os.ReadFile(sb.DiagLog())
	if runErr != nil || string(output) != "launched" {
		t.Fatalf("personal default without egg.yaml refused: %v, output=%q, log=%s", runErr, output, log)
	}
	if data, err := os.ReadFile(filepath.Join(project, "ordinary")); err != nil || string(data) != "ordinary" {
		t.Fatalf("personal workspace write failed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(project, "egg.yaml")); !os.IsNotExist(err) {
		t.Fatalf("default launch synthesized egg.yaml: %v", err)
	}
	if !strings.Contains(string(log), "deny-write path absent at launch: "+filepath.Join(project, "egg.yaml")) {
		t.Fatalf("default egg.yaml deny-write rule wasn't exercised: %s", log)
	}
}
