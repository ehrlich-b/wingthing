package egg

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CaptureSessionHistory copies the agent's native session history (e.g. Claude's JSONL)
// into the egg directory as chat.jsonl.gz + chat.meta. Callers may choose to
// treat capture failures as best-effort, but this function reports them.
func CaptureSessionHistory(agent, cwd, eggDir, home string, startedAfter time.Time) error {
	profile := Profile(agent)
	if profile.SessionDir == "" {
		return nil
	}

	sessionFile, agentSessionID, err := findAgentSession(agent, cwd, home, profile.SessionDir, startedAfter)
	if err != nil {
		return err
	}
	if sessionFile == "" {
		return nil
	}

	// Atomic private write: the captured chat may contain secrets, and replacing
	// the final path must never follow a stale or attacker-created symlink.
	dstPath := filepath.Join(eggDir, "chat.jsonl.gz")

	src, err := os.Open(sessionFile)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp(eggDir, ".chat-jsonl-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temp file: %w", err)
	}

	gw := gzip.NewWriter(tmp)
	if _, err := io.Copy(gw, src); err != nil {
		_ = gw.Close()
		return fmt.Errorf("compress: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	committed = true

	// Write metadata with the same no-symlink, private replacement semantics as
	// the compressed transcript. The session directory can be agent-writable.
	meta := fmt.Sprintf("agent_session_id=%s\nagent=%s\nformat=jsonl\ncwd=%s\n", agentSessionID, agent, cwd)
	if err := atomicWritePrivate(filepath.Join(eggDir, "chat.meta"), []byte(meta)); err != nil {
		return fmt.Errorf("write chat metadata: %w", err)
	}

	return nil
}

func atomicWritePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".chat-meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// FindLiveSessionFile locates the live agent JSONL file for an active session.
// Returns the file path and agent name, or empty strings if not found.
func FindLiveSessionFile(agent, cwd, home string) (string, error) {
	profile := Profile(agent)
	if profile.SessionDir == "" {
		return "", nil
	}
	// Use zero time to find any file (we want the most recent)
	path, _, err := findAgentSession(agent, cwd, home, profile.SessionDir, time.Time{})
	return path, err
}

// findAgentSession locates the agent's most recent session file modified after startedAfter.
func findAgentSession(agent, cwd, home, sessionDir string, startedAfter time.Time) (filePath, sessionID string, err error) {
	switch agent {
	case "claude":
		return findClaudeSession(cwd, home, sessionDir, startedAfter)
	case "codex":
		return findNewestInDir(filepath.Join(home, sessionDir), ".jsonl", startedAfter)
	case "opencode":
		return findNewestInDir(filepath.Join(home, sessionDir), ".jsonl", startedAfter)
	default:
		return "", "", nil
	}
}

// findClaudeSession finds the Claude session file for a given CWD.
// Claude encodes CWD by replacing "/" with "-" for the project directory name.
func findClaudeSession(cwd, home, sessionDir string, startedAfter time.Time) (string, string, error) {
	encoded := encodeCWDForClaude(cwd)
	projectDir := filepath.Join(home, sessionDir, encoded)
	return findNewestInDir(projectDir, ".jsonl", startedAfter)
}

// encodeCWDForClaude encodes a CWD path the same way Claude Code does for project directories.
func encodeCWDForClaude(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

// findNewestInDir finds the newest file with the given extension modified after startedAfter.
func findNewestInDir(dir, ext string, startedAfter time.Time) (filePath, sessionID string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("read dir %s: %w", dir, err)
	}

	var bestPath string
	var bestTime time.Time
	var bestID string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.ModTime().After(startedAfter) {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestPath = filepath.Join(dir, e.Name())
			bestID = strings.TrimSuffix(e.Name(), ext)
		}
	}

	return bestPath, bestID, nil
}
