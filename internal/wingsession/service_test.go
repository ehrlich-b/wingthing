package wingsession

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestMCPLaunchCanonicalSubdirectoriesAndBounds(t *testing.T) {
	home := wingpolicy.CanonicalSessionPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root, child, outside := filepath.Join(home, "work"), filepath.Join(home, "work", "child"), filepath.Join(home, "outside")
	sibling := root + "-other"
	for _, dir := range []string{child, outside, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"alias": child, "escape": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: root}}}
	s := &Service{Home: home, Policy: func() Policy { return Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	a := Authority{UserID: "alice", Role: "owner", Principal: UserPrincipal("alice"), AllowedPaths: []string{root}, EnforcePaths: true}
	for _, cwd := range []string{child, filepath.Join(root, "alias")} {
		launch, err := s.PrepareLaunch(a, cwd)
		if err != nil || launch.CWD != child {
			t.Fatalf("subdirectory %q: %+v, %v", cwd, launch, err)
		}
	}
	for _, tt := range []struct{ name, cwd, errorText string }{
		{"traversal", root + string(filepath.Separator) + "../outside", "outside"},
		{"symlink escape", filepath.Join(root, "escape"), "outside"},
		{"nonexistent", filepath.Join(root, "missing"), "no such file"},
		{"file", filepath.Join(root, "file"), "not a directory"},
		{"sibling prefix", sibling, "outside"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.PrepareLaunch(a, tt.cwd); err == nil || !strings.Contains(err.Error(), tt.errorText) {
				t.Fatalf("launch %q error = %v, want %q", tt.cwd, err, tt.errorText)
			}
		})
	}
	a.AllowedPaths = []string{outside}
	if _, err := s.PrepareLaunch(a, child); err == nil || !strings.Contains(err.Error(), "this user's wing paths") {
		t.Fatalf("authority bounds ignored: %v", err)
	}
	a.AllowedPaths = nil
	if _, err := s.PrepareLaunch(a, child); err == nil {
		t.Fatal("empty enforced grant admitted")
	}
	web, err := s.PrepareLaunch(Authority{UserID: "alice", Role: "owner", Browser: true}, child)
	if err != nil || web.CWD != root {
		t.Fatalf("browser exact-root behavior changed: %+v, %v", web, err)
	}
}

func TestLaunchUsesOnePolicyForWebAndMCP(t *testing.T) {
	root := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", root)
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "state"))
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: work}}}
	policy := egg.DefaultEggConfig()
	s := &Service{Config: &config.Config{Dir: filepath.Join(root, "state")}, Home: root, Policy: func() Policy { return Policy{Wing: wc, Egg: policy} }}
	web, err := s.PrepareLaunch(Authority{UserID: "alice", Role: "owner", Browser: true}, work)
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := s.PrepareLaunch(Authority{UserID: "alice", Role: "owner", Principal: UserPrincipal("alice")}, work)
	if err != nil {
		t.Fatal(err)
	}
	if web.CWD != mcp.CWD || web.Identity.UserID != mcp.Identity.UserID || web.Config.NetworkSummary() != mcp.Config.NetworkSummary() {
		t.Fatalf("launch policy diverged: %+v %+v", web, mcp)
	}
	wc.Locked = true
	if _, err = s.PrepareLaunch(Authority{UserID: "alice", Role: "owner"}, work); err == nil {
		t.Fatal("MCP bypassed passkey policy")
	}
	if _, err = s.PrepareLaunch(Authority{UserID: "alice", Role: "owner", Browser: true}, work); err == nil {
		t.Fatal("web bypassed passkey policy")
	}
}

func TestSessionOwnershipBindsWebAndRemoteMCPWithoutCrossingLogicalOwners(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Dir: root}
	s := &Service{Config: cfg}
	dir := filepath.Join(root, "eggs", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.meta": "cwd=" + root + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := eggclient.WriteEggOwner(dir, "alice", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	session := eggclient.LocalSession{ID: "fixture", CWD: root}
	web := Authority{UserID: "alice", Browser: true}
	mcp := Authority{UserID: "alice", Principal: UserPrincipal("alice")}
	if !s.Owns(web, session) || !s.Owns(mcp, session) {
		t.Fatal("web session is not controllable by its owner's MCP")
	}
	for _, a := range []Authority{{UserID: "bob", Browser: true}, {UserID: "bob", Principal: UserPrincipal("bob")}, {UserID: "alice", Principal: "other"}, {UserID: "alice", Principal: mcp.Principal, EnforcePaths: true}} {
		if s.Owns(a, session) {
			t.Fatalf("foreign authority admitted: %+v", a)
		}
		if _, err := s.Stop(context.Background(), a, session.ID); err == nil {
			t.Fatal("foreign stop admitted")
		}
	}
	session.Principal = "other"
	if s.Owns(mcp, session) {
		t.Fatal("remote MCP crossed logical owner")
	}
}

func TestSealedMCPLaunchRetainsConfiguredRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	root := filepath.Join(home, "work")
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	for dir, domain := range map[string]string{root: "root.example", child: "child.example"} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nnetwork: ["+domain+"]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: root, Members: []string{"alice@example.com"}}}}
	s := &Service{Config: &config.Config{Dir: filepath.Join(home, "state")}, Home: home, Policy: func() Policy { return Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	launch, err := s.PrepareLaunch(Authority{UserID: "alice", Email: "alice@example.com", Role: "member", Principal: UserPrincipal("alice"), AllowedPaths: []string{root}, EnforcePaths: true, SealedFS: true}, child)
	if err != nil {
		t.Fatal(err)
	}
	if !eggclient.ContainsExactPath(launch.Config.Network.Domains, "root.example") || eggclient.ContainsExactPath(launch.Config.Network.Domains, "child.example") {
		t.Fatalf("MCP selected writable child policy: %+v", launch.Config)
	}
	if !launch.Identity.SealedFS {
		t.Fatal("MCP lost its sealed boundary")
	}
}

func TestWebResumeDeniesForeignSourceBeforeLaunch(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	work := filepath.Join(home, "work")
	dir := filepath.Join(home, "state", "eggs", "source")
	for _, path := range []string{work, dir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "egg.yaml"), []byte("base: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteEggOwner(dir, "bob", "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: work}}}
	snapshots := 0
	s := &Service{Config: &config.Config{Dir: filepath.Join(home, "state")}, Home: home, Policy: func() Policy { snapshots++; return Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	start := ws.PTYStart{SessionID: "new", UserID: "alice", Email: "alice@example.com", OrgRole: "member", CWD: work, Agent: "claude", ResumeSessionID: "source"}
	if _, _, _, err := s.PrepareWebStart(&start); err == nil {
		t.Fatal("foreign resume source admitted")
	}
	if snapshots != 1 {
		t.Fatalf("resume policy resolved %d times", snapshots)
	}
}
