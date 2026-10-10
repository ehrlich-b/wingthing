package egg

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestProviderFailureContentAbsentFromLifecycleAndArchive(t *testing.T) {
	const canary = "private-provider-diagnostic-canary"
	t.Run("codex_app_server", func(t *testing.T) {
		dir := t.TempDir()
		wire := codexFixture(t, "turn/completed", map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"id": "failed-turn", "status": "failed", "error": map[string]string{"code": "authentication_error", "message": "Invalid API key " + canary}}}, nil)
		event, accepted, err := RecordCodexNativeEvent(dir, codexFixtureThread, wire)
		if err != nil || !accepted || event.Event.Reason != string(agent.AuthFailed) || len(event.Event.Raw) != 0 {
			t.Fatalf("native failed turn retained diagnostics: %+v, %v", event, err)
		}
		journal, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
		if err != nil || strings.Contains(string(journal), canary) {
			t.Fatalf("native failure journal: %s, %v", journal, err)
		}
	})
	t.Run("claude", func(t *testing.T) {
		f := newRunFixture(t)
		if _, err := f.runtime.submit(f.request); err != nil {
			t.Fatal(err)
		}
		<-f.sent
		appendNative(t, f.path, []byte(`{"type":"assistant","sessionId":"ours","error":"authentication_failed","message":{"role":"assistant","content":[{"type":"text","text":"Invalid API key `+canary+`"}]}}`+"\n"))
		result := waitRunFixture(t, f)
		if result.FailureKind != agent.AuthFailed {
			t.Fatal(result)
		}
		lifecycleHook(t, f.home, filepath.Base(f.runtime.dir), "failure", `{"session_id":"ours","hook_event_name":"StopFailure","error":"Invalid API key `+canary+`"}`)
		if _, err := ReadSessionLifecycle(f.runtime.dir, "claude", "/fixture/shared-workspace", f.home, "ours", true, 0, 200); err != nil {
			t.Fatal(err)
		}
		if err := CaptureSessionHistory("claude", "/fixture/shared-workspace", f.runtime.dir, f.home, time.Time{}); err != nil {
			t.Fatal(err)
		}
		assertFailureArchive(t, f.runtime.dir, f.path, canary)
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		// Replay from the redacted archive keeps the original native byte offsets.
		if _, err := ReadSessionLifecycle(f.runtime.dir, "claude", "/fixture/shared-workspace", f.home, "ours", false, 0, 200); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("codex", func(t *testing.T) {
		rt, home, request, sent := codexRunFixture(t)
		if _, err := rt.submit(request); err != nil {
			t.Fatal(err)
		}
		<-sent
		spool := codexNotifySpool(home, filepath.Base(rt.dir))
		if err := os.MkdirAll(spool, 0700); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(map[string]any{"type": "agent-turn-complete", "thread-id": "thread-exact", "turn-id": "turn-exact", "input-messages": []string{request.Prompt}, "error": map[string]string{"code": "authentication_error", "message": "Invalid API key " + canary}})
		if err := atomicWritePrivate(filepath.Join(spool, "failure.json"), wire); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.wait(context.Background(), request.RunID); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, ".codex", "sessions", "rollout-thread-exact.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		content := `{"type":"session_meta","payload":{"id":"thread-exact","cwd":"/fixture"}}` + "\n" + `{"type":"event_msg","payload":{"type":"error","message":"Invalid API key ` + canary + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := CaptureSessionHistory("codex", "/fixture", rt.dir, home, time.Time{}); err != nil {
			t.Fatal(err)
		}
		assertFailureArchive(t, rt.dir, path, canary)
	})
}

func assertFailureArchive(t *testing.T, dir, source, canary string) {
	t.Helper()
	for _, name := range []string{"lifecycle.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), canary) {
			t.Fatalf("diagnostic escaped into %s", name)
		}
	}
	f, err := os.Open(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	archive, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(archive), canary) || !strings.Contains(string(archive), "auth_failed") || len(archive) != len(original) {
		t.Fatalf("failure archive lost redaction, kind, or replay offsets: archive bytes=%d source bytes=%d", len(archive), len(original))
	}
}
