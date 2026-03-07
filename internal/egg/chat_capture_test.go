package egg

import (
	"compress/gzip"
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
	os.MkdirAll(projectDir, 0755)

	// Write a fake JSONL file
	sessionContent := `{"type":"human","text":"hello"}` + "\n" + `{"type":"assistant","text":"hi"}` + "\n"
	sessionFile := filepath.Join(projectDir, "abc123.jsonl")
	os.WriteFile(sessionFile, []byte(sessionContent), 0644)

	// Set modtime to after startedAfter
	startedAfter := time.Now().Add(-1 * time.Minute)
	os.Chtimes(sessionFile, time.Now(), time.Now())

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
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	content, _ := io.ReadAll(gr)
	gr.Close()

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
}

func TestCaptureSessionHistory_Claude_NoMatch(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)
	os.MkdirAll(projectDir, 0755)

	// Write file with old timestamp
	sessionFile := filepath.Join(projectDir, "old.jsonl")
	os.WriteFile(sessionFile, []byte("old data"), 0644)
	oldTime := time.Now().Add(-1 * time.Hour)
	os.Chtimes(sessionFile, oldTime, oldTime)

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
	os.MkdirAll(projectDir, 0755)

	startedAfter := time.Now().Add(-1 * time.Minute)

	// Write older file
	older := filepath.Join(projectDir, "older.jsonl")
	os.WriteFile(older, []byte("older content"), 0644)
	os.Chtimes(older, time.Now().Add(-30*time.Second), time.Now().Add(-30*time.Second))

	// Write newer file
	newer := filepath.Join(projectDir, "newer.jsonl")
	os.WriteFile(newer, []byte("newer content"), 0644)
	os.Chtimes(newer, time.Now(), time.Now())

	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("CaptureSessionHistory: %v", err)
	}

	// Should pick the newest file
	meta, _ := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
	m := ParseChatMeta(string(meta))
	if m["agent_session_id"] != "newer" {
		t.Errorf("picked %q, want newer", m["agent_session_id"])
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
	os.MkdirAll(projectDir, 0755)

	sessionFile := filepath.Join(projectDir, "test.jsonl")
	os.WriteFile(sessionFile, []byte("test data"), 0644)

	startedAfter := time.Now().Add(-1 * time.Minute)
	err := CaptureSessionHistory("claude", cwd, eggDir, home, startedAfter)
	if err != nil {
		t.Fatalf("CaptureSessionHistory: %v", err)
	}

	// Verify no .tmp files remain
	entries, _ := os.ReadDir(eggDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
