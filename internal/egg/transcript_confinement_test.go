package egg

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transcriptFixture(t *testing.T, agent, id string) (eggDir, home, cwd, sessionDir string) {
	t.Helper()
	eggDir, home, cwd = t.TempDir(), t.TempDir(), "/fixture/shared-workspace"
	sessionDir = filepath.Join(home, Profile(agent).SessionDir)
	if agent == "claude" {
		sessionDir = filepath.Join(sessionDir, encodeCWDForClaude(cwd))
	}
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(eggDir, "egg.meta"), "agent="+agent+"\nprovider_session_id="+id+"\nprovider_home="+home+"\ncwd="+cwd+"\n")
	return
}

func readTranscriptForTest(eggDir, home, cwd, agent, id, operation string) error {
	if operation == "capture" {
		return CaptureSessionHistory(agent, cwd, eggDir, home, time.Time{}, id)
	}
	_, err := ReadSessionLifecycle(eggDir, agent, cwd, home, id, true, 0, 10)
	return err
}

func assertNoImportedTranscript(t *testing.T, eggDir string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(eggDir, "chat.jsonl.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused transcript was captured: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(eggDir, "lifecycle.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "victim-secret") {
		t.Fatalf("victim content was imported: %s", data)
	}
}

func TestProviderTranscriptRefusesDirectorySymlinks(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode"} {
		parts := strings.Split(Profile(agent).SessionDir, string(filepath.Separator))
		if agent == "claude" {
			parts = append(parts, encodeCWDForClaude("/fixture/shared-workspace"))
		}
		for _, operation := range []string{"capture", "lifecycle"} {
			if operation == "lifecycle" && agent != "claude" {
				continue // Main imports Codex hooks, not rollout transcripts.
			}
			for i, component := range parts {
				t.Run(agent+"/"+operation+"/"+component, func(t *testing.T) {
					eggDir, home, cwd, _ := transcriptFixture(t, agent, "victim")
					victim := t.TempDir()
					victimDir := filepath.Join(append([]string{victim}, parts[i+1:]...)...)
					if err := os.MkdirAll(victimDir, 0700); err != nil {
						t.Fatal(err)
					}
					lifecycleWrite(t, filepath.Join(victimDir, "victim.jsonl"), `{"type":"user","sessionId":"victim","message":{"role":"user","content":"victim-secret"}}`+"\n")
					link := filepath.Join(append([]string{home}, parts[:i+1]...)...)
					if err := os.RemoveAll(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(victim, link); err != nil {
						t.Fatal(err)
					}
					if err := readTranscriptForTest(eggDir, home, cwd, agent, "victim", operation); err == nil {
						t.Error("symlinked provider session directory was accepted")
					}
					assertNoImportedTranscript(t, eggDir)
				})
			}
		}
	}
}

func TestProviderTranscriptRefusesMismatchedSessionID(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode"} {
		for _, operation := range []string{"capture", "lifecycle"} {
			if operation == "lifecycle" && agent == "opencode" {
				continue
			}
			t.Run(agent+"/"+operation, func(t *testing.T) {
				eggDir, home, cwd, sessionDir := transcriptFixture(t, agent, "ours")
				lifecycleWrite(t, filepath.Join(sessionDir, "victim.jsonl"), `{"type":"user","sessionId":"victim","message":{"role":"user","content":"victim-secret"}}`+"\n")
				if agent == "codex" && operation == "lifecycle" {
					spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(eggDir))
					if err := os.MkdirAll(spool, 0700); err != nil {
						t.Fatal(err)
					}
					lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000001.json"), `{"session_id":"victim","hook_event_name":"UserPromptSubmit","prompt":"victim-secret"}`)
				}
				if err := readTranscriptForTest(eggDir, home, cwd, agent, "victim", operation); err == nil {
					t.Error("provider ID differing from egg.meta was accepted")
				}
				assertNoImportedTranscript(t, eggDir)
			})
		}
	}
}

func TestProviderTranscriptImportsRegularOwnSession(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode"} {
		for _, operation := range []string{"capture", "lifecycle"} {
			if operation == "lifecycle" && agent != "claude" {
				continue
			}
			t.Run(agent+"/"+operation, func(t *testing.T) {
				eggDir, home, cwd, sessionDir := transcriptFixture(t, agent, "ours")
				content := `{"type":"user","sessionId":"ours","message":{"role":"user","content":"own-message"}}` + "\n"
				lifecycleWrite(t, filepath.Join(sessionDir, "ours.jsonl"), content)
				if err := readTranscriptForTest(eggDir, home, cwd, agent, "ours", operation); err != nil {
					t.Fatal(err)
				}
				if operation == "lifecycle" {
					data, err := os.ReadFile(filepath.Join(eggDir, "lifecycle.jsonl"))
					if err != nil || !strings.Contains(string(data), "own-message") {
						t.Fatalf("own transcript not imported: %s %v", data, err)
					}
					return
				}
				file, err := os.Open(filepath.Join(eggDir, "chat.jsonl.gz"))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				reader, err := gzip.NewReader(file)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				data, err := io.ReadAll(reader)
				if err != nil || string(data) != content {
					t.Fatalf("own transcript not captured: %s %v", data, err)
				}
			})
		}
	}
}

func TestProviderHooksRefuseDirectorySymlinks(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			eggDir, home, cwd, _ := transcriptFixture(t, agent, "ours")
			spool := filepath.Join(home, "."+agent, "wingthing-events", filepath.Base(eggDir))
			if err := os.MkdirAll(filepath.Dir(spool), 0700); err != nil {
				t.Fatal(err)
			}
			victim := t.TempDir()
			lifecycleWrite(t, filepath.Join(victim, "seq.00000000000000000001.json"), `{"session_id":"ours","hook_event_name":"UserPromptSubmit","prompt":"victim-secret"}`)
			if err := os.Symlink(victim, spool); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadSessionLifecycle(eggDir, agent, cwd, home, "ours", true, 0, 10); err == nil {
				t.Error("symlinked native hook directory was accepted")
			}
			assertNoImportedTranscript(t, eggDir)
		})
	}
}

func TestProviderResumeRefusesMismatchedSessionID(t *testing.T) {
	eggDir, home, cwd, _ := transcriptFixture(t, "claude", "ours")
	writeRestoreFixture(t, eggDir, "agent=claude\nagent_session_id=victim\n", "victim-secret\n")
	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err == nil {
		t.Fatal("captured provider ID differing from egg.meta was restored")
	}
}

func TestProviderResumeRefusesDirectorySymlinks(t *testing.T) {
	for _, component := range []string{"home", "project"} {
		t.Run(component, func(t *testing.T) {
			eggDir, home, cwd, sessionDir := transcriptFixture(t, "claude", "ours")
			writeRestoreFixture(t, eggDir, "agent=claude\nagent_session_id=ours\n", "own-message\n")
			if component == "home" {
				link := filepath.Join(t.TempDir(), "provider-home")
				if err := os.Symlink(home, link); err != nil {
					t.Fatal(err)
				}
				home = link
			} else {
				// os.Root alone permits symlinks to other directories inside home.
				other := filepath.Join(home, "other-project")
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(sessionDir); err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(filepath.Dir(sessionDir), other)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(relative, sessionDir); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err == nil {
				t.Fatal("symlinked provider restore directory was accepted")
			}
		})
	}
}

func TestProviderTranscriptRefusesUnrecordedSessionID(t *testing.T) {
	for _, operation := range []string{"capture", "lifecycle"} {
		t.Run(operation, func(t *testing.T) {
			eggDir, home, cwd, sessionDir := transcriptFixture(t, "claude", "victim")
			if err := os.Remove(filepath.Join(eggDir, "egg.meta")); err != nil {
				t.Fatal(err)
			}
			lifecycleWrite(t, filepath.Join(sessionDir, "victim.jsonl"), `{"type":"user","sessionId":"victim","message":{"role":"user","content":"victim-secret"}}`+"\n")
			if err := readTranscriptForTest(eggDir, home, cwd, "claude", "victim", operation); err == nil {
				t.Error("unrecorded provider ID was accepted")
			}
			assertNoImportedTranscript(t, eggDir)
		})
	}
}

func TestProviderCodexCaptureUsesOwnRecordedThread(t *testing.T) {
	eggDir, home, cwd, sessionDir := transcriptFixture(t, "codex", "")
	spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(eggDir))
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000001.json"), `{"session_id":"ours","hook_event_name":"SessionStart"}`)
	lifecycleWrite(t, filepath.Join(sessionDir, "rollout-2026-10-07-ours.jsonl"), "own-message\n")
	lifecycleWrite(t, filepath.Join(sessionDir, "rollout-2026-10-07-victim.jsonl"), "victim-secret\n")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(sessionDir, "rollout-2026-10-07-victim.jsonl"), future, future); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "ours"} {
		if err := CaptureSessionHistory("codex", cwd, eggDir, home, time.Time{}, id); err != nil {
			t.Fatal(err)
		}
		meta, err := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
		if err != nil || ParseChatMeta(string(meta))["agent_session_id"] != "ours" {
			t.Fatalf("capture did not use this egg's recorded thread: %s %v", meta, err)
		}
		file, err := os.Open(filepath.Join(eggDir, "chat.jsonl.gz"))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		file.Close()
		if err != nil || string(data) != "own-message\n" {
			t.Fatalf("another Codex thread was captured: %s %v", data, err)
		}
	}
}

func TestProviderTranscriptImportsCapturedOwnSession(t *testing.T) {
	eggDir, home, cwd, _ := transcriptFixture(t, "claude", "ours")
	writeRestoreFixture(t, eggDir, "agent=claude\nagent_session_id=ours\n", `{"type":"user","sessionId":"ours","message":{"role":"user","content":"own-message"}}`+"\n")
	view, err := ReadSessionLifecycle(eggDir, "claude", cwd, home, "ours", false, 0, 10)
	if err != nil || len(view.Events) != 1 || view.Events[0].Text != "own-message" {
		t.Fatalf("captured own transcript did not replay: %+v %v", view, err)
	}
}
