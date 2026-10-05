package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// The test executable doubles as a fake agent binary. It receives the actual
// installed hook commands and invokes them with native JSON on stdin. Its
// stdout acknowledges publication only; no terminal bytes determine status.
func TestAgentStatusFakeAgent(t *testing.T) {
	if os.Getenv("WT_STATUS_FAKE_AGENT") != "1" {
		return
	}
	var commands map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("WT_STATUS_FAKE_HOOKS")), &commands); err != nil {
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		event := scanner.Text()
		payload, _ := json.Marshal(map[string]string{"session_id": "provider-exact", "hook_event_name": event})
		cmd := exec.Command("/bin/sh", "-c", commands[event])
		cmd.Stdin = bytes.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) > 0 {
			os.Exit(1)
		}
		fmt.Println(event)
	}
	os.Exit(0)
}

func captureAgentStatusPS(t *testing.T, jsonOutput bool) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = previous; _ = r.Close(); _ = w.Close() }()
	cmd := sessionCmd()
	args := []string{"ps"}
	if jsonOutput {
		args = append(args, "--json")
	}
	cmd.SetArgs(args)
	err = cmd.Execute()
	_ = w.Close()
	data, readErr := io.ReadAll(r)
	if err != nil || readErr != nil {
		t.Fatalf("session ps: %v, %v", err, readErr)
	}
	return data
}

func TestAgentStatusSessionPSAndMCPFromFakeAgentHooks(t *testing.T) {
	previous := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = previous })
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			t.Setenv("WINGTHING_DIR", cfg.Dir)
			home := t.TempDir()
			id := "fake-agent"
			dir := filepath.Join(cfg.Dir, "eggs", id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			commands := map[string]string{}
			providerID := "provider-exact"
			if agent == "claude" {
				args, err := egg.ClaudeLifecycleArgs(nil, home, id, providerID)
				if err != nil {
					t.Fatal(err)
				}
				var settings struct {
					Hooks map[string][]struct {
						Hooks []struct{ Command string }
					}
				}
				if err := json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
					t.Fatal(err)
				}
				for event, groups := range settings.Hooks {
					commands[event] = groups[0].Hooks[0].Command
				}
			} else {
				providerID = "" // Codex chooses the thread ID at SessionStart.
				args, err := egg.CodexLifecycleArgs(nil, home, id)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < len(args)-2; i += 2 {
					definition := args[i+1]
					event := strings.TrimPrefix(strings.SplitN(definition, "=", 2)[0], "hooks.")
					quoted := strings.TrimSuffix(strings.SplitN(definition, "command=", 2)[1], ",timeout=3}]}]")
					commands[event], err = strconv.Unquote(quoted)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			encoded, _ := json.Marshal(commands)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fake := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentStatusFakeAgent$")
			fake.Env = append(os.Environ(), "WT_STATUS_FAKE_AGENT=1", "WT_STATUS_FAKE_HOOKS="+string(encoded))
			input, err := fake.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := fake.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := fake.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = fake.Wait() }()
			meta := fmt.Sprintf("agent=%s\nkind=agent\ncwd=/fixture\nprovider_home=%s\nprovider_session_id=%s\n", agent, home, providerID)
			for name, content := range map[string]string{"egg.meta": meta, "egg.pid": strconv.Itoa(fake.Process.Pid)} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := egg.RecordSessionProcessEvent(dir, "session_started", "starting", ""); err != nil {
				t.Fatal(err)
			}
			ack := bufio.NewScanner(output)
			for _, step := range []struct{ event, status string }{
				{"SessionStart", "idle"},
				{"UserPromptSubmit", "working"},
				{"PermissionRequest", "blocked"},
				{"Stop", "idle"},
				{"SessionEnd", "done"},
			} {
				if _, err := fmt.Fprintln(input, step.event); err != nil {
					t.Fatal(err)
				}
				if !ack.Scan() || ack.Text() != step.event {
					t.Fatalf("fake agent failed to publish %s: %v", step.event, ack.Err())
				}
				data := captureAgentStatusPS(t, true)
				var sessions []localSession
				if err := json.Unmarshal(data, &sessions); err != nil || len(sessions) != 1 || sessions[0].Status != step.status || sessions[0].Agent != agent {
					t.Fatalf("session ps --json after %s: %s, %v", step.event, data, err)
				}
				table := string(captureAgentStatusPS(t, false))
				if !strings.Contains(table, "STATUS") || !strings.Contains(table, step.status) {
					t.Fatalf("human status omitted: %s", table)
				}
				server := &localMCPServer{cfg: cfg}
				listed, err := server.toolTerminalList(ctx, json.RawMessage(`{}`))
				if err != nil || len(listed["sessions"].([]localSession)) != 1 || listed["sessions"].([]localSession)[0].Status != step.status {
					t.Fatalf("MCP status: %v, %v", listed, err)
				}
				if summary := sessionLifecycleSummary(ctx, cfg, id); summary["status"] != step.status {
					t.Fatalf("web summary disagrees: %v", summary)
				}
			}
		})
	}
}

func TestAgentStatusLegacyEggAndBrokenJournalRemainListable(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	cfg := &config.Config{Dir: root}
	for _, id := range []string{"old-egg", "broken-journal"} {
		dir := filepath.Join(root, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{
			"egg.pid": strconv.Itoa(os.Getpid()), "egg.meta": "agent=claude\nprovider_home=" + home + "\nprovider_session_id=provider-exact\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if id == "broken-journal" {
			if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), []byte("invalid\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	sessions, err := discoverActiveSessions(context.Background(), cfg)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("legacy discovery failed: %v, %v", sessions, err)
	}
	for _, session := range sessions {
		if session.Status != "unknown" {
			t.Fatalf("old egg invented state: %+v", session)
		}
	}
}
