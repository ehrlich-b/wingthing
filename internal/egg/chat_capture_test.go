package egg

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureSessionHistory_Claude(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	// Create Claude project dir with encoded CWD
	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a fake JSONL file
	sessionContent := `{"type":"human","text":"hello"}` + "\n" + `{"type":"assistant","text":"hi"}` + "\n"
	sessionFile := filepath.Join(projectDir, "abc123.jsonl")
	if err := os.WriteFile(sessionFile, []byte(sessionContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set modtime to after startedAfter
	startedAfter := time.Now().Add(-1 * time.Minute)
	if err := os.Chtimes(sessionFile, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("CaptureSessionHistory: %v", err)
	}

	// Verify chat.jsonl.gz exists and decompresses correctly
	gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open chat.jsonl.gz: %v", err)
	}
	defer func() { _ = f.Close() }()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	content, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if err := gr.Close(); err != nil {
		t.Fatal(err)
	}

	if string(content) != sessionContent {
		t.Errorf("content mismatch: got %q, want %q", string(content), sessionContent)
	}

	// Verify chat.meta
	metaData, err := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
	if err != nil {
		t.Fatalf("read chat.meta: %v", err)
	}
	meta := ParseChatMeta(string(metaData))
	if meta["agent_session_id"] != "abc123" {
		t.Errorf("agent_session_id = %q, want %q", meta["agent_session_id"], "abc123")
	}
	if meta["agent"] != "claude" {
		t.Errorf("agent = %q, want %q", meta["agent"], "claude")
	}
	if meta["format"] != "jsonl" {
		t.Errorf("format = %q, want %q", meta["format"], "jsonl")
	}
	for _, path := range []string{gzPath, filepath.Join(eggDir, "chat.meta")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want private regular file", filepath.Base(path), info.Mode())
		}
	}
}

func TestCaptureSessionHistory_ReplacesMetadataSymlink(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"
	projectDir := filepath.Join(home, ".claude", "projects", encodeCWDForClaude(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "abc123.jsonl"), []byte("chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(eggDir, "chat.meta")
	if err := os.Symlink(target, metaPath); err != nil {
		t.Fatal(err)
	}

	if err := CaptureSessionHistory("claude", cwd, eggDir, home, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTarget) != "original" {
		t.Fatalf("symlink target changed: %q", gotTarget)
	}
	info, err := os.Lstat(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode = %v, want private regular file", info.Mode())
	}
}

func TestCaptureSessionHistory_Claude_NoMatch(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write file with old timestamp
	sessionFile := filepath.Join(projectDir, "old.jsonl")
	if err := os.WriteFile(sessionFile, []byte("old data"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(sessionFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Start time is after the file
	startedAfter := time.Now().Add(-30 * time.Minute)

	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT create chat.jsonl.gz
	if _, err := os.Stat(filepath.Join(eggDir, "chat.jsonl.gz")); err == nil {
		t.Error("chat.jsonl.gz should not exist for old files")
	}
}

func TestCaptureSessionHistory_Claude_MultipleFiles(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	startedAfter := time.Now().Add(-1 * time.Minute)

	// Write older file
	older := filepath.Join(projectDir, "older.jsonl")
	if err := os.WriteFile(older, []byte("older content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(older, time.Now().Add(-30*time.Second), time.Now().Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Write newer file
	newer := filepath.Join(projectDir, "newer.jsonl")
	if err := os.WriteFile(newer, []byte("newer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("CaptureSessionHistory: %v", err)
	}

	// Should pick the newest file
	meta, err := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
	if err != nil {
		t.Fatal(err)
	}
	m := ParseChatMeta(string(meta))
	if m["agent_session_id"] != "newer" {
		t.Errorf("picked %q, want newer", m["agent_session_id"])
	}
}

func TestCaptureSessionHistory_Claude_ExactIDDoesNotSelectNewerConversation(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/shared-project"
	projectDir := filepath.Join(home, ".claude", "projects", encodeCWDForClaude(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "ours.jsonl"), []byte("ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "other.jsonl"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Join(projectDir, "ours.jsonl"), now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(projectDir, "other.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(eggDir, "egg.meta"), "agent=claude\nprovider_session_id=ours\nprovider_home="+home+"\n")
	if err := CaptureSessionHistory("claude", cwd, eggDir, home, now.Add(time.Minute), "ours"); err != nil {
		t.Fatal(err)
	}
	metaData, err := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseChatMeta(string(metaData))["agent_session_id"]; got != "ours" {
		t.Fatalf("captured provider ID = %q, want ours", got)
	}
}

func TestCaptureSessionHistory_Claude_ExplicitUnverifiedIDDoesNotGuess(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/workspace/project"
	projectDir := filepath.Join(home, ".claude", "projects", encodeCWDForClaude(cwd))
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "someone-else.jsonl"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CaptureSessionHistory("claude", cwd, eggDir, home, time.Time{}, ""); err == nil {
		t.Fatal("unverified provider capture guessed a transcript")
	}
	if _, err := os.Stat(filepath.Join(eggDir, "chat.jsonl.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified capture wrote history: %v", err)
	}
}

func TestCaptureSessionHistory_UnknownAgent(t *testing.T) {
	eggDir := t.TempDir()
	err := CaptureSessionHistory("ollama", "/tmp", eggDir, "/home/test", time.Now())
	if err != nil {
		t.Fatalf("expected nil error for unknown agent, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(eggDir, "chat.jsonl.gz")); err == nil {
		t.Error("should not create chat files for agents without SessionDir")
	}
}

func TestCaptureSessionHistory_MissingDir(t *testing.T) {
	eggDir := t.TempDir()
	err := CaptureSessionHistory("claude", "/nonexistent/path", eggDir, "/nonexistent/home", time.Now())
	if err != nil {
		t.Fatalf("expected nil for missing dir, got: %v", err)
	}
}

func TestCaptureSessionHistory_AtomicWrite(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(projectDir, "test.jsonl")
	if err := os.WriteFile(sessionFile, []byte("test data"), 0o644); err != nil {
		t.Fatal(err)
	}

	startedAfter := time.Now().Add(-1 * time.Minute)
	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("CaptureSessionHistory: %v", err)
	}

	// Verify no .tmp files remain
	entries, err := os.ReadDir(eggDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestTranscriptRefusesProviderDirectorySymlinks(t *testing.T) {
	for _, component := range []string{".claude", "projects", "project"} {
		t.Run(component, func(t *testing.T) {
			home, victim, eggDir := t.TempDir(), t.TempDir(), t.TempDir()
			cwd := "/shared/work"
			relative := filepath.Join(".claude", "projects", encodeCWDForClaude(cwd))
			link := filepath.Join(home, relative)
			suffix := ""
			if component == ".claude" {
				link = filepath.Join(home, ".claude")
				suffix = filepath.Join("projects", encodeCWDForClaude(cwd))
			}
			if component == "projects" {
				link = filepath.Join(home, ".claude", "projects")
				suffix = encodeCWDForClaude(cwd)
			}
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(victim, suffix), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(victim, suffix, "victim-id.jsonl"), []byte("{\"type\":\"assistant\",\"sessionId\":\"victim-id\",\"message\":{\"role\":\"assistant\",\"content\":\"victim-secret\"}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(eggDir, "egg.meta"), []byte("agent=claude\nprovider_session_id=victim-id\nprovider_home="+home+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if path, _, err := findClaudeSession(cwd, home, Profile("claude").SessionDir, time.Time{}, "victim-id"); err == nil && path != "" {
				t.Errorf("discovery followed %s symlink: %s", component, path)
			}
			if err := CaptureSessionHistory("claude", cwd, eggDir, home, time.Time{}, "victim-id"); err == nil {
				t.Error("capture accepted directory symlink")
			}
			if _, err := os.Stat(filepath.Join(eggDir, "chat.jsonl.gz")); !os.IsNotExist(err) {
				t.Errorf("victim transcript captured: %v", err)
			}
			if view, err := ReadSessionLifecycle(eggDir, "claude", cwd, home, "victim-id", true, 0, 10); err == nil {
				t.Errorf("lifecycle imported victim through symlink: %#v", view.Events)
			}
		})
	}
}

func TestTranscriptRejectsProviderIDFromAnotherEgg(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	lifecycleWrite(t, path, "{\"type\":\"assistant\",\"sessionId\":\"ours\",\"message\":{\"content\":\"other-egg-secret\"}}\n")
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "agent=claude\nprovider_session_id=actual-owner\nprovider_home="+home+"\n")
	if view, err := ReadSessionLifecycle(dir, "claude", cwd, home, "ours", true, 0, 10); err == nil {
		t.Errorf("wrong provider ID imported: %#v", view.Events)
	}
	if err := CaptureSessionHistory("claude", cwd, dir, home, time.Time{}, "ours"); err == nil {
		t.Error("wrong provider ID captured")
	}
}
