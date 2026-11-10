package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTranscriptExactThreadReplayAndPartialAppend(t *testing.T) {
	dir, home, cwd, sessions := transcriptFixture(t, "codex", "ours")
	dated := filepath.Join(sessions, "2026", "11", "09")
	if err := os.MkdirAll(dated, 0700); err != nil {
		t.Fatal(err)
	}
	meta := `{"type":"session_meta","payload":{"id":"ours"}}` + "\n"
	message := func(role, text string) string {
		data, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": "input_text", "text": text}}}})
		return string(data) + "\n"
	}
	path := filepath.Join(dated, "rollout-date-ours.jsonl")
	lifecycleWrite(t, path, meta+message("user", "direct terminal")+`{"type":"response_item"`)
	lifecycleWrite(t, filepath.Join(dated, "rollout-date-victim.jsonl"), `{"type":"session_meta","payload":{"id":"victim"}}`+"\n"+message("assistant", "victim-secret"))
	read := func(after int64, limit int) SessionView {
		t.Helper()
		v, err := ReadSessionLifecycle(dir, "codex", cwd, home, "ours", true, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := read(0, 200)
	if len(first.Events) != 2 || first.Events[1].Text != "direct terminal" || first.SourceComplete {
		t.Fatalf("partial import: %+v", first)
	}
	lifecycleWrite(t, path, meta+message("user", "direct terminal")+message("assistant", "native response"))
	next := read(first.Cursor, 1)
	if len(next.Events) != 1 || next.Events[0].Role != "assistant" || next.Events[0].Text != "native response" || !next.SourceComplete {
		t.Fatalf("append: %+v", next)
	}
	if replay := read(next.Cursor, 200); len(replay.Events) != 0 {
		t.Fatalf("duplicate replay: %+v", replay)
	}
	if next.Ready {
		t.Fatal("transcript granted input readiness without hooks")
	}
}

func TestCodexTranscriptRejectsForeignMetadataAndDateSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		dir, home, cwd, sessions := transcriptFixture(t, "codex", "ours")
		if symlink {
			if err := os.Symlink(t.TempDir(), filepath.Join(sessions, "2026")); err != nil {
				t.Fatal(err)
			}
		} else {
			lifecycleWrite(t, filepath.Join(sessions, "rollout-date-ours.jsonl"), `{"type":"session_meta","payload":{"id":"victim"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"victim-secret"}]}}`+"\n")
		}
		if _, err := ReadSessionLifecycle(dir, "codex", cwd, home, "ours", true, 0, 200); err == nil {
			t.Fatal("foreign/symlink transcript accepted")
		}
		assertNoImportedTranscript(t, dir)
	}
}
