package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestSessionForkCommandExposure(t *testing.T) {
	cmd, _, err := sessionCmd().Find([]string{"fork"})
	if err != nil || cmd.Name() != "fork" || cmd.Flags().Lookup("name") == nil || cmd.Flags().Lookup("json") == nil || cmd.Flags().Lookup("client") == nil {
		t.Fatalf("fork CLI missing: %v", err)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("accepted missing source")
	}
	if err := cmd.Args(cmd, []string{"source", "extra"}); err == nil {
		t.Fatal("accepted extra arguments")
	}
}

func TestSessionForkCLIResolvesConfiguredMCPClient(t *testing.T) {
	for _, name := range []string{"default", "explicit", "environment", "missing", "unknown", "invalid"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("WT_MCP_CLIENT", "")
			cfg := &config.Config{Dir: t.TempDir()}
			client := ""
			if name != "default" {
				if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  coordinator:\n    owner: alice\n    grants: [terminal.start]\n    bounds: {max_sessions: 3}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "explicit":
				client = "coordinator"
			case "environment":
				t.Setenv("WT_MCP_CLIENT", "coordinator")
			case "unknown":
				client = "typo"
			case "invalid":
				client = "cli:session-fork"
			}
			s, err := newLocalMCPServer(cfg, client, false)
			if name == "missing" || name == "unknown" || name == "invalid" {
				if err == nil {
					t.Fatal("accepted unusable MCP client")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Actor = "cli:session-fork"
			wantClient, wantOwner := "coordinator", "alice"
			if name == "default" {
				wantClient, wantOwner = "default", "default"
			}
			if s.MCPClient != wantClient || s.Principal != wantOwner || s.Actor != "cli:session-fork" || s.Unsandboxed || eggclient.ValidateSessionName(s.MCPClient) != nil {
				t.Fatalf("fork caller/client identity: %#v", s)
			}
			if name != "default" && (!s.Grants["terminal.start"] || s.MaxSessions != 3) {
				t.Fatalf("client lost configured grants/bounds: %#v", s)
			}
		})
	}
}

func TestSessionForkCLIReportsUnsupportedProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WINGTHING_DIR", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeResumeSessionFixture(t, cfg, "source", "", "codex", t.TempDir(), "provider", "{}\n")
	cmd := sessionForkCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("name", "branch"); err != nil {
		t.Fatal(err)
	}
	err = cmd.RunE(cmd, []string{"source"})
	if err == nil || !strings.Contains(err.Error(), "only Claude") {
		t.Fatalf("fork refusal: %v", err)
	}
}
