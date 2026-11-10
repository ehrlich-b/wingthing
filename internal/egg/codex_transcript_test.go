package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTranscriptTurnIdentitySurvivesResumedImportAndReopen(t *testing.T) {
	for _, context := range []string{
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"old"}}`,
		`{"type":"turn_context","payload":{"turn_id":"old"}}`,
	} {
		t.Run(context, func(t *testing.T) {
			dir, home, cwd, sessions := transcriptFixture(t, "codex", "ours")
			spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(dir))
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			lifecycleWrite(t, filepath.Join(spool, "prompt.json"), `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"repeat","turn_id":"new"}`)
			path := filepath.Join(sessions, "rollout-date-ours.jsonl")
			prefix := `{"type":"session_meta","payload":{"id":"ours"}}` + "\n" + context + "\n"
			user := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"repeat"}]}}` + "\n"
			lifecycleWrite(t, path, prefix+user)
			read := func(after int64) SessionView {
				t.Helper()
				view, err := ReadSessionLifecycle(dir, "codex", cwd, home, "ours", true, after, 200)
				if err != nil {
					t.Fatal(err)
				}
				return view
			}
			first := read(0)
			if len(first.Events) != 4 || first.Events[0].Source != "codex_hook" || first.Events[0].TurnID != "new" || first.Events[3].TurnID != "old" {
				t.Fatalf("hook and historical native turn lost identity: %+v", first.Events)
			}
			// Import resumes from the journal's byte offset after reopening. The
			// hook's newer ID must never become the historical transcript context.
			answer := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old reply"}]}}` + "\n"
			lifecycleWrite(t, path, prefix+user+answer)
			second := read(first.Cursor)
			if len(second.Events) != 1 || second.Events[0].TurnID != "old" {
				t.Fatalf("reopened importer lost historical context: %+v", second.Events)
			}
			newContext := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"new"}}` + "\n"
			lifecycleWrite(t, path, prefix+user+answer+newContext+user)
			third := read(second.Cursor)
			if len(third.Events) != 2 || third.Events[1].TurnID != "new" || third.Events[1].Text != "repeat" {
				t.Fatalf("new native prompt lost hook identity: %+v", third.Events)
			}
			if replay := read(third.Cursor); len(replay.Events) != 0 {
				t.Fatalf("replayed imported turns: %+v", replay.Events)
			}
		})
	}
}

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
