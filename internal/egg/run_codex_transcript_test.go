package egg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestCodexNativeCompletionRequiresExactFlushedTurnAndStop(t *testing.T) {
	rt, home, request, _ := initialCodexFixture(t)
	session := filepath.Base(rt.dir)
	spool := filepath.Join(home, ".codex", "wingthing-events", session)
	transcript := filepath.Join(home, ".codex", "sessions", "rollout-fixture-thread-exact.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, transcript, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread-exact\"}}\n")
	scan, err := codexRunScanner(home, session, "", request.Prompt, rt.backend.Read, readRunFile)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(name, event, turn, text string) {
		wire, _ := json.Marshal(map[string]any{"session_id": "thread-exact", "turn_id": turn, "hook_event_name": event, "prompt": request.Prompt, "transcript_path": transcript, "last_assistant_message": text})
		lifecycleWrite(t, filepath.Join(spool, name), string(wire))
	}
	hook("seq.00000000000000000001.json", "SessionStart", "", "")
	hook("seq.00000000000000000002.json", "UserPromptSubmit", "turn-exact", "")
	text := strings.Repeat("native Ω🙂 result\n", 100000)
	hook("seq.00000000000000000003.json", "Stop", "turn-exact", text)
	if evidence, err := scan(); err != nil || !evidence.Receipt || evidence.Complete {
		t.Fatalf("Stop alone completed native run: %+v %v", evidence, err)
	}
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	appendRecord := func(turn, last string) []byte {
		wire, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": last}})
		return append(wire, '\n')
	}
	if _, err := f.Write(appendRecord("foreign-turn", text)); err != nil {
		t.Fatal(err)
	}
	if evidence, err := scan(); err != nil || evidence.Complete {
		t.Fatalf("foreign turn completed native run: %v", err)
	}
	wire := appendRecord("turn-exact", text)
	if _, err := f.Write(wire[:len(wire)/2]); err != nil {
		t.Fatal(err)
	}
	if evidence, err := scan(); err != nil || evidence.Complete {
		t.Fatalf("partial record completed native run: %v", err)
	}
	if _, err := f.Write(wire[len(wire)/2:]); err != nil {
		t.Fatal(err)
	}
	evidence, err := scan()
	if err != nil || !evidence.Complete || evidence.Text != text {
		t.Fatalf("exact native completion lost full text: complete=%v bytes=%d %v", evidence.Complete, len(evidence.Text), err)
	}
	if _, err := rt.backend.Read(context.Background(), 0, 200); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCompletionTranscriptRefusesForeignAndAliasedPaths(t *testing.T) {
	home := t.TempDir()
	valid := filepath.Join(home, ".codex", "sessions", "2026", "10", "10", "rollout-fixture-thread-exact.jsonl")
	for _, path := range []string{filepath.Join(home, "outside-thread-exact.jsonl"), filepath.Join(home, ".codex", "sessions", "rollout-fixture-foreign.jsonl"), "relative-thread-exact.jsonl"} {
		if _, err := codexBoundTranscriptPath(home, "thread-exact", path); err == nil {
			t.Fatalf("accepted foreign transcript: %q", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(valid), 0700); err != nil {
		t.Fatal(err)
	}
	rel, err := codexBoundTranscriptPath(home, "thread-exact", valid)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, valid, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"foreign-thread\"}}\n")
	var offset int64
	var bound bool
	if _, _, err := readCodexTurnCompletion(home, rel, "thread-exact", "turn-exact", &offset, &bound); err == nil {
		t.Fatal("accepted foreign transcript metadata")
	}
	if err := os.Remove(valid); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "foreign")
	lifecycleWrite(t, target, "{}\n")
	if err := os.Symlink(target, valid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCodexTurnCompletion(home, rel, "thread-exact", "turn-exact", &offset, &bound); err == nil {
		t.Fatal("followed a transcript symlink")
	}
}

func TestCodexNativeTaskFailureIsClassifiedWithoutStopOrDiagnosticLeak(t *testing.T) {
	rt, home, request, sent := codexRunFixture(t)
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	<-sent
	transcript := filepath.Join(home, ".codex", "sessions", "rollout-fixture-thread-exact.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0700); err != nil {
		t.Fatal(err)
	}
	errorRecord := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-exact","last_agent_message":null,"error":{"message":"unexpected status 401 Unauthorized: Invalid API key private-failure-sentinel","codex_error_info":{"http_connection_failed":{"http_status_code":401}}}}}`
	lifecycleWrite(t, transcript, "{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread-exact\"}}\n"+errorRecord+"\n")
	wire, _ := json.Marshal(map[string]string{"session_id": "thread-exact", "turn_id": "turn-exact", "hook_event_name": "PostToolUse", "transcript_path": transcript})
	lifecycleWrite(t, filepath.Join(home, ".codex", "wingthing-events", filepath.Base(rt.dir), "seq.00000000000000000003.json"), string(wire))
	result, err := rt.wait(context.Background(), request.RunID)
	if err != nil || result.Status != "failed" || result.FailureKind != agent.AuthFailed || result.TurnID != "turn-exact" || result.Text != "" {
		t.Fatalf("native task error: %+v %v", result, err)
	}
	data, err := os.ReadFile(runTurnPath(rt.dir, request.RunID))
	if err != nil || strings.Contains(string(data), "private-failure-sentinel") {
		t.Fatal("native diagnostic leaked into durable result")
	}
}
