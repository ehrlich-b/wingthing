package eggclient

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestMCPSubdirectoryRetainsRootPolicy(t *testing.T) {
	for _, rootConfig := range []bool{false, true} {
		t.Run(map[bool]string{false: "wing default", true: "root egg.yaml"}[rootConfig], func(t *testing.T) {
			home := config.CanonicalProviderPath(t.TempDir())
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
			root, child := filepath.Join(home, "work"), filepath.Join(home, "work", "child")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(child, "egg.yaml"), []byte("base: none\nfs: [rw:/]\nnetwork: '*'\nenv: '*'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if rootConfig {
				// Preserve root discovery's base-chain behavior as well.
				for name, data := range map[string]string{
					"base.yaml": "base: none\nfs: [deny:/, rw:., deny:./private, ro:../data, cache]\nnetwork: [root.example]\nenv: [SAFE]\n",
					"egg.yaml":  "base: ./base.yaml\naudit: true\nresources: {max_fds: 64}\n",
				} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			fallback := &egg.EggConfig{FS: []string{"deny:/", "rw:.", "deny:./private", "ro:../data", "cache"}, Network: egg.NetworkField{Domains: []string{"fallback.example"}}}
			wc := &config.WingConfig{Paths: config.PathList{{Path: root}}}
			atRoot := ws.PTYStart{UserID: "alice", OrgRole: "owner", CWD: root}
			rootPolicy, identity, err := PrepareBrowserLaunch(wc, &atRoot, home, false, fallback)
			if err != nil {
				t.Fatal(err)
			}
			start := ws.PTYStart{UserID: "alice", OrgRole: "owner", CWD: child}
			got, childIdentity, err := PrepareMCPLaunch(wc, &start, home, false, fallback)
			if err != nil || start.CWD != child || !reflect.DeepEqual(childIdentity, identity) {
				t.Fatalf("subdirectory launch = %+v, %+v, %v", start, childIdentity, err)
			}
			want := *rootPolicy
			want.FS = []string{"deny:/", "rw:" + root, "deny:" + filepath.Join(root, "private"), "ro:" + filepath.Join(home, "data"), filepath.Join(root, "cache")}
			if !reflect.DeepEqual(got, &want) {
				t.Fatalf("subdirectory policy = %+v, want %+v", got, &want)
			}
			if !reflect.DeepEqual(rootPolicy.FS, fallback.FS) {
				t.Fatalf("root policy was mutated: %v", rootPolicy.FS)
			}
			browser := ws.PTYStart{UserID: "alice", OrgRole: "owner", CWD: child}
			browserPolicy, _, err := PrepareBrowserLaunch(wc, &browser, home, false, fallback)
			if err != nil || browser.CWD != root || !reflect.DeepEqual(browserPolicy, rootPolicy) {
				t.Fatalf("browser root selection changed: %+v, %+v, %v", browser, browserPolicy, err)
			}
		})
	}
}

func TestMCPLaunchPreservesExplicitNestedRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root, nested := filepath.Join(home, "work"), filepath.Join(home, "work", "nested")
	child := filepath.Join(nested, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	for dir, domain := range map[string]string{root: "root.example", nested: "nested.example", child: "child.example"} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nnetwork: ["+domain+"]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, paths := range []config.PathList{{{Path: root}, {Path: nested}}, {{Path: nested}, {Path: root}}} {
		wc := &config.WingConfig{Paths: paths}
		for _, cwd := range []string{nested, child} {
			start := ws.PTYStart{UserID: "alice", OrgRole: "owner", CWD: cwd}
			cfg, _, err := PrepareMCPLaunch(wc, &start, home, false, egg.DefaultEggConfig())
			if err != nil || start.CWD != cwd || !reflect.DeepEqual(cfg.Network.Domains, []string{"nested.example"}) {
				t.Fatalf("explicit nested policy = %+v, %+v, %v", start, cfg, err)
			}
		}
	}
}
