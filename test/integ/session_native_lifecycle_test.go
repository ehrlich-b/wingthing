//go:build e2e

package integ

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

// Drives the installed observational hook command with native protocol
// fixtures. This proves argv -> hook stdin -> atomic spool -> durable events,
// rather than claiming a mocked CLI proves vendor authentication or UI readiness.
func TestSessionNativeLifecycleProtocolReconnect(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\nprovider_session_id=ours\nprovider_home="+home+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := filepath.Base(dir)
	args, err := egg.ClaudeLifecycleArgs([]string{"--session-id", "ours"}, home, id, "ours")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err = json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
		t.Fatal(err)
	}
	drive := func(event, payload string) {
		t.Helper()
		command := settings.Hooks[event][0].Hooks[0].Command
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Stdin = strings.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) > 0 {
			t.Fatalf("native observational hook: %v %s", err, output)
		}
	}
	drive("SessionStart", "{\n\"session_id\":\"ours\",\"hook_event_name\":\"SessionStart\"\n}")
	view, err := egg.ReadSessionLifecycle(dir, "claude", "/fixture", home, "ours", true, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Ready || view.State != "idle" {
		t.Fatalf("not initialized: %+v", view)
	}
	cursor := view.Cursor
	drive("UserPromptSubmit", `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"disposable fixture"}`)
	drive("PermissionRequest", `{"session_id":"ours","hook_event_name":"PermissionRequest","tool_name":"Bash"}`)
	view, err = egg.ReadSessionLifecycle(dir, "claude", "/fixture", home, "ours", true, cursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "needs_input" || len(view.Events) != 2 {
		t.Fatalf("input wait omitted: %+v", view)
	}
	cursor = view.Cursor
	// Simulate a new wing-side reader after reconnect; records remain stable.
	drive("PostToolUse", `{"session_id":"ours","hook_event_name":"PostToolUse"}`)
	drive("Stop", `{"session_id":"ours","hook_event_name":"Stop","background_tasks":[]}`)
	view, err = egg.ReadSessionLifecycle(dir, "claude", "/fixture", home, "ours", true, cursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "completed" || view.StateSource != "claude_hook" || len(view.Events) != 2 {
		t.Fatalf("reconnect lost completion: %+v", view)
	}
	if err = egg.RecordSessionProcessEvent(dir, "session_exit", "failed", "session cancelled by caller"); err != nil {
		t.Fatal(err)
	}
	view, err = egg.ReadSessionLifecycle(dir, "claude", "/fixture", home, "ours", false, view.Cursor, 50)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "failed" || view.Ready || len(view.Events) != 1 {
		t.Fatalf("cancelled process became complete: %+v", view)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "wingthing-events", id))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("hook record permissions: %v", info.Mode())
		}
	}
}

func TestSessionPromptNativeTranscriptReceiptAfterLostConnection(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\nprovider_session_id=ours\nprovider_home="+home+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := filepath.Base(dir)
	cwd := "/fixture/multiline"
	args, err := egg.ClaudeLifecycleArgs(nil, home, id, "ours")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err = json.Unmarshal([]byte(args[len(args)-1]), &settings); err != nil {
		t.Fatal(err)
	}
	hook := func(event, payload string) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", settings.Hooks[event][0].Hooks[0].Command)
		cmd.Stdin = strings.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) > 0 {
			t.Fatalf("native hook %s: %v %s", event, err, output)
		}
	}
	hook("SessionStart", `{"session_id":"ours","hook_event_name":"SessionStart"}`)
	input := "first line\nsecond line"
	sends := 0
	lost := make(chan error, 1)
	lost <- context.Canceled
	options := egg.SessionPromptOptions{RequestID: "headless-fixture-request", Input: input, Timeout: time.Second,
		Read: func(ctx context.Context, after int64, limit int) (egg.SessionView, error) {
			return egg.ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, after, limit)
		},
		Send: func(ctx context.Context, text string) (egg.PromptDelivery, error) {
			sends++
			data, _ := json.Marshal(map[string]any{"session_id": "ours", "hook_event_name": "UserPromptSubmit", "prompt": text})
			hook("UserPromptSubmit", string(data))
			return egg.PromptDelivery{BytesEnqueued: len(text) + 1, Lost: lost}, nil
		},
	}
	result, err := egg.SubmitSessionPrompt(context.Background(), dir, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeReceiptObserved || result.ProviderRequestAcknowledged || !result.TransportEnqueued {
		t.Fatalf("hook or transport became acceptance: %+v", result)
	}
	project := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
	if err = os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"role": "user", "content": input}})
	if err = os.WriteFile(filepath.Join(project, "ours.jsonl"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = egg.SubmitSessionPrompt(context.Background(), dir, options)
	if err != nil {
		t.Fatal(err)
	}
	if sends != 1 || !result.Retried || !result.NativeReceiptObserved || result.ProviderRequestAcknowledged {
		t.Fatalf("reconnect resent multiline or claimed causal ack: %+v sends=%d", result, sends)
	}
	view, err := options.Read(context.Background(), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, event := range view.Events {
		if event.Source == "claude_transcript" && event.Role == "user" {
			users++
			if event.Text != input {
				t.Fatalf("multiline text changed: %q", event.Text)
			}
		}
	}
	if users != 1 {
		t.Fatalf("multiline created %d native user rows", users)
	}
}
