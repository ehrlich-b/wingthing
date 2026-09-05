//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This uses the installed vendor CLI against a loopback fake API, with no real
// credentials or provider calls. The other tiers exercise Wingthing's launch path.
func TestRealClaudeModelPolicyPreservesPersonalSettings(t *testing.T) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		if os.Getenv("WT_REQUIRE_REAL_CLAUDE") == "1" {
			t.Fatal("Claude CLI is required by this gate")
		}
		t.Skip("Claude CLI is not installed")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer versionCancel()
	version, err := exec.CommandContext(versionCtx, binary, "--version").Output()
	if err != nil || !strings.Contains(string(version), "(Claude Code)") {
		if os.Getenv("WT_REQUIRE_REAL_CLAUDE") == "1" {
			t.Fatalf("vendor Claude CLI is required: version=%q err=%v", version, err)
		}
		t.Skip("installed claude is a test stand-in, not the vendor CLI")
	}
	t.Logf("vendor runtime: %s", strings.TrimSpace(string(version)))
	for _, tc := range []struct {
		name, policy, model, effort string
		extra                       []string
	}{
		{"user default without policy", `{}`, "claude-opus-5", "max", nil},
		{"current deployment", `{"model":"claude-sonnet-4-6","env":{"CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, "claude-sonnet-4-6", "max", nil},
		{"new deployment", `{"model":"claude-sonnet-5","effortLevel":"xhigh","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`, "claude-sonnet-5", "xhigh", nil},
		{"explicit session selection", `{"model":"claude-sonnet-5","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`, "claude-opus-5", "xhigh", []string{"--model", "claude-opus-5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := t.TempDir()
			t.Setenv("HOME", host)
			writePolicyFixture(t, filepath.Join(host, ".claude", "settings.json"), tc.policy)
			policy, err := isolatedClaudePolicyArgs("claude", true)
			if err != nil {
				t.Fatal(err)
			}
			user := t.TempDir()
			settings := filepath.Join(user, ".claude", "settings.json")
			const personal = `{"model":"claude-opus-5","theme":"dark","env":{"ANTHROPIC_MODEL":"claude-opus-5","CLAUDE_CODE_EFFORT_LEVEL":"max"},"effortLevel":"low"}`
			writePolicyFixture(t, settings, personal)
			writePolicyFixture(t, filepath.Join(user, ".claude", ".claude.json"), `{"hasCompletedOnboarding":true,"theme":"dark"}`)
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/v1/messages/count_tokens") {
					fmt.Fprint(w, `{"input_tokens":10}`)
					return
				}
				if r.URL.Path != "/v1/messages" {
					fmt.Fprint(w, `{}`)
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, "invalid JSON", 400)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				events := []any{
					map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": request["model"], "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}}},
					map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}},
					map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "OK"}},
					map[string]any{"type": "content_block_stop", "index": 0},
					map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}},
					map[string]any{"type": "message_stop"},
				}
				for _, event := range events {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.(map[string]any)["type"], data)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			args := append(policy, "-p", "Reply OK without tools.", "--output-format", "json", "--max-turns", "1", "--tools", "", "--no-session-persistence")
			args = append(args, tc.extra...)
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{
				"HOME=" + user, "CLAUDE_CONFIG_DIR=" + filepath.Join(user, ".claude"),
				"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color",
				"ANTHROPIC_API_KEY=fixture-not-a-real-key", "ANTHROPIC_BASE_URL=" + server.URL,
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1",
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Claude failed: %v\n%s", err, output)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) == 0 {
				t.Fatalf("no model request observed: %s", output)
			}
			for _, request := range requests {
				config, _ := request["output_config"].(map[string]any)
				if request["model"] != tc.model || config["effort"] != tc.effort {
					t.Fatalf("vendor request model=%v effort=%v, want %s/%s", request["model"], config["effort"], tc.model, tc.effort)
				}
			}
			assertPolicyFixture(t, settings, personal)
		})
	}
}
