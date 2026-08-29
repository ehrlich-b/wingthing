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
