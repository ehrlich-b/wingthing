package egg

import (
	"compress/gzip"
	"errors"
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
func CaptureSessionHistory(agent, cwd, eggDir, home string, startedAfter time.Time, exactSessionID ...string) error {
	profile := Profile(agent)
	if profile.SessionDir == "" {
		return nil
	}
	if agent == "claude" && len(exactSessionID) > 0 && exactSessionID[0] == "" {
		return errors.New("exact Claude provider session ID is required for capture")
	}

	requestedID := ""
	if len(exactSessionID) > 0 {
		requestedID = exactSessionID[0]
	}
	if agent == "claude" && requestedID != "" {
		if err := verifyProviderSession(eggDir, agent, home, requestedID); err != nil {
			return err
		}
	}
	src, agentSessionID, err := openAgentSession(agent, cwd, home, profile.SessionDir, startedAfter, requestedID)
	if err != nil {
		return err
	}
	if src == nil {
		return nil
	}
	defer src.Close()

	// Atomic private write: the captured chat may contain secrets, and replacing
	// the final path must never follow a stale or attacker-created symlink.
	dstPath := filepath.Join(eggDir, "chat.jsonl.gz")

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

// Discovery returns only a name inspected through the same pinned descriptors
// used by capture and lifecycle imports. Those readers keep the returned file.
func findAgentSession(agent, cwd, home, sessionDir string, startedAfter time.Time, exactSessionID ...string) (string, string, error) {
	requestedID := ""
	if len(exactSessionID) > 0 {
		requestedID = exactSessionID[0]
	}
	file, id, err := openAgentSession(agent, cwd, home, sessionDir, startedAfter, requestedID)
	if err != nil || file == nil {
		return "", "", err
	}
	defer file.Close()
	return file.Name(), id, nil
}

func findClaudeSession(cwd, home, sessionDir string, startedAfter time.Time, exactSessionID ...string) (string, string, error) {
	return findAgentSession("claude", cwd, home, sessionDir, startedAfter, exactSessionID...)
}

func openAgentSession(agent, cwd, home, sessionDir string, startedAfter time.Time, requestedID string) (*os.File, string, error) {
	if agent != "claude" && agent != "codex" && agent != "opencode" {
		return nil, "", nil
	}
	root, err := openProviderHome(home)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	relative := sessionDir
	if agent == "claude" {
		relative = filepath.Join(relative, encodeCWDForClaude(cwd))
	}
	dir, err := openProviderDirectory(root, relative, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer dir.Close()
	if agent == "claude" && requestedID != "" {
		if !validLifecycleID(requestedID) {
			return nil, "", errors.New("invalid exact provider session ID")
		}
		file, err := openProviderLeaf(dir, requestedID+".jsonl")
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return file, requestedID, err
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, "", err
	}
	var best *os.File
	var bestTime time.Time
	var bestID string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		file, err := openProviderLeaf(dir, entry.Name())
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.ModTime().After(startedAfter) || !info.ModTime().After(bestTime) {
			file.Close()
			continue
		}
		if best != nil {
			best.Close()
		}
		best, bestTime, bestID = file, info.ModTime(), strings.TrimSuffix(entry.Name(), ".jsonl")
	}
	return best, bestID, nil
}

// The caller's egg metadata is host-owned and masked from provider writes.
// An exact transcript ID must be the one recorded when that egg was launched.
func verifyProviderSession(eggDir, agent, home, id string) error {
	root, err := openProviderHome(eggDir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := openProviderLeaf(root, "egg.meta")
	if err != nil {
		return fmt.Errorf("verify provider session ownership: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRestoreChatMetadataBytes+1))
	if err != nil {
		return err
	}
	meta := ParseChatMeta(string(data))
	if len(data) > maxRestoreChatMetadataBytes || meta["agent"] != agent || meta["provider_session_id"] != id || filepath.Clean(meta["provider_home"]) != filepath.Clean(home) {
		return errors.New("provider session does not belong to this egg")
	}
	return nil
}

// encodeCWDForClaude encodes a CWD path the same way Claude Code does for project directories.
func encodeCWDForClaude(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}
