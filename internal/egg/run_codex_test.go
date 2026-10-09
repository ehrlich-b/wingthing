package egg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func codexRunFixture(t *testing.T) (rt *runTurnRuntime, home string, request RunTurnRequest, sent <-chan struct{}) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	sessionID := filepath.Base(dir)
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "agent=codex\n")
	publishHook := func(name, payload string) {
		spool := filepath.Join(home, ".codex", "wingthing-events", sessionID)
		if err := os.MkdirAll(spool, 0700); err != nil {
			t.Fatal(err)
		}
		lifecycleWrite(t, filepath.Join(spool, name+".json"), payload)
	}
	publishHook("seq.00000000000000000001", `{"session_id":"thread-exact","hook_event_name":"SessionStart"}`)
	read := func(ctx context.Context, after int64, limit int) (SessionView, error) {
		return ReadSessionLifecycle(dir, "codex", "/fixture", home, "", true, after, limit)
	}
	sentCh := make(chan struct{})
	backend := runTurnBackend{Agent: "codex", Read: read}
	backend.Prepare = func(prompt, id string) (func() (turnEvidence, error), error) {
		return codexRunScanner(home, sessionID, id, prompt, read)
	}
	backend.Send = func(ctx context.Context, prompt string) (PromptDelivery, error) {
		wire, _ := json.Marshal(map[string]string{"session_id": "thread-exact", "turn_id": "turn-exact", "hook_event_name": "UserPromptSubmit", "prompt": prompt})
		publishHook("seq.00000000000000000002", string(wire))
		close(sentCh)
		return PromptDelivery{BytesEnqueued: len(prompt) + 1}, nil
	}
	rt = newRunTurnRuntime(dir, backend)
	t.Cleanup(rt.stopActive)
	return rt, home, RunTurnRequest{RunID: "codex-run", Prompt: "inspect native fixture", Deadline: time.Now().Add(time.Hour)}, sentCh
}

func publishCodexNotify(t *testing.T, home, sessionID, name, threadID, turnID, input, text string) {
	t.Helper()
	spool := codexNotifySpool(home, sessionID)
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(map[string]any{"type": "agent-turn-complete", "thread-id": threadID, "turn-id": turnID, "input-messages": []string{input}, "last-assistant-message": text})
	if err := atomicWritePrivate(filepath.Join(spool, name+".json"), wire); err != nil {
		t.Fatal(err)
	}
}

func TestCodexExactTurnResultAfterTranscriptFlush(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	sessionID := filepath.Base(rt.dir)
	// Native completion from an old turn predates reservation and is ignored.
	publishCodexNotify(t, home, sessionID, "old", "thread-exact", "old-turn", request.Prompt, "old result")
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	publishCodexNotify(t, home, sessionID, "foreign-thread", "another-thread", "turn-exact", request.Prompt, "foreign result")
	publishCodexNotify(t, home, sessionID, "foreign-turn", "thread-exact", "another-turn", request.Prompt, "foreign result")
	text := strings.Repeat("untruncated native Ω result\n", 80000)
	wire, _ := json.Marshal(map[string]any{"type": "agent-turn-complete", "thread-id": "thread-exact", "turn-id": "turn-exact", "input-messages": []string{request.Prompt}, "last-assistant-message": text})
	spool := codexNotifySpool(home, sessionID)
	partial := filepath.Join(spool, "publishing")
	if err := os.WriteFile(partial, wire[:len(wire)/2], 0600); err != nil {
		t.Fatal(err)
	}
	// Only atomically published .json files are native receipts; an in-progress
	// callback and foreign identities cannot complete the run.
	run := rt.runs[request.RunID]
	run.mu.Lock()
	if run.record.Result.Terminal() {
		t.Fatal("partial/foreign notification completed run")
	}
	run.mu.Unlock()
	if err := os.WriteFile(partial, wire, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(partial, filepath.Join(spool, "completed.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.wait(context.Background(), request.RunID); err != nil {
		t.Fatal(err)
	}
	result, err := ReadRunTurnResult(rt.dir, request.RunID)
	if err != nil || result.Status != "done" || result.Text != text || result.ProviderSessionID != "thread-exact" || result.TurnID != "turn-exact" {
		t.Fatalf("native result: %s %s bytes=%d %v", result.Status, result.TurnID, len(result.Text), err)
	}
}

func TestCodexStopHookAloneNeverCompletesRun(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(rt.dir))
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000003.json"), `{"session_id":"thread-exact","turn_id":"turn-exact","hook_event_name":"Stop","last_assistant_message":"pre-continuation text"}`)
	view, err := rt.backend.Read(context.Background(), 0, 200)
	if err != nil || view.State != "completed" {
		t.Fatalf("Stop fixture: %s %v", view.State, err)
	}
	result, err := rt.get(request.RunID, true)
	if err != nil || result.Terminal() {
		t.Fatalf("Stop inferred final completion: %s %v", result.Status, err)
	}
	publishCodexNotify(t, home, filepath.Base(rt.dir), "complete", "thread-exact", "turn-exact", request.Prompt, "actual final")
	if result, err := rt.wait(context.Background(), request.RunID); err != nil || result.Status != "done" {
		t.Fatalf("native completion: %s %v", result.Status, err)
	}
}

func TestCodexRunIDReplayAndConflictingInput(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	var sends atomic.Int32
	send := rt.backend.Send
	rt.backend.Send = func(ctx context.Context, prompt string) (PromptDelivery, error) {
		sends.Add(1)
		return send(ctx, prompt)
	}
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	publishCodexNotify(t, home, filepath.Base(rt.dir), "complete", "thread-exact", "turn-exact", "human's different prompt", "human's result")
	result, err := rt.wait(context.Background(), request.RunID)
	if err != nil || result.Status != "failed" || result.FailureKind != agent.InputConflict || sends.Load() != 1 {
		t.Fatalf("conflict/replay: %s %s %v sends=%d", result.Status, result.FailureKind, err, sends.Load())
	}
}

func TestCodexRunDifferentSubmittedPromptFails(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(rt.dir))
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000003.json"), `{"session_id":"thread-exact","turn_id":"human-turn","hook_event_name":"UserPromptSubmit","prompt":"human prompt"}`)
	publishCodexNotify(t, home, filepath.Base(rt.dir), "complete", "thread-exact", "turn-exact", request.Prompt, "final")
	result, err := rt.wait(context.Background(), request.RunID)
	if err != nil || result.Status != "failed" || result.FailureKind != agent.InputConflict {
		t.Fatalf("submitted prompt conflict: %+v %v", result, err)
	}
}

func TestCodexRunArgsExactArgvAndNativePublisher(t *testing.T) {
	home := t.TempDir()
	input := []string{"resume", "thread-exact", "-m", "fixture-model"}
	args, enabled, err := CodexRunArgs(input, home, "egg-exact")
	if err != nil || !enabled {
		t.Fatalf("native args: %t %v", enabled, err)
	}
	hooked, err := CodexLifecycleArgs(input, home, "egg-exact")
	if err != nil {
		t.Fatal(err)
	}
	spool := codexNotifySpool(home, "egg-exact")
	command := "printf '%s\\n' \"$1\" | (" + lifecycleHookCommand(spool) + ")"
	want := append([]string{"--no-daemon", "-c", "notify=[\"/bin/sh\",\"-c\"," + strconv.Quote(command) + ",\"wt-codex-notify\"]"}, hooked...)
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv=%q, want %q", args, want)
	}
	// Execute only the generated callback with fixture JSON, never Codex.
	wire := `{"type":"agent-turn-complete","thread-id":"thread-exact","turn-id":"turn-exact","input-messages":["fixture"],"last-assistant-message":"complete"}`
	cmd := exec.Command("/bin/sh", "-c", command, "wt-codex-notify", wire)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	root, err := openProviderHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, err := codexRunNotifications(root, filepath.Join(".codex", "wingthing-run-notify", "egg-exact"))
	if err != nil || len(files) != 1 {
		t.Fatalf("publication: %d %v", len(files), err)
	}
	for _, data := range files {
		if strings.TrimSpace(string(data)) != wire {
			t.Fatal("native JSON changed in callback")
		}
	}
}

func TestCodexRunArgsExplicitConfigurationDisablesAdmission(t *testing.T) {
	for _, args := range [][]string{{"--disable", "hooks"}, {"-c", "hooks.Stop=[]"}, {"-c", "notify=[\"fixture-notifier\"]"}} {
		actual, enabled, err := CodexRunArgs(args, t.TempDir(), "fixture")
		if err != nil || enabled || actual[0] != "--no-daemon" {
			t.Fatalf("explicit config: %q %t %v", actual, enabled, err)
		}
	}
	if _, _, err := CodexRunArgs([]string{"--remote", "unix:///another-runtime"}, t.TempDir(), "fixture"); err == nil {
		t.Fatal("egg delegated execution to another runtime")
	}
}

func TestCodexRunFailureKeepsTurnIDAndDiscardsDiagnostics(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	spool := codexNotifySpool(home, filepath.Base(rt.dir))
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(map[string]any{"type": "agent-turn-complete", "thread-id": "thread-exact", "turn-id": "turn-exact", "input-messages": []string{request.Prompt}, "error": map[string]string{"code": "authentication_error", "message": "Invalid API key private-provider-canary"}})
	if err := atomicWritePrivate(filepath.Join(spool, "failure.json"), wire); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.wait(context.Background(), request.RunID); err != nil {
		t.Fatal(err)
	}
	result, err := ReadRunTurnResult(rt.dir, request.RunID)
	data, readErr := os.ReadFile(runTurnPath(rt.dir, request.RunID))
	if err != nil || readErr != nil || result.Status != "failed" || result.FailureKind != agent.AuthFailed || result.TurnID != "turn-exact" || strings.Contains(string(data), "private-provider-canary") {
		t.Fatalf("failure result: %s %s %s %v %v", result.Status, result.FailureKind, result.TurnID, err, readErr)
	}
}
