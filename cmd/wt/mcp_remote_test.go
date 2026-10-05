package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestMCPTerminalListRemoteArgument(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	seedRemoteListSession(t, cfg, "local-session", "alpha")
	if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"work": {SSHTarget: "me@host"}}); err != nil {
		t.Fatal(err)
	}
	server := &localMCPServer{cfg: cfg, principal: "alpha", logs: io.Discard}
	noSSH := context.WithValue(context.Background(), remoteIOContextKey{}, remotepkg.IO{SSHPath: filepath.Join(cfg.Dir, "must-not-run-ssh")})
	local, isError, protocolErr := server.callTool(noSSH, "terminal_list", json.RawMessage(`{}`))
	if isError || protocolErr != nil {
		t.Fatalf("default list: %#v, %v", local, protocolErr)
	}
	sessions := local["sessions"].([]eggclient.LocalSession)
	if len(sessions) != 1 || sessions[0].ID != "local-session" {
		t.Fatalf("default terminal_list changed: %#v", sessions)
	}
	data, _ := json.Marshal(local)
	if strings.Contains(string(data), `"machine"`) {
		t.Fatalf("default local response gained remote fields: %s", data)
	}
	sshPath := fakeInventorySSH(t, eggclient.RemoteSessionInventory{Version: "remote-test", ContractVersion: eggclient.RemoteSessionContractVersion,
		Sessions: []eggclient.LocalSession{{ID: "remote-owned", Principal: "alpha"}, {ID: "remote-other", Principal: "beta"}}})
	ctx := context.WithValue(context.Background(), remoteIOContextKey{}, remotepkg.IO{SSHPath: sshPath})
	remote, isError, protocolErr := server.callTool(ctx, "terminal_list", json.RawMessage(`{"remote":"work"}`))
	if isError || protocolErr != nil {
		t.Fatalf("remote list: %#v, %v", remote, protocolErr)
	}
	rows := remote["sessions"].([]eggclient.MachineSession)
	if len(rows) != 1 || rows[0].ID != "remote-owned" || rows[0].Machine != "work" {
		t.Fatalf("remote argument did not select only the owned remote inventory: %#v", rows)
	}
	for _, args := range []string{`{"remote":"missing"}`, `{"remote":"me@host"}`, `{"remote":""}`, `{"remote":123}`, `{"remote":"work","extra":true}`} {
		result, isError, protocolErr := server.callTool(noSSH, "terminal_list", json.RawMessage(args))
		if !isError || protocolErr != nil || strings.Contains(result["error"].(string), "start ssh") {
			t.Errorf("invalid remote argument %s: %#v, isError=%v, protocolErr=%v", args, result, isError, protocolErr)
		}
	}
	result, isError, protocolErr := server.callTool(noSSH, "terminal_read", json.RawMessage(`{"session":"local-session","remote":"work"}`))
	if !isError || protocolErr != nil || !strings.Contains(result["error"].(string), `unknown field "remote"`) {
		t.Fatalf("another MCP tool accepted remote: %#v, %v", result, protocolErr)
	}
}

func TestMCPTerminalListRemotePreservesConnectionBounds(t *testing.T) {
	for _, server := range []*localMCPServer{
		{enforcePathBounds: true}, {boundConversation: "conversation"},
	} {
		server.cfg = &config.Config{Dir: t.TempDir()}
		ctx := context.WithValue(context.Background(), remoteIOContextKey{}, remotepkg.IO{SSHPath: filepath.Join(server.cfg.Dir, "must-not-run-ssh")})
		_, err := server.toolTerminalList(ctx, json.RawMessage(`{"remote":"work"}`))
		if err == nil || !strings.Contains(err.Error(), "bound MCP connection") {
			t.Fatalf("restricted MCP connection queried a remote: %v", err)
		}
	}
}

func TestMCPTerminalListRemoteSchemaIsLimitedToList(t *testing.T) {
	for _, tool := range localMCPTools() {
		properties := tool.InputSchema["properties"].(map[string]any)
		remote, hasRemote := properties["remote"]
		if tool.Name == "terminal_list" {
			if !hasRemote || remote.(map[string]any)["type"] != "string" {
				t.Fatalf("terminal_list remote schema missing: %#v", tool.InputSchema)
			}
			if _, required := tool.InputSchema["required"]; required {
				t.Fatal("remote argument is required")
			}
		} else if hasRemote {
			t.Errorf("%s unexpectedly exposes remote", tool.Name)
		}
	}
}
