package egg

import (
	"compress/gzip"
	"errors"
	"fmt"
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
	switch agent {
	case "claude", "codex", "opencode":
	default:
		return nil
	}
	requestedID, err := recordedProviderSessionID(eggDir, agent)
	if err != nil {
		return err
	}
	launchID := requestedID
	var j *lifecycleJournal
	if agent == "claude" || agent == "codex" {
		j, err = openLifecycleJournal(eggDir)
		if err != nil {
			return err
		}
		defer j.close()
		requestedID = recordedHookSessionID(j.events, agent, requestedID)
	}
	root, err := openProviderHome(home)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if root != nil {
		defer root.Close()
	}
	if j != nil {
		requestedID, err = j.importProviderHooks(root, filepath.Join("."+agent, "wingthing-events", filepath.Base(eggDir)), requestedID, agent)
		if err != nil {
			return err
		}
	}
	if len(exactSessionID) > 0 && (agent != "codex" || exactSessionID[0] != "") {
		matches := exactSessionID[0] == launchID
		if j != nil {
			matches = providerSessionRecorded(j.events, agent, launchID, exactSessionID[0])
		}
		if !matches {
			return errors.New("provider session ID does not match this egg's recorded session")
		}
	}
	if agent != "codex" && !validLifecycleID(requestedID) {
		return errors.New("recorded provider session ID is required for capture")
	}
	if !validLifecycleID(requestedID) {
		return nil // A fresh Codex thread has not published SessionStart yet.
	}
	src, agentSessionID, err := openAgentSession(root, agent, cwd, profile.SessionDir, startedAfter, requestedID)
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
	if err := copyConversationArchive(gw, src, agent); err != nil {
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
func findAgentSession(agent, cwd, home, sessionDir string, startedAfter time.Time, exactSessionID ...string) (filePath, sessionID string, err error) {
	requestedID := ""
	if len(exactSessionID) > 0 {
		requestedID = exactSessionID[0]
	}
	root, err := openProviderHome(home)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	file, id, err := openAgentSession(root, agent, cwd, sessionDir, startedAfter, requestedID)
	if err != nil || file == nil {
		return "", "", err
	}
	defer file.Close()
	return file.Name(), id, nil
}

// encodeCWDForClaude encodes a CWD path the same way Claude Code does for project directories.
func encodeCWDForClaude(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

// openAgentSession returns the pinned native file; import/capture callers must
// supply the ID recorded for their egg. Only discovery may select by mtime.
func openAgentSession(root *os.File, agent, cwd, sessionDir string, startedAfter time.Time, id string) (*os.File, string, error) {
	if root == nil {
		return nil, "", nil
	}
	switch agent {
	case "claude":
		sessionDir = filepath.Join(sessionDir, encodeCWDForClaude(cwd))
	case "codex", "opencode":
	default:
		return nil, "", nil
	}
	dir, err := openProviderPath(root, sessionDir, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer dir.Close()
	if id != "" {
		if !validLifecycleID(id) {
			return nil, "", errors.New("invalid exact provider session ID")
		}
		file, err := openProviderPath(dir, id+".jsonl", false)
		if !errors.Is(err, os.ErrNotExist) {
			return file, id, err
		}
		if agent != "codex" {
			return nil, "", nil
		}
		// Main's Codex capture supports flat rollout files. Select only a
		// filename ending in the bound thread ID, never another thread's newest.
		entries, err := dir.ReadDir(-1)
		if err != nil {
			return nil, "", err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), "-"+id+".jsonl") {
				file, err := openProviderPath(dir, entry.Name(), false)
				return file, id, err
			}
		}
		return nil, "", nil
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, "", err
	}

	var best *os.File
	var bestTime time.Time
	var bestID string

	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		file, err := openProviderPath(dir, e.Name(), false)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.ModTime().After(startedAfter) || !info.ModTime().After(bestTime) {
			_ = file.Close()
			continue
		}
		if best != nil {
			_ = best.Close()
		}
		bestTime, best, bestID = info.ModTime(), file, strings.TrimSuffix(e.Name(), ".jsonl")
	}

	return best, bestID, nil
}
