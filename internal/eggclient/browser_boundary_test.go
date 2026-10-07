package eggclient

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"path/filepath"
	"testing"
)

func TestStandaloneOrgBrowserMemberSealsFilesystem(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	workspace := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: workspace, Members: []string{"member@example.com"}}}}
	for _, role := range []string{"member", "", "unexpected"} {
		identity := BrowserEggIdentity(wc, ws.PTYStart{UserID: "member", Email: "member@example.com", OrgRole: role}, home, false)
		if !identity.SealedFS || !identity.SharedHost {
			t.Fatalf("org browser member can read host filesystem: %#v", identity)
		}
		if _, err := sealedSharedHostEggConfig(&config.Config{Dir: filepath.Join(home, "state")}, egg.DefaultEggConfig(), workspace, identity.AllowedPaths); err == nil {
			t.Fatal("admitted host-readable default policy")
		}
	}
	for _, role := range []string{"admin", "owner"} {
		identity := BrowserEggIdentity(wc, ws.PTYStart{UserID: "admin", OrgRole: role}, home, false)
		if identity.SealedFS || identity.SharedHost {
			t.Fatalf("changed elevated org authority: %#v", identity)
		}
	}
	identity := BrowserEggIdentity(&config.WingConfig{}, ws.PTYStart{UserID: "personal"}, home, false)
	if identity.SealedFS || identity.SharedHost {
		t.Fatal("changed personal wing boundary")
	}
}
