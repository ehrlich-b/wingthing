package eggclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func roostPolicyFixture(t *testing.T) (string, string, string, *config.WingConfig) {
	t.Helper()
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root := filepath.Join(home, "work")
	child := filepath.Join(home, "Restricted")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(child, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, child} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nfs: [rw:"+dir+"]\nshell: "+dir+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{
		{Path: root, Members: []string{"alice@example.com"}},
		{Path: child, Members: []string{"bob@example.com"}},
	}}
	return home, root, child, wc
}

func TestRoostPolicyRejectsNestedRoots(t *testing.T) {
	home, root, _, wc := roostPolicyFixture(t)
	child := filepath.Join(root, "Restricted")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	wc.Paths[1].Path = child // No child egg.yaml exists to enforce deny-write on.
	for _, shared := range []bool{false, true} {
		wc.Org = "org"
		if shared {
			wc.Org = ""
		}
		for _, role := range []string{"member", "admin"} {
			for _, cwd := range []string{root, child, home, ""} {
				start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: role, CWD: cwd}
				if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), "nested roost paths") || !strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), child) {
					t.Fatalf("nested roots admitted (shared=%v role=%s cwd=%s): %v", shared, role, cwd, err)
				}
			}
		}
	}
	wc.Org = ""
	start := ws.PTYStart{UserID: "owner", OrgRole: "owner", CWD: root}
	if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig()); err != nil || cfg.Shell != root {
		t.Fatalf("personal wing rejected nested roots: %#v, %v", cfg, err)
	}
}

func TestRoostPolicyRejectsSymlinkedRoots(t *testing.T) {
	home, root, child, wc := roostPolicyFixture(t)
	alias := filepath.Join(root, "bob")
	if err := os.Symlink(child, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, filepath.Join(alias, "sub"), filepath.Join(alias, "absent")} {
		wc.Paths[1].Path = path
		for _, shared := range []bool{false, true} {
			wc.Org = "org"
			if shared {
				wc.Org = ""
			}
			for _, role := range []string{"member", "admin"} {
				start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: role, CWD: root}
				if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "symlink") {
					t.Fatalf("symlinked root admitted (shared=%v role=%s): %v", shared, role, err)
				}
			}
		}
	}
	// Personal wings retain workspace policy discovery despite nested aliases.
	wc.Org = ""
	start := ws.PTYStart{UserID: "owner", OrgRole: "owner", CWD: root}
	if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, false, egg.DefaultEggConfig()); err != nil || cfg.Shell != root {
		t.Fatalf("personal wing rejected configured aliases: %#v, %v", cfg, err)
	}
}

func TestRoostPolicyAnchorsRelativeFSRules(t *testing.T) {
	home, root, _, wc := roostPolicyFixture(t)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	policy := "base: none\nfs: [deny:./private, deny:private, deny-write:./egg.yaml, ro:./read, rw:write, bare, ro:/usr, deny:~/.ssh, rw:~]\n"
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		wc.Org = "org"
		if shared {
			wc.Org = ""
		}
		start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: sub}
		cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig())
		if err != nil {
			t.Fatal(err)
		}
		wantFS := []string{"deny:" + filepath.Join(root, "private"), "deny:" + filepath.Join(root, "private"), "deny-write:" + filepath.Join(root, "egg.yaml"), "ro:" + filepath.Join(root, "read"), "rw:" + filepath.Join(root, "write"), filepath.Join(root, "bare"), "ro:/usr", "deny:~/.ssh", "rw:~"}
		for i, want := range wantFS {
			if cfg.FS[i] != want {
				t.Fatalf("relative rule %d was not rooted: %q, want %q", i, cfg.FS[i], want)
			}
		}
		effective := egg.ResolvePolicy(cfg, "", home)
		if !ContainsExactPath(effective.Deny, filepath.Join(root, "private")) || ContainsExactPath(effective.Deny, filepath.Join(sub, "private")) || !ContainsExactPath(effective.Deny, filepath.Join(home, ".ssh")) || !ContainsExactPath(effective.DenyWrite, filepath.Join(root, "egg.yaml")) {
			t.Fatalf("relative or home deny changed meaning: %#v", effective)
		}
		for i, want := range []string{filepath.Join(root, "read"), filepath.Join(root, "write"), filepath.Join(root, "bare"), "/usr", home} {
			if effective.Mounts[i].Source != want || effective.Mounts[i].Target != want || effective.Mounts[i].ReadOnly != (i == 0 || i == 3) {
				t.Fatalf("mount changed meaning: %#v, want %s", effective.Mounts[i], want)
			}
		}
	}
}

func TestRoostPolicySelectedRootACL(t *testing.T) {
	home, root, child, wc := roostPolicyFixture(t)
	for _, shared := range []bool{false, true} {
		wc.Org = "org"
		if shared {
			wc.Org = ""
		}
		for _, cwd := range []string{child, filepath.Join(child, "sub")} {
			start := ws.PTYStart{UserID: "alice", Email: "ALICE@example.com", OrgRole: "member", CWD: cwd}
			if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), child) {
				t.Fatalf("Alice inherited access to Bob's selected root (shared=%v): %#v, %v", shared, cfg, err)
			}
			start.Email, start.UserID = "BOB@example.com", "bob"
			if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err != nil || cfg.Shell != child {
				t.Fatalf("Bob's selected-root ACL was lost: %#v, %v", cfg, err)
			}
			start.Email, start.OrgRole = "alice@example.com", "admin"
			if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err != nil || cfg.Shell != child {
				t.Fatalf("admin could not select child policy: %#v, %v", cfg, err)
			}
		}
		start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: root}
		if _, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig()); err != nil {
			t.Fatal(err)
		}
	}
	wc.Paths[1].Members = nil // Legacy open entries remain accessible.
	start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: child}
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err != nil {
		t.Fatalf("legacy open child denied: %v", err)
	}
}

func TestRoostPolicyRejectsFileBases(t *testing.T) {
	home, root, _, wc := roostPolicyFixture(t)
	base := filepath.Join(root, "base.yaml")
	if err := os.WriteFile(base, []byte("base: none\nnetwork: ['*']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stateBases := filepath.Join(home, "state", "bases")
	if err := os.MkdirAll(stateBases, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateBases, "custom.yaml"), []byte("base: none\nnetwork: ['*']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, baseField := range []string{"./base.yaml", base, "custom", "../work/base.yaml", "{fs: ./base.yaml}", "{network: ./base.yaml}", "{env: ./base.yaml}"} {
		t.Run(baseField, func(t *testing.T) {
			path := filepath.Join(root, "egg.yaml")
			if err := os.WriteFile(path, []byte("base: "+baseField+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"member", "admin"} {
				start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: role, CWD: root}
				if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "base") {
					t.Fatalf("file base admitted: %#v, %v", cfg, err)
				}
			}
		})
	}
}

func TestRoostPolicyBuiltinBases(t *testing.T) {
	home, root, _, wc := roostPolicyFixture(t)
	// A file named default must never substitute for the built-in default.
	stateBases := filepath.Join(home, "state", "bases")
	if err := os.MkdirAll(stateBases, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateBases, "default.yaml"), []byte("base: none\nnetwork: ['*']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, baseField := range []string{"", "base: none\n", "base: default\n", "base: {name: default, fs: none, env: none}\n"} {
		t.Run(baseField, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte(baseField+"audit: true\nnetwork: [role.example]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: root}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig())
			if err != nil || !cfg.Audit || !ContainsExactPath(cfg.Network.Domains, "role.example") || ContainsExactPath(cfg.Network.Domains, "*") {
				t.Fatalf("built-in base lost role policy: %#v, %v", cfg, err)
			}
			wantDefaultFS := baseField == "" || baseField == "base: default\n"
			if ContainsExactPath(cfg.FS, "deny:~/.ssh") != wantDefaultFS {
				t.Fatalf("unexpected default filesystem policy: %#v", cfg.FS)
			}
		})
	}
}

func TestRoostPolicyProtectsEveryRootWithoutCreatingTargets(t *testing.T) {
	home, root, child, wc := roostPolicyFixture(t)
	missing := filepath.Join(home, "absent", "workspace")
	wc.Paths = append(wc.Paths, config.PathEntry{Path: missing, Members: []string{"bob@example.com"}})
	if err := os.Remove(filepath.Join(child, "egg.yaml")); err != nil {
		t.Fatal(err)
	}
	defaultPolicy := &egg.EggConfig{FS: make([]string, 0, 8), Shell: "/bin/default"}
	for _, role := range []string{"member", "admin"} {
		cwds := []string{root}
		if role == "admin" {
			cwds = append(cwds, child, home) // Missing-policy and outside-root fallbacks.
		}
		for _, cwd := range cwds {
			start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: role, CWD: cwd}
			cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, defaultPolicy)
			if err != nil {
				t.Fatal(err)
			}
			resolved := cfg.ToSandboxConfig(home)
			for _, dir := range []string{root, child, missing} {
				path := filepath.Join(dir, "egg.yaml")
				if !ContainsExactPath(resolved.DenyWrite, path) {
					t.Errorf("%s session can replace configured policy %s: %#v", role, path, cfg.FS)
				}
			}
		}
	}
	if len(defaultPolicy.FS) != 0 || defaultPolicy.FS[:cap(defaultPolicy.FS)][0] != "" {
		t.Fatal("root protection mutated the captured default policy")
	}
	for _, path := range []string{filepath.Join(child, "egg.yaml"), filepath.Join(home, "absent")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("protecting an absent target created %s: %v", path, err)
		}
	}
}

func TestRoostPolicyRequiresRegularFile(t *testing.T) {
	home, root, _, wc := roostPolicyFixture(t)
	path := filepath.Join(root, "egg.yaml")
	if err := os.Rename(path, filepath.Join(root, "policy.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"internal symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "internal symlink" {
				if err := os.Symlink("policy.yaml", path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			for _, role := range []string{"member", "admin"} {
				start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: role, CWD: root}
				if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "regular file") {
					t.Fatalf("non-regular root policy accepted: %v", err)
				}
			}
		})
	}
}

func TestRoostPolicyCanonicalCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("on-disk case canonicalization uses Darwin F_GETPATH")
	}
	home, _, child, wc := roostPolicyFixture(t)
	alias := filepath.Join(home, "RESTRICTED", "sub")
	info, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("case-sensitive filesystem")
	}
	if err != nil || !info.IsDir() {
		t.Fatalf("inspect case alias: %v", err)
	}
	if got := wingpolicy.CanonicalSessionPath(alias); got != filepath.Join(child, "sub") {
		t.Fatalf("cwd kept caller-supplied case: %q", got)
	}
	wc.Paths[1].Path = filepath.Join(home, "rESTRICTED")
	start := ws.PTYStart{UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: alias}
	if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), child) {
		t.Fatalf("case variant bypassed selected-root ACL: %#v, %v", cfg, err)
	}
	start.UserID, start.Email = "bob", "bob@example.com"
	if cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err != nil || cfg.Shell != child {
		t.Fatalf("case variant did not select authorized child policy: %#v, %v", cfg, err)
	}
	wc.Paths = config.PathList{{Path: home}, {Path: filepath.Join(home, "rESTRICTED")}}
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), "nested roost paths") || !strings.Contains(err.Error(), child) || !strings.Contains(err.Error(), home) {
		t.Fatalf("case variant bypassed nested-root rejection: %v", err)
	}
}

func TestRoostPolicyOmittedCWDUsesFirstAccessibleRoot(t *testing.T) {
	home, _, child, wc := roostPolicyFixture(t)
	for _, shared := range []bool{false, true} {
		wc.Org = "org"
		if shared {
			wc.Org = ""
		}
		start := ws.PTYStart{UserID: "bob", Email: "bob@example.com", OrgRole: "member"}
		cfg, _, err := PrepareBrowserLaunch(wc, &start, home, shared, egg.DefaultEggConfig())
		if err != nil || start.CWD != child || cfg.Shell != child {
			t.Fatalf("omitted cwd lost first accessible root (shared=%v): %#v, %s, %v", shared, cfg, start.CWD, err)
		}
	}
	if err := os.WriteFile(filepath.Join(child, "egg.yaml"), []byte(fmt.Sprintf("base: %s\n", filepath.Join(child, "base.yaml"))), 0600); err != nil {
		t.Fatal(err)
	}
	start := ws.PTYStart{UserID: "bob", Email: "bob@example.com", OrgRole: "member"}
	if _, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig()); err == nil || !strings.Contains(err.Error(), filepath.Join(child, "egg.yaml")) {
		t.Fatalf("omitted cwd bypassed strict policy checks: %v", err)
	}
}

func TestRoostPolicyPinsRootsAndAncestors(t *testing.T) {
	home, root, other, wc := roostPolicyFixture(t)
	role := filepath.Join(root, "role")
	if err := os.Mkdir(role, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(role, "egg.yaml"), []byte("base: none\nfs: [rw:"+home+"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wc.Paths[0].Path = role
	missing := filepath.Join(home, "absent", "role")
	wc.Paths = append(wc.Paths, config.PathEntry{Path: missing})
	for _, cwd := range []string{role, home, other} {
		if cwd == other {
			if err := os.Remove(filepath.Join(other, "egg.yaml")); err != nil {
				t.Fatal(err)
			}
		}
		start := ws.PTYStart{UserID: "admin", OrgRole: "admin", CWD: cwd}
		cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, &egg.EggConfig{FS: []string{"rw:" + home}})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, path := range []string{role, other, missing} {
			for dir := path; dir != "/"; dir = filepath.Dir(dir) {
				if !slices.Contains(want, dir) {
					want = append(want, dir)
				}
			}
		}
		if got := cfg.ToSandboxConfig(home).DenyRename; !slices.Equal(got, want) {
			t.Fatalf("root pins (cwd=%s) = %v, want %v", cwd, got, want)
		}
	}
}

func TestRoostPolicyPinsRegardlessOfWriteRules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fs    []string
		roots []string
		want  []string
	}{
		{
			name: "Slide sibling roles",
			fs: []string{"deny:/", "ro:/usr", "rw:/opt/wingthing/a",
				"deny:/opt/wingthing/b", "deny-write:/opt/wingthing/a/egg.yaml"},
			roots: []string{"/opt/wingthing/a", "/opt/wingthing/b"},
			want:  []string{"/opt/wingthing/a", "/opt/wingthing", "/opt", "/opt/wingthing/b"},
		},
		{
			name: "default base outside HOME",
			fs:   egg.DefaultEggConfig().FS, roots: []string{"/work/team/role"},
			want: []string{"/work/team/role", "/work/team", "/work"},
		},
		{
			name: "macOS writable parent",
			fs:   []string{"rw:/work"}, roots: []string{"/work/role"},
			want: []string{"/work/role", "/work"},
		},
		{
			name: "writable ancestor",
			fs:   []string{"rw:/work"}, roots: []string{"/work/group/role"},
			want: []string{"/work/group/role", "/work/group", "/work"},
		},
		{
			name: "read-only parent",
			fs:   []string{"rw:/work", "ro:/work/group"}, roots: []string{"/work/group/role"},
			want: []string{"/work/group/role", "/work/group", "/work"},
		},
		{
			name: "denied parent",
			fs:   []string{"rw:/work", "deny:/work/group"}, roots: []string{"/work/group/role"},
			want: []string{"/work/group/role", "/work/group", "/work"},
		},
		{
			name: "write-denied parent",
			fs:   []string{"rw:/work", "deny-write:/work/group"}, roots: []string{"/work/group/role"},
			want: []string{"/work/group/role", "/work/group", "/work"},
		},
		{
			name: "relative fallback grant",
			fs:   []string{"rw:.."}, roots: []string{"/work/role"},
			want: []string{"/work/role", "/work"},
		},
		{
			name: "duplicate roots",
			fs:   []string{"rw:/work"}, roots: []string{"/work/role", "/work/role"},
			want: []string{"/work/role", "/work"},
		},
		{
			name: "no writable grants", roots: []string{"/work/role"},
			want: []string{"/work/role", "/work"},
		},
		{
			name: "filesystem root", roots: []string{"/"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := protectRoostRootPolicies(&egg.EggConfig{FS: tc.fs}, tc.roots)
			if got := cfg.ToSandboxConfig("").DenyRename; !slices.Equal(got, tc.want) {
				t.Fatalf("root pins = %v, want %v", got, tc.want)
			}
		})
	}
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	root := filepath.Join(home, "role")
	cfg := protectRoostRootPolicies(&egg.EggConfig{FS: []string{"rw:~"}}, []string{root})
	if got := cfg.ToSandboxConfig(home).DenyRename; !slices.Contains(got, root) || !slices.Contains(got, home) || slices.Contains(got, "/") {
		t.Fatalf("tilde root pins = %v", got)
	}
}

func TestRoostPolicyPinsSeatbeltProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt profile is macOS-only")
	}
	home, parent, _, wc := roostPolicyFixture(t)
	root := filepath.Join(parent, "role")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("base: none\nfs: [rw:"+home+"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wc.Paths[0].Path = root
	start := ws.PTYStart{UserID: "admin", OrgRole: "admin", CWD: root}
	cfg, _, err := PrepareBrowserLaunch(wc, &start, home, true, egg.DefaultEggConfig())
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
	cmd, err := sb.Exec(context.Background(), "/bin/true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "-p" {
		t.Fatalf("missing Seatbelt profile: %v", cmd.Args)
	}
	profile := cmd.Args[2]
	allow := strings.Index(profile, fmt.Sprintf("(allow file-write* (subpath %q))", home))
	if allow < 0 {
		t.Fatal("ordinary role-root writes lost their grant")
	}
	for path := root; path != "/"; path = filepath.Dir(path) {
		rule := fmt.Sprintf("(deny file-write* (literal %q))", path)
		if strings.Index(profile, rule) <= allow {
			t.Fatalf("policy directory can be replaced through writable parent: %s", path)
		}
		if path != home && strings.Contains(profile, fmt.Sprintf("(deny file-write* (subpath %q))", path)) {
			t.Fatalf("ordinary role-root files became unwritable: %s", path)
		}
	}
	t.Run("ordinary session", func(t *testing.T) {
		if output, err := exec.Command("sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput(); err != nil {
			t.Skipf("Seatbelt execution unavailable: %v, %s", err, output)
		}
		cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", `
set -e
mkdir -p "$1/.claude" "$1/.codex"
printf settings > "$1/.claude/settings.json"
printf config > "$1/.codex/config.toml"
printf home-file > "$1/ordinary"
cd "$2"
git init -q
printf ordinary > ordinary
git add ordinary
git -c user.name=Test -c user.email=test@example.com commit -qm initial
printf changed > ordinary-new
mv ordinary-new ordinary
git add ordinary
git -c user.name=Test -c user.email=test@example.com commit -qm changed
for path in "$1" "$2" "$3"; do
  if mv "$path" "$path-old" 2>/dev/null; then exit 42; fi
done
`, "pinned-session", home, root, parent})
		if err != nil {
			t.Fatal(err)
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ordinary session failed or policy directory was replaced: %v, %s", err, output)
		}
	})
}
