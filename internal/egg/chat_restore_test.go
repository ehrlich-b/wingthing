package egg

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreSessionHistory_Claude(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	// Create chat.meta
	meta := "agent_session_id=abc123\nagent=claude\nformat=jsonl\ncwd=/Users/test/project\n"
	os.WriteFile(filepath.Join(eggDir, "chat.meta"), []byte(meta), 0644)

	// Create chat.jsonl.gz
	content := `{"type":"human","text":"hello"}` + "\n"
	gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
	f, _ := os.Create(gzPath)
	gw := gzip.NewWriter(f)
	gw.Write([]byte(content))
	gw.Close()
	f.Close()

	agentSessionID, err := RestoreSessionHistory("claude", cwd, eggDir, home)
	if err != nil {
		t.Fatalf("RestoreSessionHistory: %v", err)
	}
	if agentSessionID != "abc123" {
		t.Errorf("agentSessionID = %q, want %q", agentSessionID, "abc123")
	}

	// Verify file was placed in agent session dir
	encoded := encodeCWDForClaude(cwd)
	dstPath := filepath.Join(home, ".claude", "projects", encoded, "abc123.jsonl")
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != content {
		t.Errorf("content mismatch: got %q, want %q", string(data), content)
	}
}

func TestRestoreSessionHistory_NoChat(t *testing.T) {
	eggDir := t.TempDir()
	_, err := RestoreSessionHistory("claude", "/tmp", eggDir, "/home/test")
	if err == nil {
		t.Error("expected error for missing chat history")
	}
}

func TestRestoreSessionHistory_BadMeta(t *testing.T) {
	eggDir := t.TempDir()

	// Create invalid chat.meta (missing agent_session_id)
	os.WriteFile(filepath.Join(eggDir, "chat.meta"), []byte("agent=claude\n"), 0644)

	// Create chat.jsonl.gz
	gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
	f, _ := os.Create(gzPath)
	gw := gzip.NewWriter(f)
	gw.Write([]byte("test"))
	gw.Close()
	f.Close()

	_, err := RestoreSessionHistory("claude", "/tmp", eggDir, t.TempDir())
	if err == nil {
		t.Error("expected error for bad meta")
	}
}
