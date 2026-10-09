package egg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRunScannersReadEachHookOnce(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir, home, cwd, transcript := lifecycleFixture(t)
			lifecycleWrite(t, transcript, "")
			sessionID := filepath.Base(dir)
			var events []SessionEvent
			read := func(_ context.Context, after int64, limit int) (SessionView, error) {
				view := SessionView{SessionID: sessionID, ProviderSessionID: "ours", HeadCursor: int64(len(events)), Cursor: after}
				for _, event := range events {
					if event.Sequence <= after {
						continue
					}
					if len(view.Events) == limit {
						view.HasMore = true
						break
					}
					view.Events = append(view.Events, event)
					view.Cursor = event.Sequence
				}
				return view, nil
			}
			reads := make(map[string]int)
			readFile := func(root *os.File, path string) ([]byte, error) {
				reads[path]++
				return readRunFile(root, path)
			}
			var scan func() (turnEvidence, error)
			var err error
			if provider == "claude" {
				scan, err = claudeRunScanner(home, cwd, "ours", "fixture prompt", read, readFile)
			} else {
				scan, err = codexRunScanner(home, sessionID, "ours", "fixture prompt", read, readFile)
			}
			if err != nil {
				t.Fatal(err)
			}
			spool := filepath.Join(home, "."+provider, "wingthing-events", sessionID)
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			// Cross multiple lifecycle pages, then append more hooks between ticks.
			for tick := range 100 {
				count := 0
				if tick == 0 {
					count = 450
				} else if tick == 50 {
					count = 50
				}
				for range count {
					sequence := int64(len(events) + 1)
					name := fmt.Sprintf("seq.%020d.json", sequence)
					lifecycleWrite(t, filepath.Join(spool, name), `{"session_id":"ours","hook_event_name":"PreToolUse"}`)
					events = append(events, SessionEvent{Sequence: sequence, Source: provider + "_hook", SourceKey: "hook:" + name})
				}
				if _, err := scan(); err != nil {
					t.Fatal(err)
				}
			}
			if len(reads) != 500 {
				t.Fatalf("read %d hooks, want 500", len(reads))
			}
			for path, count := range reads {
				if count != 1 {
					t.Fatalf("hook %s read %d times across 100 ticks, want once", path, count)
				}
			}
		})
	}
}

func TestCodexRunScannerReadsEachNotificationOnce(t *testing.T) {
	home := t.TempDir()
	const sessionID = "egg-exact"
	reads := make(map[string]int)
	readFile := func(root *os.File, path string) ([]byte, error) {
		reads[path]++
		return readRunFile(root, path)
	}
	read := func(context.Context, int64, int) (SessionView, error) {
		return SessionView{SessionID: sessionID, ProviderSessionID: "thread-exact"}, nil
	}
	scan, err := codexRunScanner(home, sessionID, "thread-exact", "fixture", read, readFile)
	if err != nil {
		t.Fatal(err)
	}
	for tick := range 100 {
		if tick%10 == 0 {
			publishCodexNotify(t, home, sessionID, fmt.Sprintf("notification-%d", tick), "another-thread", "foreign-turn", "fixture", "foreign result")
		}
		if _, err := scan(); err != nil {
			t.Fatal(err)
		}
	}
	if len(reads) != 10 {
		t.Fatalf("read %d notifications, want 10", len(reads))
	}
	for path, count := range reads {
		if count != 1 {
			t.Fatalf("notification %s read %d times across 100 ticks, want once", path, count)
		}
	}
}

func TestClaudeRunScannerRetainsStopAcrossTicks(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			dir, path, options := promptFixtureOptions(t)
			home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
			scan, err := claudeRunScanner(home, "/fixture/shared-workspace", "ours", options.Input, options.Read, readRunFile)
			if err != nil {
				t.Fatal(err)
			}
			writeNativeUserPrompt(t, path, options.Input)
			stop := map[string]any{"session_id": "ours", "hook_event_name": "Stop", "last_assistant_message": "final"}
			if background {
				stop["background_tasks"] = []string{"task-exact"}
				appendNative(t, path, assistantRecord("final", "end_turn"))
			}
			wire, _ := json.Marshal(stop)
			lifecycleHook(t, home, filepath.Base(dir), "stop-1", string(wire))
			for range 3 {
				if evidence, err := scan(); err != nil || !evidence.Receipt || evidence.Complete {
					t.Fatalf("Stop completed before flush/background completion: %+v %v", evidence, err)
				}
			}
			if background {
				delete(stop, "background_tasks")
				wire, _ = json.Marshal(stop)
				lifecycleHook(t, home, filepath.Base(dir), "stop-2", string(wire))
			} else {
				// No end_turn: only the previously read Stop can complete this flush.
				appendNative(t, path, assistantRecord("final", ""))
			}
			if evidence, err := scan(); err != nil || !evidence.Complete || evidence.Text != "final" {
				t.Fatalf("lost Stop evidence between ticks: %+v %v", evidence, err)
			}
		})
	}
}

func TestClaudeRunScannerRejectsTruncatedTranscript(t *testing.T) {
	_, path, options := promptFixtureOptions(t)
	home := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	writeNativeUserPrompt(t, path, "old prompt")
	scan, err := claudeRunScanner(home, "/fixture/shared-workspace", "ours", options.Input, options.Read, readRunFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := scan(); err == nil {
		t.Fatal("seeking past a truncated transcript hid lost evidence")
	}
}
