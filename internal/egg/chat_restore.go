package egg

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RestoreSessionHistory decompresses chat.jsonl.gz and places it in the agent's
// native session directory so the agent can resume the conversation.
// Returns the agent session ID for use with resume flags.
func RestoreSessionHistory(agent, cwd, eggDir, home string) (agentSessionID string, err error) {
	metaPath := filepath.Join(eggDir, "chat.meta")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return "", fmt.Errorf("no chat history: %w", err)
	}

	meta := ParseChatMeta(string(metaData))
	agentSessionID = meta["agent_session_id"]
	if agentSessionID == "" {
		return "", fmt.Errorf("chat.meta missing agent_session_id")
	}

	gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
	gzFile, err := os.Open(gzPath)
	if err != nil {
		return "", fmt.Errorf("open chat.jsonl.gz: %w", err)
	}
	defer gzFile.Close()

	gr, err := gzip.NewReader(gzFile)
	if err != nil {
		return "", fmt.Errorf("decompress: %w", err)
	}
	defer gr.Close()

	profile := Profile(agent)
	if profile.SessionDir == "" {
		return agentSessionID, fmt.Errorf("agent %q has no session directory", agent)
	}

	// Determine destination path
	dstDir, dstFile := restoreDestination(agent, cwd, home, profile.SessionDir, agentSessionID)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return agentSessionID, fmt.Errorf("create dir: %w", err)
	}

	dstPath := filepath.Join(dstDir, dstFile)
	out, err := os.Create(dstPath)
	if err != nil {
		return agentSessionID, fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, gr); err != nil {
		return agentSessionID, fmt.Errorf("write: %w", err)
	}

	return agentSessionID, nil
}

// restoreDestination returns the directory and filename where the session file should be placed.
func restoreDestination(agent, cwd, home, sessionDir, agentSessionID string) (dir, file string) {
	switch agent {
	case "claude":
		encoded := encodeCWDForClaude(cwd)
		return filepath.Join(home, sessionDir, encoded), agentSessionID + ".jsonl"
	default:
		return filepath.Join(home, sessionDir), agentSessionID + ".jsonl"
	}
}

// ParseChatMeta parses a simple key=value metadata file.
func ParseChatMeta(data string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}
