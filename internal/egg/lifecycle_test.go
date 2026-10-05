package egg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
