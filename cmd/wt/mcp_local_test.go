package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

func TestLocalAndHTTPMCPShareControlRegistry(t *testing.T) {
	local := make(map[string]localmcp.LocalMCPTool)
	for _, tool := range localmcp.LocalMCPTools() {
		local[tool.Name] = tool
	}
	cfg := &config.Config{WingID: "embedded-wing"}
	native := roostMCPControlTools(relay.NewServer(nil, relay.ServerConfig{}), cfg, false)
	httpDefinitions := control.Tools(control.SurfaceHTTPMCP)
	if len(native) != len(httpDefinitions) {
		t.Fatalf("HTTP native tools = %d, registry = %d", len(native), len(httpDefinitions))
	}
	for index, want := range httpDefinitions {
		got := native[index]
		if got.Name != want.Name || got.Title != want.Title || got.Description != want.Description {
			t.Errorf("HTTP tool %d metadata = %q/%q/%q, want %q/%q/%q",
				index, got.Name, got.Title, got.Description, want.Name, want.Title, want.Description)
		}
		if !reflect.DeepEqual(got.InputSchema, want.InputSchema) {
			t.Errorf("%s HTTP schema differs from registry", want.Name)
		}
		if !reflect.DeepEqual(got.Annotations, want.Annotations) {
			t.Errorf("%s HTTP annotations differ from registry", want.Name)
		}
		if want.Authority == control.AuthorityWing && !reflect.DeepEqual(local[want.Name].InputSchema, want.InputSchema) {
			t.Errorf("%s local schema differs from registry", want.Name)
		}
	}
}

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
