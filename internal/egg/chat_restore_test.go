package egg

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
			meta := "agent_session_id=" + id + "\nagent=claude\nformat=jsonl\ncwd=/tmp/project\n"
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

func TestRestoreSessionHistory_RejectsExistingSymlinkWithoutFollowingIt(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/Users/test/project"
	meta := "agent_session_id=abc123\nagent=claude\nformat=jsonl\ncwd=/Users/test/project\n"
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

	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err == nil {
		t.Fatal("existing provider symlink was accepted")
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
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("provider symlink was replaced: %v", info.Mode())
	}
}

func TestRestoreSessionHistoryRejectsProviderRollbackAndAllowsIdenticalRetry(t *testing.T) {
	home := t.TempDir()
	eggDir := t.TempDir()
	cwd := "/tmp/project"
	meta := "agent_session_id=abc123\nagent=claude\nformat=jsonl\ncwd=/tmp/project\n"
	writeRestoreFixture(t, eggDir, meta, "snapshot\n")
	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err != nil {
		t.Fatalf("identical retry failed: %v", err)
	}
	destination := filepath.Join(home, ".claude", "projects", encodeCWDForClaude(cwd), "abc123.jsonl")
	if err := os.WriteFile(destination, []byte("snapshot\nnew provider event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err != nil {
		t.Fatalf("advanced provider transcript with captured prefix was rejected: %v", err)
	}
	data, _ := os.ReadFile(destination)
	if string(data) != "snapshot\nnew provider event\n" {
		t.Fatalf("advanced provider conversation changed: %q", data)
	}
	if err := os.WriteFile(destination, []byte("diverged conversation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSessionHistory("claude", cwd, eggDir, home); err == nil || !strings.Contains(err.Error(), "advanced") {
		t.Fatalf("rollback error = %v", err)
	}
	data, _ = os.ReadFile(destination)
	if string(data) != "diverged conversation\n" {
		t.Fatalf("advanced provider conversation changed: %q", data)
	}
}

func TestRestoreSessionHistorySerializesConflictingConcurrentResumes(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/project"
	meta := "agent_session_id=shared-id\nagent=claude\nformat=jsonl\ncwd=/tmp/project\n"
	dirs := []string{t.TempDir(), t.TempDir()}
	writeRestoreFixture(t, dirs[0], meta, "first\n")
	writeRestoreFixture(t, dirs[1], meta, "second\n")
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for _, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := RestoreSessionHistory("claude", cwd, dir, home)
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	successes, failures := 0, 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent restores: successes=%d failures=%d", successes, failures)
	}
}

func TestRestoreSessionHistoryPreservesCLICrossCWDCompatibility(t *testing.T) {
	eggDir := t.TempDir()
	writeRestoreFixture(t, eggDir, "agent_session_id=abc\nagent=claude\nformat=jsonl\ncwd=/tmp/a\n", "content")
	home := t.TempDir()
	if _, err := RestoreSessionHistory("claude", "/tmp/b", eggDir, home); err != nil {
		t.Fatalf("cross-CWD CLI restore failed: %v", err)
	}
	destination := filepath.Join(home, ".claude", "projects", encodeCWDForClaude("/tmp/b"), "abc.jsonl")
	if data, err := os.ReadFile(destination); err != nil || string(data) != "content" {
		t.Fatalf("cross-CWD restore = %q err=%v", data, err)
	}
}

func TestRestoreSessionHistoryDoesNotEscapeProviderHomeSymlink(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	eggDir := t.TempDir()
	writeRestoreFixture(t, eggDir, "agent_session_id=abc\nagent=claude\nformat=jsonl\ncwd=/tmp/project\n", "sensitive")
	if _, err := RestoreSessionHistory("claude", "/tmp/project", eggDir, home); err == nil {
		t.Fatal("provider home symlink escape was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restore wrote outside provider home: %v", entries)
	}
}

func TestRestoreSessionHistoryRejectsSymlinkedSourceArtifacts(t *testing.T) {
	for _, artifact := range []string{"chat.meta", "chat.jsonl.gz"} {
		t.Run(artifact, func(t *testing.T) {
			eggDir := t.TempDir()
			writeRestoreFixture(t, eggDir, "agent_session_id=abc\nagent=claude\nformat=jsonl\ncwd=/tmp/project\n", "sensitive")
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(eggDir, artifact)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(eggDir, artifact)); err != nil {
				t.Fatal(err)
			}
			if _, err := RestoreSessionHistory("claude", "/tmp/project", eggDir, t.TempDir()); err == nil {
				t.Fatal("symlinked source artifact was accepted")
			}
		})
	}
}
