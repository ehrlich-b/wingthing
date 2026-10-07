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
			// The wrapper can search this directory as root; the jailed agent cannot.
			// Prove EACCES is evaluated with the agent's filesystem credentials.
			blocked := filepath.Join(role, "unsearchable")
			if err := os.MkdirAll(filepath.Join(blocked, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(blocked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
			sandboxCfg.DenyRename = append(sandboxCfg.DenyRename, filepath.Join(blocked, "child"))
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
if test -e "$1/unsearchable/child"; then exit 46; fi
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
			for _, path := range []string{hidden, filepath.Join(blocked, "child")} {
				found := false
				for _, line := range strings.Split(string(log), "\n") {
					if strings.Contains(line, "level=DEBUG") && strings.Contains(line, "skipped unreachable policy directory") && strings.Contains(line, "path="+path) {
						found = true
					}
				}
				if !found {
					t.Fatalf("unreachable sibling or unsearchable pin was not skipped: %s\n%s", path, log)
				}
			}
		})
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
