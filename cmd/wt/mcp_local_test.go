package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalMCPRejectsUnknownConfiguredClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	configData := []byte("require_client: true\nclients:\n  observer:\n    grants: [terminal.read]\n")
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), configData, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio", "--client", "typo"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `MCP client "typo" is not configured`) {
		t.Fatalf("unknown client error = %v", err)
	}
}

func TestLocalMCPRejectsImplicitDefaultWhenAnyClientsAreConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	configData := []byte("clients:\n  observer:\n    grants: [terminal.read]\n")
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), configData, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `MCP client "default" is not configured`) {
		t.Fatalf("implicit default error = %v", err)
	}
}
