package egg

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CaptureSessionHistory copies the agent's native session history (e.g. Claude's JSONL)
// into the egg directory as chat.jsonl.gz + chat.meta. Best-effort: errors are logged, never fatal.
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

	// Atomic write: temp file + rename
	tmpPath := filepath.Join(eggDir, "chat.jsonl.gz.tmp")
	dstPath := filepath.Join(eggDir, "chat.jsonl.gz")

	src, err := os.Open(sessionFile)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer src.Close()

	tmp, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	gw := gzip.NewWriter(tmp)
	if _, err := io.Copy(gw, src); err != nil {
		gw.Close()
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("compress: %w", err)
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("gzip close: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	// Write metadata
	meta := fmt.Sprintf("agent_session_id=%s\nagent=%s\nformat=jsonl\ncwd=%s\n", agentSessionID, agent, cwd)
	if err := os.WriteFile(filepath.Join(eggDir, "chat.meta"), []byte(meta), 0644); err != nil {
		log.Printf("egg: chat.meta write failed: %v", err)
	}

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
