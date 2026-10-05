package main

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
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestSocketPathCLIFailsBeforeStateOrProcessCreation(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	for _, channel := range []string{"stable", "preview"} {
		config.ReleaseChannel = channel
		for _, args := range [][]string{
			{"terminal", "--detach", "--json", "--", "/bin/sh"},
			{"egg", "codex", "--detach", "--json"},
			{"egg", "run", "--session-id", "socket-fixture", "--agent", "codex"},
		} {
			state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
			t.Setenv("WINGTHING_DIR", state)
			err := executeCLI(context.Background(), args, remotepkg.IO{})
			assertSocketPathFailure(t, err)
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("%s %v wrote state before rejecting path: %v", channel, args, err)
			}
		}
	}
}

func TestSocketPathSpawnRejectsBeforeEggOrProcessCreation(t *testing.T) {
	state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	// A nil policy would fail later; address rejection must precede all setup.
	_, err := spawnEgg(&config.Config{Dir: state}, "socket-fixture", "", nil, 24, 80, "", false, false, false, EggIdentity{}, 0)
	assertSocketPathFailure(t, err)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("spawn wrote state: %v", err)
	}
}

func TestSocketPathMCPReportsReadableFailureWithoutSessionCreation(t *testing.T) {
	cwd := t.TempDir()
	for _, tool := range []string{"terminal_start", "agent_start"} {
		state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
		t.Setenv("WINGTHING_DIR", state)
		t.Setenv("WINGTHING_PREVIEW_DIR", "")
		server := &localMCPServer{cfg: &config.Config{Dir: state}, logs: &bytes.Buffer{}}
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
