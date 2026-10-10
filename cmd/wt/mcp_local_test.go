package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func testLocalWing(t *testing.T) *config.Config {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("WINGTHING_DIR", "state")
	t.Setenv("WT_MCP_CLIENT", "")
	cfg := &config.Config{Dir: "state", WingID: "test-wing", DefaultAgent: "claude"}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	service := &wingsession.Service{Config: cfg, Policy: func() wingsession.Policy {
		return wingsession.Policy{Wing: &config.WingConfig{WingID: cfg.WingID}, Egg: egg.DefaultEggConfig()}
	}}
	listener, err := localmcp.ListenLocalWingControl(t.Context(), "test", service, "owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return cfg
}

func TestNoWingFailsWithoutSpawning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("WINGTHING_DIR", dir)
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio"})
	cmd.SetIn(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "wt wing start --local-only") {
		t.Fatalf("no wing error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("client created state: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("client served MCP without wing: %s", out.String())
	}
}

func TestLocalMCPRejectsUnknownConfiguredClient(t *testing.T) {
	cfg := testLocalWing(t)
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  observer:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio", "--client", "typo"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `MCP client "typo" is not configured`) {
		t.Fatalf("unknown client error = %v", err)
	}
}

func TestLocalMCPRejectsImplicitDefaultWhenAnyClientsAreConfigured(t *testing.T) {
	cfg := testLocalWing(t)
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("clients:\n  observer:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `MCP client "default" is not configured`) {
		t.Fatalf("implicit default error = %v", err)
	}
}
