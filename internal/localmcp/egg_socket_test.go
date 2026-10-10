package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func assertSocketPathFailure(t *testing.T, err error) {
	t.Helper()
	var tooLong *egg.SocketPathTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("expected socket path preflight failure, got %v", err)
	}
	for _, fragment := range []string{fmt.Sprintf("%q", tooLong.Path), fmt.Sprintf("%d bytes", len(tooLong.Path)), "WINGTHING_DIR", "shorter"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error missing %q: %v", fragment, err)
		}
	}
}

func TestSocketPathMCPReportsReadableFailureWithoutSessionCreation(t *testing.T) {
	cwd := t.TempDir()
	for _, tool := range []string{"terminal_start", "agent_start"} {
		state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
		t.Setenv("WINGTHING_DIR", state)
		t.Setenv("WINGTHING_PREVIEW_DIR", "")
		server := testWingServer(t, &Server{Version: "dev", Cfg: &config.Config{Dir: state}, Logs: &bytes.Buffer{}})
		args, err := json.Marshal(map[string]any{"cwd": cwd, "agent": "codex"})
		if tool == "terminal_start" {
			args, err = json.Marshal(map[string]any{"cwd": cwd, "command": []string{"/bin/sh"}})
		}
		if err != nil {
			t.Fatal(err)
		}
		var spawnErr error
		if tool == "terminal_start" {
			_, spawnErr = server.toolTerminalStart(args)
		} else {
			_, spawnErr = server.toolAgentStart(args)
		}
		assertSocketPathFailure(t, spawnErr)
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatalf("MCP spawn wrote state: %v", err)
		}
		data, isError, protocolErr := server.callTool(context.Background(), tool, args)
		if protocolErr != nil || !isError {
			t.Fatalf("MCP did not return a tool failure: %#v, %v, %v", data, isError, protocolErr)
		}
		message, _ := data["error"].(string)
		for _, fragment := range []string{filepath.Join(state, "eggs"), "egg.sock", "bytes", "WINGTHING_DIR", "shorter"} {
			if !strings.Contains(message, fragment) {
				t.Fatalf("MCP error missing %q: %s", fragment, message)
			}
		}
		// MCP call auditing remains intact, but there is no egg/session state.
		if _, err := os.Stat(filepath.Join(state, "eggs")); !os.IsNotExist(err) {
			t.Fatalf("MCP created session state: %v", err)
		}
		if _, err := os.Stat(filepath.Join(state, "mcp-audit.log")); err != nil {
			t.Fatalf("MCP refusal was not audited: %v", err)
		}
	}
}
