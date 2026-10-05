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
			if before.State != "working" || before.StateSource != "native_import" || !before.HasMore {
				t.Fatalf("live unterminated tail stopped being pending: %+v", before)
			}
			if err := recordSessionProcessExit(dir, 0, false); err != nil {
				t.Fatal(err)
			}
			v := lifecycleRead(t, dir, home, cwd, before.Cursor, 10)
			if v.State != state || v.StateSource != "claude_hook" || v.ProcessAlive || v.Ready || v.HasMore {
				t.Fatalf("ended process remained pending on permanent tail: %+v", v)
			}
			if len(v.Events) != 2 || v.Events[1].Type != "provider_warning" || !strings.Contains(v.Events[1].Reason, "unterminated") || v.Events[1].SourceOffset != int64(len(tail)) {
				t.Fatalf("unterminated tail warning or offset missing: %+v", v.Events)
			}
			if again := lifecycleRead(t, dir, home, cwd, v.Cursor, 10); again.State != state || again.HasMore || len(again.Events) != 0 || again.HeadCursor != v.HeadCursor {
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
			if link.name == "symlink" {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "target settings" {
					t.Fatalf("symlink target changed: %s %v", data, err)
				}
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
