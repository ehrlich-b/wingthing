package egg

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRestoreFixture(t *testing.T, eggDir, meta, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(eggDir, "chat.meta"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(eggDir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreSessionHistory_Claude(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"

	// Create chat.meta
	meta := "agent_session_id=abc123\nagent=claude\nformat=jsonl\ncwd=/Users/test/project\n"
	content := `{"type":"human","text":"hello"}` + "\n"
	writeRestoreFixture(t, eggDir, meta, content)

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
	writeRestoreFixture(t, eggDir, "agent=claude\n", "test")

	_, err := RestoreSessionHistory("claude", "/tmp", eggDir, t.TempDir())
	if err == nil {
		t.Error("expected error for bad meta")
	}
}

func TestRestoreSessionHistory_RejectsInvalidSessionID(t *testing.T) {
	for _, id := range []string{"../outside", "..", ".", "dir/session", strings.Repeat("x", 241)} {
		t.Run(id, func(t *testing.T) {
			eggDir := t.TempDir()
			home := t.TempDir()
			meta := "agent_session_id=" + id + "\nagent=claude\nformat=jsonl\n"
			writeRestoreFixture(t, eggDir, meta, "sensitive")

			if _, err := RestoreSessionHistory("claude", "/tmp/project", eggDir, home); err == nil {
				t.Fatal("expected invalid session ID to be rejected")
			}
			if _, err := os.Stat(filepath.Join(home, "outside.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("unexpected file outside session directory: %v", err)
			}
		})
	}
}

func TestRestoreSessionHistory_ReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"
	meta := "agent_session_id=abc123\nagent=claude\nformat=jsonl\n"
	writeRestoreFixture(t, eggDir, meta, "restored transcript\n")

	dstDir := filepath.Join(home, ".claude", "projects", encodeCWDForClaude(cwd))
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, "abc123.jsonl")
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err != nil {
		t.Fatal(err)
	}
	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTarget) != "original" {
		t.Fatalf("symlink target changed: %q", gotTarget)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored file mode = %v, want private regular file", info.Mode())
	}
}
