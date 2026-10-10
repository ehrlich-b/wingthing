package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"golang.org/x/sys/unix"
)

func TestSessionListingsDoNotWaitForLifecycleLock(t *testing.T) {
	for _, entrypoint := range []string{"discovery", "session ps", "attach selection", "MCP terminal_list"} {
		t.Run(entrypoint, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			t.Setenv("WINGTHING_DIR", cfg.Dir)
			seedRemoteListSession(t, cfg, "locked-session", "")
			lock, err := os.OpenFile(filepath.Join(cfg.Dir, "eggs", "locked-session", "lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			wingCall := testWingTool(t, cfg, "alice")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				var sessions []eggclient.LocalSession
				var err error
				var output bytes.Buffer
				switch entrypoint {
				case "discovery":
					sessions, err = eggclient.DiscoverActiveSessions(ctx, cfg)
				case "session ps":
					err = executeCLI(ctx, []string{"session", "ps", "--json"}, remotepkg.IO{Out: &output})
					if err == nil {
						err = json.Unmarshal(output.Bytes(), &sessions)
					}
				case "attach selection":
					_, err = selectActiveSession(ctx, cfg)
					// Without an interactive terminal, selection stops after listing.
					if err != nil && strings.Contains(err.Error(), "interactive selection requires a terminal") {
						err = nil
					}
				case "MCP terminal_list":
					var listed map[string]any
					listed, err = wingCall(ctx, "terminal_list", json.RawMessage(`{}`))
					if err == nil {
						data, _ := json.Marshal(listed["sessions"])
						err = json.Unmarshal(data, &sessions)
					}
				}
				if err == nil && entrypoint != "attach selection" && (len(sessions) != 1 || sessions[0].Status != "unknown") {
					err = fmt.Errorf("locked session must remain listed with unknown status: %#v", sessions)
				}
				result <- err
			}()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(250 * time.Millisecond):
				// Release and join the reader even on the unfixed implementation.
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				<-result
				t.Fatal("listing blocked on lifecycle.lock after context cancellation")
			}
		})
	}
}

func TestEggRunFlagParserPreservesEmptyAndWhitespaceAgentArgs(t *testing.T) {
	command := eggRunCmd()
	if err := command.ParseFlags([]string{
		"--session-id", "argv-canary", "--agent-arg=--tools", "--agent-arg=", "--agent-arg= \t ",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := command.Flags().GetStringArray("agent-arg")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--tools", "", " \t "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed agent argv = %#v, want %#v", got, want)
	}
}

func TestEggRunRejectsTraversalSessionIDBeforeEnvironmentRead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	payload := filepath.Join(state, "escape", ".egg.env")
	if err := os.MkdirAll(filepath.Dir(payload), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte(`{"SECRET":"must-survive"}`), 0600); err != nil {
		t.Fatal(err)
	}

	command := eggRunCmd()
	command.SetArgs([]string{"--session-id", "../escape", "--env-file-required"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "invalid session ID") {
		t.Fatalf("traversal session error = %v", err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("invalid session ID consumed environment payload: %v", err)
	}
}
