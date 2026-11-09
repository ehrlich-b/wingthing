package eggclient

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestRoleResumeTransfersPinAndForkDoesNotDuplicateIt(t *testing.T) {
	cfg := &config.Config{Dir: testsocket.Dir(t)}
	source := filepath.Join(cfg.Dir, "eggs", "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []bool{true, false} {
		if err := sessionrole.Write(source, sessionrole.Metadata{Role: "coordinator", Pinned: pinned}); err != nil {
			t.Fatal(err)
		}
		m, err := spawnRoleMetadata(cfg, SpawnEggOpts{ResumeSourceSessionID: "source", RoleMetadata: sessionrole.Metadata{Role: "other", Pinned: !pinned}})
		if err != nil || m.Role != "coordinator" || m.Pinned != pinned {
			t.Fatalf("resume=%v %v", m, err)
		}
		m, err = spawnRoleMetadata(cfg, SpawnEggOpts{ResumeSourceSessionID: "source", ForkSession: true})
		if err != nil || m != (sessionrole.Metadata{}) {
			t.Fatalf("fork duplicated role: %v %v", m, err)
		}
	}
	if _, err := spawnRoleMetadata(cfg, SpawnEggOpts{ResumeSourceSessionID: "../source"}); err == nil {
		t.Fatal("unvalidated source accepted")
	}
	if m, err := spawnRoleMetadata(cfg, SpawnEggOpts{}); err != nil || m != (sessionrole.Metadata{}) {
		t.Fatalf("ordinary=%v %v", m, err)
	}
}

func TestRoleResumeRequiresProtectedMailboxAndOrdinaryResumesDoNot(t *testing.T) {
	cfg := &config.Config{Dir: testsocket.Dir(t)}
	dir := filepath.Join(cfg.Dir, "eggs", "source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStandaloneRoleResume(cfg, "source"); err != nil {
		t.Fatalf("ordinary resume blocked: %v", err)
	}
	if err := validateRoleSpawn(SpawnEggOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStandaloneRoleResume(cfg, "source"); err == nil {
		t.Fatal("standalone role resume accepted")
	}
	opts := SpawnEggOpts{RoleMetadata: sessionrole.Metadata{Role: "coordinator", Pinned: true}}
	if err := validateRoleSpawn(opts); err == nil {
		t.Fatal("role spawned without protected MCP")
	}
	opts.ProtectedWriteTargets = []string{cfg.Dir}
	opts.OmitBrowserBridge = true
	if err := validateRoleSpawn(opts); err == nil {
		t.Fatal("role spawned without injected MCP")
	}
	opts.AgentArgs = []string{"--strict-mcp-config", "--mcp-config", "/fixture/mcp.json"}
	if err := validateRoleSpawn(opts); err != nil {
		t.Fatal(err)
	}
}
