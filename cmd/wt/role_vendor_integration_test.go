//go:build integration

package main

import (
	"bufio"
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

	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

// No provider account, credentials, billing or outbound API is used. This
// crosses the vendor boundary: /compact runs in the installed Claude process,
// and its next model request must contain freshly injected role context.
func TestRealClaudeRoleContextAfterCompaction(t *testing.T) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		if os.Getenv("WT_REQUIRE_REAL_CLAUDE") == "1" {
			t.Fatal(err)
		}
		t.Skip("vendor Claude is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil || !strings.Contains(string(version), "(Claude Code)") {
		if os.Getenv("WT_REQUIRE_REAL_CLAUDE") == "1" {
			t.Fatalf("vendor CLI required: %q %v", version, err)
		}
		t.Skip("installed claude is a test stand-in, not the vendor CLI")
	}
	t.Logf("vendor runtime: %s", strings.TrimSpace(string(version)))
	home := testsocket.Dir(t)
	work := testsocket.Dir(t)
	const role = "you are the coordinator node. ROLE_REPOKE_FIXTURE_7351"
	settings := map[string]any{}
	if err := sessionrole.AddHook(settings, role); err != nil {
		t.Fatal(err)
	}
	// Record event sources, while the generated production hook supplies context.
	hookLog := filepath.Join(home, "hook-events.jsonl")
	hooks := settings["hooks"].(map[string]any)
	hooks["SessionStart"] = append(hooks["SessionStart"].([]any), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "cat >> '" + hookLog + "'; printf '\\n' >> '" + hookLog + "'", "timeout": 5}}})
	settingsJSON, _ := json.Marshal(settings)
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/messages/count_tokens") {
			fmt.Fprint(w, `{"input_tokens":1000}`)
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
		wire, _ := json.Marshal(request)
		mu.Lock()
		requests = append(requests, string(wire))
		mu.Unlock()
		text := "Fixture summary: the user requested a brief response. Continue responding briefly."
		if streaming, _ := request["stream"].(bool); !streaming {
			json.NewEncoder(w).Encode(map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": request["model"], "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1000, "output_tokens": 20}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": request["model"], "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 1000, "output_tokens": 0}}},
			{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": text}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}},
			{"type": "message_stop"},
		} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	defer server.Close()
	cmd := exec.CommandContext(ctx, binary, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--model", "claude-sonnet-4-6", "--tools", "", "--settings", string(settingsJSON), "--setting-sources=", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	cmd.Dir = work
	cmd.Env = []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color", "ANTHROPIC_API_KEY=fixture-not-a-real-key", "ANTHROPIC_BASE_URL=" + server.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	defer func() {
		if !joined {
			cancel()
			stdin.Close()
			cmd.Wait()
		}
	}()
	events := make(chan map[string]any, 64)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	send := func(text string) {
		t.Helper()
		wire, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
		if _, err := fmt.Fprintln(stdin, string(wire)); err != nil {
			t.Fatal(err)
		}
	}
	resumeID := ""
	waitResult := func() {
		t.Helper()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatalf("Claude output ended: %s", stderr.String())
				}
				t.Logf("vendor event: %v/%v", event["type"], event["subtype"])
				if id, _ := event["session_id"].(string); resumeID == "" && id != "" {
					resumeID = id
				}
				if event["type"] == "result" {
					if event["is_error"] == true {
						t.Fatalf("vendor error: %v", event)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	send("Reply briefly without tools.")
	waitResult()
	send("Reply briefly again without tools.")
	waitResult()
	send("/compact")
	waitResult()
	mu.Lock()
	afterCompact := len(requests)
	mu.Unlock()
	send("Reply briefly after compaction without tools.")
	waitResult()
	mu.Lock()
	if len(requests) <= afterCompact || !strings.Contains(requests[len(requests)-1], role) {
		mu.Unlock()
		t.Fatal("post-compaction model request omitted the role")
	}
	mu.Unlock()
	send("/clear")
	waitResult()
	send("Reply briefly after clear without tools.")
	waitResult()
	stdin.Close()
	err = cmd.Wait()
	joined = true
	if err != nil {
		t.Fatalf("Claude exit: %v %s", err, stderr.String())
	}
	if resumeID == "" {
		t.Fatal("vendor session identity absent")
	}
	resumed := exec.CommandContext(ctx, binary, "-p", "Reply briefly after resume without tools.", "--resume", resumeID, "--model", "claude-sonnet-4-6", "--tools", "", "--settings", string(settingsJSON), "--setting-sources=", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	resumed.Dir, resumed.Env = cmd.Dir, cmd.Env
	if output, err := resumed.CombinedOutput(); err != nil {
		t.Fatalf("vendor resume failed: %v %s", err, output)
	}
	data, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatal(err)
	}
	startup, compact, clear, resume := false, false, false, false
	for _, line := range strings.Split(string(data), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil {
			startup = startup || event["source"] == "startup"
			compact = compact || event["source"] == "compact"
			clear = clear || event["source"] == "clear"
			resume = resume || event["source"] == "resume"
			t.Logf("SessionStart source=%v", event["source"])
		}
	}
	if !startup || !compact || !clear || !resume {
		t.Fatalf("real startup/resume/clear/compact hook absent: %s", data)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) <= afterCompact || !strings.Contains(requests[len(requests)-1], role) {
		t.Fatal("post-compaction model request omitted the role")
	}
	t.Logf("verified role in startup and post-compaction model requests; %d loopback requests, %d after compaction", len(requests), len(requests)-afterCompact)
}
