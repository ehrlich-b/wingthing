package egg

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func lifecycleFixture(t *testing.T) (dir, home, cwd, path string) {
	t.Helper()
	dir = t.TempDir()
	home = t.TempDir()
	cwd = "/fixture/shared-workspace"
	project := filepath.Join(home, Profile("claude").SessionDir, encodeCWDForClaude(cwd))
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(project, "ours.jsonl")
	return
}

func lifecycleWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func lifecycleHook(t *testing.T, home, session, name, data string) {
	t.Helper()
	dir := lifecycleHookDir(home, session)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(dir, name+".json"), data)
}
func lifecycleRead(t *testing.T, dir, home, cwd string, after int64, limit int) SessionView {
	t.Helper()
	v, err := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, after, limit)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLifecycleStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name, state, want   string
		hooks, alive, ended bool
	}{
		{"prompt", "working", "working", true, true, false},
		{"permission or elicitation", "needs_input", "blocked", true, true, false},
		{"ready", "idle", "idle", true, true, false},
		{"turn stopped", "completed", "idle", true, true, false},
		{"session ended before egg exits", "completed", "done", true, true, true},
		{"clean exit", "completed", "done", true, false, true},
		{"crash", "working", "exited", true, false, false},
		{"failed exit", "failed", "exited", true, false, true},
		{"failed live turn", "failed", "unknown", true, true, false},
		{"unsupported", "unknown", "unknown", false, true, false},
		{"transcript only", "completed", "unknown", false, true, false},
		{"legacy dead egg", "unknown", "unknown", false, false, false},
		{"exit without hooks", "completed", "unknown", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lifecycleStatus(tc.state, tc.hooks, tc.alive, tc.ended); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLifecycleClaudeStatusRequiresHooksAndDistinguishesSessionEnd(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	lifecycleWrite(t, path, `{"type":"assistant","sessionId":"ours","message":{"stop_reason":"end_turn"}}`+"\n")
	if v := lifecycleRead(t, dir, home, cwd, 0, 10); v.Status != "unknown" {
		t.Fatalf("transcript invented status: %+v", v)
	}
	for i, tc := range []struct{ event, notification, want string }{
		{"UserPromptSubmit", "", "working"},
		{"Notification", "permission_prompt", "blocked"},
		{"Notification", "elicitation_dialog", "blocked"},
		{"Notification", "elicitation_url_dialog", "blocked"},
		{"Stop", "", "idle"},
		{"Notification", "unrelated", "idle"},
		{"SessionEnd", "", "done"},
	} {
		lifecycleHook(t, home, filepath.Base(dir), fmt.Sprintf("seq.%020d", i+1), fmt.Sprintf(`{"session_id":"ours","hook_event_name":%q,"notification_type":%q}`, tc.event, tc.notification))
		v := lifecycleRead(t, dir, home, cwd, 0, 10)
		if v.Status != tc.want {
			t.Fatalf("%s/%s: %+v", tc.event, tc.notification, v)
		}
	}
	v, err := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", false, 0, 10)
	if err != nil || v.Status != "done" || v.Ready {
		t.Fatalf("lost clean native end: %+v, %v", v, err)
	}
}

func TestLifecycleMissingProcessWithHooksReportsExited(t *testing.T) {
	dir, home, cwd, _ := lifecycleFixture(t)
	lifecycleHook(t, home, filepath.Base(dir), "prompt", `{"session_id":"ours","hook_event_name":"UserPromptSubmit"}`)
	v, err := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", false, 0, 10)
	if err != nil || v.Status != "exited" || v.ProcessAlive || v.Ready {
		t.Fatalf("unclean process loss: %+v, %v", v, err)
	}
}

func TestLifecycleExactIdentityPartialReplayAndConcurrentReaders(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	lifecycleWrite(t, filepath.Join(filepath.Dir(path), "other.jsonl"), `{"type":"assistant","sessionId":"other","message":{"content":"foreign-secret","stop_reason":"end_turn"}}`+"\n")
	lifecycleWrite(t, path, `{"type":"user","sessionId":"ours","message":{"role":"user","content":"investigate"}}`+"\n"+`{"type":"assistant","sessionId":"ours","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}`)
	v := lifecycleRead(t, dir, home, cwd, 0, 10)
	if v.State != "working" || v.HeadCursor != 1 {
		t.Fatalf("partial transcript became completion: %+v", v)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, 1, 10)
			if e != nil {
				errs <- e
				return
			}
			if v.State != "completed" || v.HeadCursor != 2 || len(v.Events) != 1 || v.Events[0].Text != "done" {
				errs <- fmt.Errorf("incorrect replay: %+v", v)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	v = lifecycleRead(t, dir, home, cwd, 2, 10)
	if len(v.Events) != 0 || v.Cursor != 2 {
		t.Fatalf("duplicate replay: %+v", v)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "foreign-secret") {
		t.Fatal("selected another session in same cwd")
	}
}

func TestLifecycleEndedProcessSkipsUnterminatedTranscriptTail(t *testing.T) {
	for _, state := range []string{"completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			tail := `{"type":"user","sessionId":"ours","message":{"content":"unterminated tail"}}`
			lifecycleWrite(t, path, tail)
			hook := "Stop"
			if state == "failed" {
				hook = "StopFailure"
			}
			lifecycleHook(t, home, filepath.Base(dir), "final", fmt.Sprintf(`{"session_id":"ours","hook_event_name":%q}`, hook))
			before := lifecycleRead(t, dir, home, cwd, 0, 10)
			status := "idle"
			if state == "failed" {
				status = "unknown"
			}
			if before.State != state || before.Status != status || before.StateSource != "claude_hook" || !before.HasMore {
				t.Fatalf("live unterminated tail hid native hook state: %+v", before)
			}
			if err := recordSessionProcessExit(dir, 0, false); err != nil {
				t.Fatal(err)
			}
			v := lifecycleRead(t, dir, home, cwd, before.Cursor, 10)
			status = "done"
			if state == "failed" {
				status = "exited"
			}
			if v.State != state || v.Status != status || v.StateSource != "claude_hook" || v.ProcessAlive || v.Ready || v.HasMore {
				t.Fatalf("ended process remained pending on permanent tail: %+v", v)
			}
			if len(v.Events) != 2 || v.Events[1].Type != "provider_warning" || !strings.Contains(v.Events[1].Reason, "unterminated") || v.Events[1].SourceOffset != int64(len(tail)) {
				t.Fatalf("unterminated tail warning or offset missing: %+v", v.Events)
			}
			if again := lifecycleRead(t, dir, home, cwd, v.Cursor, 10); again.State != state || again.Status != status || again.HasMore || len(again.Events) != 0 || again.HeadCursor != v.HeadCursor {
				t.Fatalf("permanent tail was retried: %+v", again)
			}
		})
	}
}

func TestLifecycleEndedProcessKeepsBatchImportPending(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	row := `{"type":"assistant","sessionId":"ours","message":{"content":"done","stop_reason":"end_turn"}}` + "\n"
	lifecycleWrite(t, path, strings.Repeat(row, 501)+`{"type":`)
	if err := recordSessionProcessExit(dir, 0, false); err != nil {
		t.Fatal(err)
	}
	v := lifecycleRead(t, dir, home, cwd, 0, 200)
	if v.State != "working" || v.StateSource != "native_import" || v.StateCursor != 0 || !v.HasMore || v.ProcessAlive || v.Ready {
		t.Fatalf("ended process reported completion before import caught up: %+v", v)
	}
	v = lifecycleRead(t, dir, home, cwd, v.HeadCursor, 10)
	if v.State != "completed" || v.StateSource != "claude_transcript" || v.HasMore || len(v.Events) != 2 || v.Events[1].Type != "provider_warning" {
		t.Fatalf("ended process did not finish batch and warn about tail: %+v", v)
	}
}

func TestLifecyclePendingImportStatusUsesFinalState(t *testing.T) {
	for _, tc := range []struct{ name, exitState, state, status string }{
		{"live", "", "completed", "idle"},
		{"clean exit", "completed", "completed", "done"},
		{"failed exit", "failed", "failed", "exited"},
		{"stopped exit", "stopped", "stopped", "exited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			lifecycleHook(t, home, filepath.Base(dir), "final", `{"session_id":"ours","hook_event_name":"Stop"}`)
			if tc.exitState != "" {
				if err := RecordSessionProcessEvent(dir, "session_exit", tc.exitState, tc.name); err != nil {
					t.Fatal(err)
				}
			}
			row := `{"type":"assistant","sessionId":"ours","message":{"content":"done","stop_reason":"end_turn"}}` + "\n"
			lifecycleWrite(t, path, strings.Repeat(row, 501))
			v := lifecycleRead(t, dir, home, cwd, 0, 200)
			if v.State != tc.state || v.Status != tc.status || v.ProcessAlive != (tc.exitState == "") || v.Ready || !v.HasMore {
				t.Fatalf("pending import status disagrees with final state: %+v", v)
			}
			v = lifecycleRead(t, dir, home, cwd, v.HeadCursor, 10)
			state, status := tc.state, tc.status
			if tc.exitState == "" {
				state, status = "completed", "idle"
			} else if tc.exitState == "completed" {
				state, status = "completed", "done"
			}
			if v.State != state || v.Status != status || v.HasMore {
				t.Fatalf("finished import status disagrees with final state: %+v", v)
			}
		})
	}
}

func TestLifecycleTranscriptPendingKeepsHookState(t *testing.T) {
	for _, backlog := range []string{"unterminated", "batch"} {
		for _, hook := range []struct{ event, state, status string }{
			{"PermissionRequest", "needs_input", "blocked"},
			{"Stop", "completed", "idle"},
			{"StopFailure", "failed", "unknown"},
		} {
			t.Run(backlog+"/"+hook.event, func(t *testing.T) {
				dir, home, cwd, path := lifecycleFixture(t)
				lifecycleHook(t, home, filepath.Base(dir), "seq.00000000000000000001", `{"session_id":"ours","hook_event_name":"SessionStart"}`)
				lifecycleHook(t, home, filepath.Base(dir), "seq.00000000000000000002", fmt.Sprintf(`{"session_id":"ours","hook_event_name":%q}`, hook.event))
				before := lifecycleRead(t, dir, home, cwd, 0, 10)
				row := `{"type":"assistant","sessionId":"ours","message":{"content":"delayed transcript","stop_reason":"end_turn"}}`
				if backlog == "batch" {
					row = strings.Repeat(row+"\n", 501)
				}
				lifecycleWrite(t, path, row)
				v := lifecycleRead(t, dir, home, cwd, before.Cursor, 10)
				if v.State != hook.state || v.Status != hook.status || v.StateSource != "claude_hook" || v.StateCursor != before.StateCursor || v.Reason != before.Reason || v.Ready != before.Ready || !v.HasMore {
					t.Fatalf("transcript backlog hid hook state: state=%s status=%s source=%s cursor=%d ready=%t more=%t", v.State, v.Status, v.StateSource, v.StateCursor, v.Ready, v.HasMore)
				}
			})
		}
	}
}

func TestLifecycleHookPendingKeepsBlockedOrUnknownState(t *testing.T) {
	for _, state := range []string{"needs_input", "unknown"} {
		t.Run(state, func(t *testing.T) {
			dir, home, cwd, _ := lifecycleFixture(t)
			for i := 1; i <= 501; i++ {
				data := `{"session_id":"ours","hook_event_name":"Notification","notification_type":"unrelated"}`
				if i == 1 {
					data = `{"session_id":"ours","hook_event_name":"SessionStart"}`
				} else if i == 2 {
					data = `{"session_id":"ours","hook_event_name":"PermissionRequest"}`
					if state == "unknown" {
						data = strings.Repeat("x", maxLifecycleRecord+1)
					}
				}
				lifecycleHook(t, home, filepath.Base(dir), fmt.Sprintf("seq.%020d", i), data)
			}
			v := lifecycleRead(t, dir, home, cwd, 0, 10)
			status := "blocked"
			if state == "unknown" {
				status = "unknown"
			}
			if v.State != state || v.Status != status || v.StateSource != "claude_hook" || v.StateCursor != 2 || v.Ready || !v.HasMore || v.HeadCursor != 500 {
				t.Fatalf("hook backlog hid %s state: state=%s status=%s source=%s cursor=%d ready=%t more=%t head=%d", state, v.State, v.Status, v.StateSource, v.StateCursor, v.Ready, v.HasMore, v.HeadCursor)
			}
		})
	}
}

func TestLifecycleNativeHooksRemainAuthoritativeAfterDelayedTranscript(t *testing.T) {
	for _, event := range []struct{ hook, state string }{{"Stop", "completed"}, {"PermissionRequest", "needs_input"}} {
		t.Run(event.hook, func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			lifecycleHook(t, home, filepath.Base(dir), "first", `{"session_id":"ours","hook_event_name":"SessionStart"}`)
			v := lifecycleRead(t, dir, home, cwd, 0, 10)
			if !v.Ready || v.State != "idle" {
				t.Fatalf("not natively ready: %+v", v)
			}
			lifecycleHook(t, home, filepath.Base(dir), "second", fmt.Sprintf(`{"session_id":"ours","hook_event_name":%q}`, event.hook))
			v = lifecycleRead(t, dir, home, cwd, v.Cursor, 10)
			stateCursor := v.StateCursor
			lifecycleWrite(t, path, `{"type":"assistant","sessionId":"ours","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}`+"\n")
			v = lifecycleRead(t, dir, home, cwd, v.Cursor, 10)
			if v.State != event.state || v.StateSource != "claude_hook" || v.StateCursor != stateCursor {
				t.Fatalf("delayed transcript regressed native state: %+v", v)
			}
			lifecycleHook(t, home, filepath.Base(dir), "third", `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"follow up"}`)
			v = lifecycleRead(t, dir, home, cwd, v.Cursor, 10)
			if v.State != "working" || v.StateCursor <= stateCursor {
				t.Fatalf("new prompt failed to advance state: %+v", v)
			}
		})
	}
}

func TestLifecycleTerminalFailureMissingProcessAndUnknownProvider(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	if err := RecordSessionProcessEvent(dir, "session_exit", "failed", "session cancelled by caller"); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, path, `{"type":"assistant","sessionId":"ours","message":{"content":"late flush","stop_reason":"end_turn"}}`+"\n")
	v, err := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", false, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "failed" || v.StateSource != "egg_process" || v.Ready {
		t.Fatalf("termination overwritten: %+v", v)
	}
	unknown := t.TempDir()
	v, err = ReadSessionLifecycle(unknown, "codex", cwd, home, "", true, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "unknown" || v.StateSource != "unsupported" || v.Ready {
		t.Fatalf("unsupported provider guessed ready: %+v", v)
	}
	v, err = ReadSessionLifecycle(unknown, "claude", cwd, home, "ours", false, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != "unknown" || !strings.Contains(v.Reason, "without a recorded exit") {
		t.Fatalf("lost provider was reported completed: %+v", v)
	}
}

func TestLifecyclePaginationAndCrashTailRecovery(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	var rows strings.Builder
	for i := 0; i < 205; i++ {
		fmt.Fprintf(&rows, "{\"type\":\"user\",\"sessionId\":\"ours\",\"message\":{\"content\":\"%d\"}}\n", i)
	}
	lifecycleWrite(t, path, rows.String())
	v := lifecycleRead(t, dir, home, cwd, 0, 200)
	if v.Cursor != 200 || v.HeadCursor != 205 || !v.HasMore || v.StateCursor != 205 {
		t.Fatalf("pagination lost head state: %+v", v)
	}
	f, err := os.OpenFile(filepath.Join(dir, "lifecycle.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"sequence":206`)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	v = lifecycleRead(t, dir, home, cwd, 200, 200)
	if len(v.Events) != 5 || v.Cursor != 205 || v.HasMore {
		t.Fatalf("tail recovery lost events: %+v", v)
	}
	if _, err = ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, 206, 10); err == nil {
		t.Fatal("accepted a future cursor")
	}
}

func TestClaudeLifecycleArgsStopAtNativeTerminator(t *testing.T) {
	for _, supplied := range [][]string{
		{"--session-id", "ours", "--", "--session-id"},
		{"--model", "sonnet", "--", "--", "--settings", "literal-settings"},
		{"--settings", `{"model":"sonnet","disableAllHooks":true}`, "--", "--settings=literal-settings"},
	} {
		t.Run(strings.Join(supplied, " "), func(t *testing.T) {
			home, dir, cwd := t.TempDir(), t.TempDir(), t.TempDir()
			original := slices.Clone(supplied)
			args, err := ClaudeLifecycleArgs(supplied, home, filepath.Base(dir), "ours")
			if err != nil {
				t.Fatal(err)
			}
			end := slices.Index(args, "--")
			if end < 2 || args[end-2] != "--settings" || !slices.Equal(args[end:], supplied[slices.Index(supplied, "--"):]) {
				t.Fatalf("lifecycle options changed the native prompt: %q", args)
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(args[end-1]), &settings); err != nil {
				t.Fatal(err)
			}
			if supplied[0] == "--settings" && (settings["model"] != "sonnet" || settings["disableAllHooks"] != true) {
				t.Fatalf("explicit settings changed: %v", settings)
			}
			prepared, err := prepareClaudeLifecycleArgs(supplied, home, dir, "ours", cwd)
			if err != nil {
				t.Fatal(err)
			}
			end = slices.Index(prepared, "--")
			path := filepath.Join(dir, claudeLifecycleSettingsFile)
			if end < 2 || prepared[end-2] != "--settings" || prepared[end-1] != path || !slices.Equal(prepared[end:], supplied[slices.Index(supplied, "--"):]) {
				t.Fatalf("settings file was not inserted before the native terminator: %q", prepared)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != args[slices.Index(args, "--")-1] {
				t.Fatalf("merged settings file = %q, %v", data, err)
			}
			if !slices.Equal(supplied, original) {
				t.Fatalf("caller arguments changed: %q", supplied)
			}
		})
	}
}

func TestClaudeLifecycleArgsPreserveSettingsAndNativeIdentity(t *testing.T) {
	home := t.TempDir()
	args, err := ClaudeLifecycleArgs([]string{"--session-id", "ours", "--settings", `{"model":"sonnet","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo existing"}]}]}}`}, home, "wing-session", "ours")
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "--session-id" || args[1] != "ours" || args[2] != "--settings" {
		t.Fatalf("argv identity changed: %v", args)
	}
	var settings map[string]any
	if err = json.Unmarshal([]byte(args[3]), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["model"] != "sonnet" {
		t.Fatal("model policy lost")
	}
	hooks := settings["hooks"].(map[string]any)
	if len(hooks["Stop"].([]any)) != 2 {
		t.Fatal("existing Stop hook lost")
	}
	args, err = ClaudeLifecycleArgs([]string{"--settings", `{"disableAllHooks":true}`}, home, "disabled", "ours")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(args[1], "wingthing-events") {
		t.Fatal("overrode explicit hook disable")
	}
	if _, err = ClaudeLifecycleArgs(nil, home, "session", "../foreign"); err == nil {
		t.Fatal("accepted invalid provider identity")
	}
}

func TestClaudeLifecycleSettingsFileKeepsCredentialsPrivateAndCleansUp(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	lifecycleWrite(t, settingsPath, `{"env":{"ANTHROPIC_API_KEY":"fixture-secret"},"model":"sonnet"}`)
	args, err := prepareClaudeLifecycleArgs([]string{"--session-id", "ours", "--settings", settingsPath}, home, dir, "ours", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "fixture-secret") {
		t.Fatal("merged settings credentials exposed in argv")
	}
	path := args[len(args)-1]
	if path != filepath.Join(dir, claudeLifecycleSettingsFile) {
		t.Fatalf("settings outside private egg directory: %q", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings permissions: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["env"].(map[string]any)["ANTHROPIC_API_KEY"] != "fixture-secret" || settings["hooks"] == nil || settings["model"] != "sonnet" {
		t.Fatal("settings or lifecycle hooks lost")
	}
	// Lifecycle data retains the egg directory, but credentials must be removed.
	if err = RecordSessionProcessEvent(dir, "session_exit", "", "test cleanup"); err != nil {
		t.Fatal(err)
	}
	(&Server{dir: dir}).cleanup()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credentials survived egg cleanup: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "lifecycle.jsonl")); err != nil {
		t.Fatalf("cleanup removed retained lifecycle: %v", err)
	}
}

func TestClaudeLifecycleSettingsFileReplacesPreviousLaunch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, claudeLifecycleSettingsFile)
	lifecycleWrite(t, path, strings.Repeat("previous launch settings", 1000))
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	args, err := prepareClaudeLifecycleArgs([]string{"--settings", `{"model":"new-model","disableAllHooks":true}`}, t.TempDir(), dir, "ours", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if args[len(args)-1] != path {
		t.Fatalf("relaunch changed settings path: %v", args)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil || settings["model"] != "new-model" || settings["disableAllHooks"] != true {
		t.Fatalf("previous settings were not replaced: %s %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("reused settings permissions: %v %v", info, err)
	}
}

func TestClaudeLifecycleSettingsFileRefusesLinks(t *testing.T) {
	for _, link := range []struct {
		name   string
		create func(string, string) error
	}{{"symlink", os.Symlink}, {"hardlink", os.Link}} {
		t.Run(link.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "target.json")
			lifecycleWrite(t, target, "target settings")
			if err := link.create(target, filepath.Join(dir, claudeLifecycleSettingsFile)); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareClaudeLifecycleArgs(nil, t.TempDir(), dir, "ours", t.TempDir()); err == nil {
				t.Fatal("accepted linked settings file")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "target settings" {
				t.Fatalf("%s target changed: %s %v", link.name, data, err)
			}
		})
	}
}

func TestClaudeLifecycleSettingsResolveRelativePathsFromProviderCWD(t *testing.T) {
	for _, equal := range []bool{false, true} {
		t.Run(fmt.Sprintf("equals=%t", equal), func(t *testing.T) {
			cwd := t.TempDir()
			lifecycleWrite(t, filepath.Join(cwd, "settings.json"), `{"model":"cwd-model","disableAllHooks":true}`)
			args := []string{"--settings", "settings.json"}
			if equal {
				args = []string{"--settings=settings.json"}
			}
			out, err := prepareClaudeLifecycleArgs(args, t.TempDir(), t.TempDir(), "ours", cwd)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out[len(out)-1])
			if err != nil || !strings.Contains(string(data), "cwd-model") || strings.Contains(string(data), "wingthing-events") {
				t.Fatalf("provider cwd settings lost: %s %v", data, err)
			}
		})
	}
}

func TestLifecyclePartialNativeImportCannotReportAuthoritativeCompletion(t *testing.T) {
	for _, hooks := range []bool{false, true} {
		t.Run(fmt.Sprintf("hooks=%t", hooks), func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			var rows strings.Builder
			for i := 1; i <= 501; i++ {
				if hooks {
					event := "Stop"
					if i == 501 {
						event = "UserPromptSubmit"
					}
					lifecycleHook(t, home, filepath.Base(dir), fmt.Sprintf("seq.%020d", i), fmt.Sprintf(`{"session_id":"ours","hook_event_name":%q}`, event))
				} else if i == 501 {
					rows.WriteString(`{"type":"user","sessionId":"ours","message":{"content":"next turn"}}` + "\n")
				} else {
					rows.WriteString(`{"type":"assistant","sessionId":"ours","message":{"content":"done","stop_reason":"end_turn"}}` + "\n")
				}
			}
			if !hooks {
				lifecycleWrite(t, path, rows.String())
			}
			v := lifecycleRead(t, dir, home, cwd, 0, 200)
			if v.State != "working" || v.StateSource != "native_import" || v.StateCursor != 0 || v.Ready || !v.HasMore || v.HeadCursor != 500 {
				t.Fatalf("partial import reported stale native completion: %+v", v)
			}
			v = lifecycleRead(t, dir, home, cwd, v.HeadCursor, 10)
			if v.State != "working" || v.StateSource == "native_import" || v.StateCursor != 501 || v.HasMore || len(v.Events) != 1 {
				t.Fatalf("source head not restored after import: %+v", v)
			}
		})
	}
}

func TestLifecycleCleanProcessExitPreservesNativeTurnFailure(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			dir, home, cwd, _ := lifecycleFixture(t)
			lifecycleHook(t, home, filepath.Base(dir), "failure", `{"session_id":"ours","hook_event_name":"StopFailure"}`)
			before := lifecycleRead(t, dir, home, cwd, 0, 10)
			var err error
			if legacy {
				err = RecordSessionProcessEvent(dir, "session_exit", "completed", "provider process exited with code 0")
			} else {
				err = recordSessionProcessExit(dir, 0, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			v := lifecycleRead(t, dir, home, cwd, before.Cursor, 10)
			if v.State != "failed" || v.StateSource != "claude_hook" || v.StateCursor != before.StateCursor || v.ProcessAlive || v.Ready {
				t.Fatalf("clean process exit erased native turn failure: %+v", v)
			}
			if !legacy && (len(v.Events) != 1 || v.Events[0].State != "" || v.Events[0].ExitCode == nil || *v.Events[0].ExitCode != 0) {
				t.Fatalf("process exit did not record a separate outcome: %+v", v.Events)
			}
		})
	}
	// Readers still understand the termination records written by live old eggs
	// that have no native turn evidence, including unsupported providers.
	dir := t.TempDir()
	if err := RecordSessionProcessEvent(dir, "session_exit", "completed", "legacy exit"); err != nil {
		t.Fatal(err)
	}
	v, err := ReadSessionLifecycle(dir, "codex", "", "", "", false, 0, 10)
	if err != nil || v.State != "completed" || v.StateSource != "egg_process" || v.ProcessAlive || v.Ready {
		t.Fatalf("legacy exit fallback changed: %+v %v", v, err)
	}
}

func TestLifecycleStoppedProcessExitOverridesNativeState(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%t", pending), func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			lifecycleHook(t, home, filepath.Base(dir), "prompt", `{"session_id":"ours","hook_event_name":"UserPromptSubmit"}`)
			before := lifecycleRead(t, dir, home, cwd, 0, 10)
			if before.State != "working" || before.StateSource != "claude_hook" {
				t.Fatalf("fixture not working: %+v", before)
			}
			const reason = "provider process was killed"
			if err := RecordSessionProcessEvent(dir, "session_exit", "stopped", reason); err != nil {
				t.Fatal(err)
			}
			if pending {
				row := `{"type":"assistant","sessionId":"ours","message":{"content":"late flush","stop_reason":"end_turn"}}` + "\n"
				lifecycleWrite(t, path, strings.Repeat(row, 501))
			}
			v := lifecycleRead(t, dir, home, cwd, before.Cursor, 10)
			if v.State != "stopped" || v.Status != "exited" || v.StateSource != "egg_process" || v.StateCursor != before.HeadCursor+1 || v.Reason != reason || v.ProcessAlive || v.Ready {
				t.Fatalf("native state hid stopped process: %+v", v)
			}
			if len(v.Events) == 0 || v.Events[0].Type != "session_exit" || v.Events[0].State != "stopped" {
				t.Fatalf("process exit missing from replay: %+v", v.Events)
			}
			if pending && !v.HasMore {
				t.Fatalf("pending transcript import lost: %+v", v)
			}
			again := lifecycleRead(t, dir, home, cwd, v.HeadCursor, 10)
			if again.State != "stopped" || again.Status != "exited" || again.StateSource != "egg_process" || again.StateCursor != v.StateCursor || again.ProcessAlive || again.Ready || again.HasMore {
				t.Fatalf("later import regressed stopped process: %+v", again)
			}
		})
	}
}

func TestLifecycleSkipsOversizedNativeRecordsAndPersistsProgress(t *testing.T) {
	for _, hooks := range []bool{false, true} {
		t.Run(fmt.Sprintf("hooks=%t", hooks), func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			oversized := strings.Repeat("x", 3*maxLifecycleRecord) + "\n"
			if hooks {
				lifecycleHook(t, home, filepath.Base(dir), "seq.00000000000000000001", oversized)
				lifecycleHook(t, home, filepath.Base(dir), "seq.00000000000000000002", `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"after large row"}`)
			} else {
				lifecycleWrite(t, path, oversized+`{"type":"user","sessionId":"ours","message":{"content":"after large row"}}`+"\n")
			}
			v := lifecycleRead(t, dir, home, cwd, 0, 10)
			if len(v.Events) != 2 || v.Events[0].Type != "provider_warning" || !v.Events[0].Truncated || v.Events[0].OriginalBytes != int64(len(oversized)) || v.Events[1].Text != "after large row" {
				t.Fatalf("large row wedged native import: %+v", v)
			}
			if !hooks && v.Events[0].SourceOffset != int64(len(oversized)) {
				t.Fatalf("warning lost skipped byte offset: %+v", v.Events[0])
			}
			if again := lifecycleRead(t, dir, home, cwd, v.Cursor, 10); len(again.Events) != 0 || again.HeadCursor != v.HeadCursor {
				t.Fatalf("restart retried skipped native row: %+v", again)
			}
		})
	}
}

func TestLifecycleOversizedHookInvalidatesStateAndReadiness(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			dir, home, cwd, path := lifecycleFixture(t)
			spool := filepath.Join(home, "."+agent, "wingthing-events", filepath.Base(dir))
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(sequence int, data string) {
				t.Helper()
				lifecycleWrite(t, filepath.Join(spool, fmt.Sprintf("seq.%020d.json", sequence)), data)
			}
			read := func(after int64) SessionView {
				t.Helper()
				v, err := ReadSessionLifecycle(dir, agent, cwd, home, "ours", true, after, 10)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			write(1, `{"session_id":"ours","hook_event_name":"SessionStart"}`)
			write(2, `{"session_id":"ours","hook_event_name":"Stop"}`)
			before := read(0)
			if before.State != "completed" || before.Status != "idle" || !before.Ready {
				t.Fatalf("fixture not ready after Stop: %+v", before)
			}
			write(3, `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"`+strings.Repeat("x", maxLifecycleRecord)+`"}`)
			v := read(before.Cursor)
			if v.State != "unknown" || v.Status != "unknown" || v.Ready || v.StateSource != agent+"_hook" || v.StateCursor != before.HeadCursor+1 || !strings.Contains(v.Reason, "skipped") || len(v.Events) != 1 || !v.Events[0].Truncated {
				t.Fatalf("skipped hook retained stale completion or readiness: %+v", v)
			}
			if agent == "claude" {
				lifecycleWrite(t, path, `{"type":"assistant","sessionId":"ours","message":{"stop_reason":"end_turn"}}`+"\n")
			}
			again := read(v.Cursor)
			if again.State != "unknown" || again.Status != "unknown" || again.Ready || again.StateCursor != v.StateCursor {
				t.Fatalf("replay or transcript restored stale state: %+v", again)
			}
			write(4, `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"next turn"}`)
			if restored := read(again.Cursor); restored.State != "working" || restored.Status != "working" || !restored.Ready || restored.StateCursor <= v.StateCursor {
				t.Fatalf("later native evidence did not restore state: %+v", restored)
			}
		})
	}
}

func TestLifecycleResponseFitsEncryptedRelayEnvelopeAndReplaysEveryEvent(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	var rows strings.Builder
	large, err := json.Marshal(map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "content": strings.Repeat("large result", 40000)}}}})
	if err != nil {
		t.Fatal(err)
	}
	rows.Write(large)
	rows.WriteByte('\n')
	// Include both ordinary and JSON-escaped text to exercise serialized size.
	for i := 0; i < 12; i++ {
		text := strings.Repeat("x", 50000)
		if i%2 == 0 {
			text = strings.Repeat("\x01", 20000)
		}
		row, err := json.Marshal(map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"content": text}})
		if err != nil {
			t.Fatal(err)
		}
		rows.Write(row)
		rows.WriteByte('\n')
	}
	lifecycleWrite(t, path, rows.String())
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var cursor int64
	pages := 0
	for {
		v := lifecycleRead(t, dir, home, cwd, cursor, 200)
		if len(v.Events) == 0 {
			t.Fatal("response budget stalled cursor replay")
		}
		if pages == 0 && (!v.Events[0].Truncated || v.Events[0].OriginalBytes != int64(len(large)+1)) {
			t.Fatalf("oversized tool result omitted truncation metadata: %+v", v.Events[0])
		}
		for _, event := range v.Events {
			cursor++
			if event.Sequence != cursor {
				t.Fatalf("response skipped event %d: %d", cursor, event.Sequence)
			}
		}
		data, err := json.Marshal(map[string]any{"session": v.SessionID, "lifecycle": v})
		if err != nil {
			t.Fatal(err)
		}
		encrypted, err := auth.Encrypt(gcm, data)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := json.Marshal(ws.TunnelResponse{Type: ws.TypeTunnelResponse, RequestID: strings.Repeat("r", 240), Payload: encrypted})
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope) > 512<<10 {
			t.Fatalf("encrypted lifecycle response exceeds relay read limit: %d", len(envelope))
		}
		pages++
		if !v.HasMore {
			break
		}
		if pages > 13 {
			t.Fatal("cursor replay did not finish")
		}
	}
	if cursor != 13 || pages < 2 {
		t.Fatalf("response did not use bounded contiguous pages: cursor=%d pages=%d", cursor, pages)
	}
}

func TestLifecycleLegacyLargeReasonDoesNotStallResponseCursor(t *testing.T) {
	dir, home, cwd, _ := lifecycleFixture(t)
	j, err := openLifecycleJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A running older egg can still append rows larger than the new page budget.
	err = j.append(SessionEvent{Type: "notification", Source: "claude_hook", State: "needs_input", Reason: strings.Repeat("\x01", 60000)})
	j.close()
	if err != nil {
		t.Fatal(err)
	}
	v := lifecycleRead(t, dir, home, cwd, 0, 200)
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxLifecycleResponse || v.Cursor != 1 || v.HasMore || len(v.Events) != 1 || !v.Events[0].Truncated || v.Events[0].OriginalBytes == 0 {
		t.Fatalf("legacy large metadata exhausted response budget: bytes=%d cursor=%d events=%d", len(data), v.Cursor, len(v.Events))
	}
}

func TestLifecycleHookBackgroundTaskAndForeignInput(t *testing.T) {
	dir, home, cwd, _ := lifecycleFixture(t)
	lifecycleHook(t, home, filepath.Base(dir), "bad", `{"session_id":"foreign","hook_event_name":"UserPromptSubmit","prompt":"foreign-secret"}`)
	lifecycleHook(t, home, filepath.Base(dir), "stop", `{"session_id":"ours","hook_event_name":"Stop","background_tasks":[{"status":"running"}]}`)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(lifecycleHookDir(home, filepath.Base(dir)), "stop.json"), now, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	v := lifecycleRead(t, dir, home, cwd, 0, 10)
	if v.State != "idle" || !strings.Contains(v.Reason, "background") {
		t.Fatalf("ignored background work: %+v", v)
	}
	for _, e := range v.Events {
		if strings.Contains(e.Text, "foreign-secret") {
			t.Fatal("disclosed foreign hook prompt")
		}
	}
}

// A publisher that keeps losing the sequence name must keep retrying rather
// than fall back to a legacy name ordered before every sequenced record.
func TestLifecycleHookPublishRetriesSequenceCollisionsWithoutLegacyFallback(t *testing.T) {
	dir, home, cwd, _ := lifecycleFixture(t)
	id := filepath.Base(dir)
	args, err := ClaudeLifecycleArgs(nil, home, id, "ours")
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
	realLS, err := exec.LookPath("ls")
	if err != nil {
		t.Skip("ls unavailable")
	}
	// After arming, the first 17 listings are stale (empty), so each link
	// targets the already published seq 1 and collides for real.
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	fake := "#!/bin/sh\nc=" + count + "; if [ -f \"$c\" ]; then n=$(($(cat \"$c\")+1)); echo $n > \"$c\"; [ $n -le 17 ] && exit 0; fi\nexec " + realLS + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "ls"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	drive := func(event, payload string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // vendor hook timeout
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", settings.Hooks[event][0].Hooks[0].Command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) > 0 {
			t.Fatalf("native hook %s: %v %s", event, err, output)
		}
	}
	drive("UserPromptSubmit", `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"fixture"}`)
	drive("PreToolUse", `{"session_id":"ours","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if err = os.WriteFile(count, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	drive("PermissionRequest", `{"session_id":"ours","hook_event_name":"PermissionRequest","tool_name":"Bash"}`)
	if data, _ := os.ReadFile(count); strings.TrimSpace(string(data)) != "18" {
		t.Fatalf("expected 17 collisions then a fresh listing, got %q listings", data)
	}
	spool := lifecycleHookDir(home, id)
	entries, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	same := time.Date(2026, 10, 4, 3, 0, 53, 446596710, time.UTC)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
		if err = os.Chtimes(filepath.Join(spool, entry.Name()), same, same); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(names, ",") != "seq.00000000000000000001.json,seq.00000000000000000002.json,seq.00000000000000000003.json" {
		t.Fatalf("collision exhaustion left legacy or temp records: %v", names)
	}
	v := lifecycleRead(t, dir, home, cwd, 0, 50)
	var types []string
	for _, e := range v.Events {
		types = append(types, e.Type)
	}
	if v.State != "needs_input" || strings.Join(types, ",") != "prompt_submitted,tool_activity,input_requested" {
		t.Fatalf("late permission request reordered: %v %+v", types, v)
	}
}

// Equal mtimes plus temp names that sort in reverse must not reorder records
// published by the installed hook command, nor duplicate them on reconnect.
func TestLifecycleHookPublishOrderSurvivesEqualMtimeReversedTempNames(t *testing.T) {
	dir, home, cwd, _ := lifecycleFixture(t)
	id := filepath.Base(dir)
	args, err := ClaudeLifecycleArgs(nil, home, id, "ours")
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
	// Each mktemp call yields a name sorting before the previous one.
	bin := t.TempDir()
	fake := "#!/bin/sh\nc=" + bin + "/count; n=$(cat \"$c\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"$c\"\n" +
		"case $n in 1) s=zzzzzz;; 2) s=yyyyyy;; 3) s=xxxxxx;; *) s=aaaaa$n;; esac\n" +
		"f=$(printf '%s' \"$1\" | sed \"s/XXXXXX\\$/$s/\"); : > \"$f\" && printf '%s\\n' \"$f\"\n"
	if err = os.WriteFile(filepath.Join(bin, "mktemp"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	spool := lifecycleHookDir(home, id)
	lifecycleHook(t, home, id, "event.legacy", `{"session_id":"ours","hook_event_name":"SessionStart"}`)
	drive := func(event, payload string) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", settings.Hooks[event][0].Hooks[0].Command)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(payload)
		if output, err := cmd.CombinedOutput(); err != nil || len(output) > 0 {
			t.Fatalf("native hook %s: %v %s", event, err, output)
		}
	}
	same := time.Date(2026, 10, 4, 3, 0, 53, 446596710, time.UTC)
	flatten := func() {
		t.Helper()
		entries, err := os.ReadDir(spool)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 || !strings.HasSuffix(entry.Name(), ".json") {
				t.Fatalf("unpublished or exposed hook record: %s %v", entry.Name(), info.Mode())
			}
			if err = os.Chtimes(filepath.Join(spool, entry.Name()), same, same); err != nil {
				t.Fatal(err)
			}
		}
	}
	drive("UserPromptSubmit", `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"fixture"}`)
	drive("PermissionRequest", `{"session_id":"ours","hook_event_name":"PermissionRequest","tool_name":"Bash"}`)
	flatten()
	for _, name := range []string{"seq.00000000000000000001.json", "seq.00000000000000000002.json"} {
		if !sequencedLifecycleHook(name) {
			t.Fatalf("sequence name rejected: %s", name)
		}
		if _, err = os.Stat(filepath.Join(spool, name)); err != nil {
			t.Fatalf("not published in sequence: %v", err)
		}
	}
	v := lifecycleRead(t, dir, home, cwd, 0, 50)
	var types []string
	for _, e := range v.Events {
		types = append(types, e.Type)
	}
	if v.State != "needs_input" || strings.Join(types, ",") != "session_ready,prompt_submitted,input_requested" {
		t.Fatalf("publish order lost: %v %+v", types, v)
	}
	cursor := v.Cursor
	// A new reader after reconnect sees no duplicates, then later records in order.
	if v = lifecycleRead(t, dir, home, cwd, cursor, 50); len(v.Events) != 0 || v.State != "needs_input" {
		t.Fatalf("reconnect duplicated records: %+v", v)
	}
	drive("PostToolUse", `{"session_id":"ours","hook_event_name":"PostToolUse"}`)
	drive("Stop", `{"session_id":"ours","hook_event_name":"Stop","background_tasks":[]}`)
	flatten()
	v = lifecycleRead(t, dir, home, cwd, cursor, 50)
	if v.State != "completed" || len(v.Events) != 2 || v.Events[0].Type != "tool_activity" || v.Events[1].Type != "turn_completed" {
		t.Fatalf("later publications reordered or lost: %+v", v)
	}
	if v = lifecycleRead(t, dir, home, cwd, 0, 50); len(v.Events) != 5 {
		t.Fatalf("full replay duplicated records: %+v", v)
	}
}
