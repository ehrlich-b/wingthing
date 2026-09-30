package egg

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var restoreSessionHistoryMu sync.Mutex

const maxRestoreChatMetadataBytes = 64 << 10

func openBoundRegularFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("file changed while opening it")
	}
	return file, nil
}

// RestoreSessionHistory decompresses chat.jsonl.gz and places it in the agent's
// native session directory so the agent can resume the conversation.
// Returns the agent session ID for use with resume flags.
func RestoreSessionHistory(agent, cwd, eggDir, home string) (agentSessionID string, err error) {
	restoreSessionHistoryMu.Lock()
	defer restoreSessionHistoryMu.Unlock()
	metaPath := filepath.Join(eggDir, "chat.meta")
	metaFile, err := openBoundRegularFile(metaPath)
	if err != nil {
		return "", fmt.Errorf("no chat history: %w", err)
	}
	metaData, err := io.ReadAll(io.LimitReader(metaFile, maxRestoreChatMetadataBytes+1))
	_ = metaFile.Close()
	if err != nil {
		return "", fmt.Errorf("read chat metadata: %w", err)
	}
	if len(metaData) > maxRestoreChatMetadataBytes {
		return "", fmt.Errorf("chat metadata is too large")
	}

	meta := ParseChatMeta(string(metaData))
	if recordedAgent := meta["agent"]; recordedAgent == "" || recordedAgent != agent {
		return "", fmt.Errorf("chat.meta agent %q does not match requested agent %q", recordedAgent, agent)
	}
	if recordedCWD := meta["cwd"]; recordedCWD == "" || restoreCanonicalPath(recordedCWD) != restoreCanonicalPath(cwd) {
		return "", fmt.Errorf("chat.meta cwd does not match requested working directory")
	}
	agentSessionID = meta["agent_session_id"]
	if agentSessionID == "" {
		return "", fmt.Errorf("chat.meta missing agent_session_id")
	}
	if filepath.Base(agentSessionID) != agentSessionID || agentSessionID == "." || agentSessionID == ".." || strings.ContainsAny(agentSessionID, "\x00\r\n") || len(agentSessionID) > 240 {
		return "", fmt.Errorf("chat.meta contains invalid agent_session_id")
	}

	gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
	gzFile, err := openBoundRegularFile(gzPath)
	if err != nil {
		return "", fmt.Errorf("open chat.jsonl.gz: %w", err)
	}
	defer func() { _ = gzFile.Close() }()

	gr, err := gzip.NewReader(gzFile)
	if err != nil {
		return "", fmt.Errorf("decompress: %w", err)
	}
	defer func() { _ = gr.Close() }()

	profile := Profile(agent)
	if profile.SessionDir == "" {
		return agentSessionID, fmt.Errorf("agent %q has no session directory", agent)
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return agentSessionID, fmt.Errorf("create provider home: %w", err)
	}
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return agentSessionID, fmt.Errorf("open provider home: %w", err)
	}
	defer func() { _ = homeRoot.Close() }()
	dstDir, dstFile := restoreRelativeDestination(agent, cwd, profile.SessionDir, agentSessionID)
	if err := homeRoot.MkdirAll(dstDir, 0o755); err != nil {
		return agentSessionID, fmt.Errorf("create provider session directory: %w", err)
	}
	dstRoot, err := homeRoot.OpenRoot(dstDir)
	if err != nil {
		return agentSessionID, fmt.Errorf("open provider session directory: %w", err)
	}
	defer func() { _ = dstRoot.Close() }()
	temporaryName, err := restoreTemporaryName()
	if err != nil {
		return agentSessionID, fmt.Errorf("create restore name: %w", err)
	}
	out, err := dstRoot.OpenFile(temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return agentSessionID, fmt.Errorf("create provider session: %w", err)
	}
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = dstRoot.Remove(temporaryName)
		}
	}()
	if err := out.Chmod(0o600); err != nil {
		return agentSessionID, fmt.Errorf("protect dest: %w", err)
	}

	if _, err := io.Copy(out, gr); err != nil {
		return agentSessionID, fmt.Errorf("write: %w", err)
	}
	if err := out.Sync(); err != nil {
		return agentSessionID, fmt.Errorf("sync dest: %w", err)
	}
	if err := out.Close(); err != nil {
		return agentSessionID, fmt.Errorf("close dest: %w", err)
	}
	if existing, statErr := dstRoot.Lstat(dstFile); statErr == nil {
		if !existing.Mode().IsRegular() {
			return agentSessionID, fmt.Errorf("existing provider session is not a regular file")
		}
		equal, compareErr := equalRootFiles(dstRoot, temporaryName, dstFile)
		if compareErr != nil {
			return agentSessionID, fmt.Errorf("compare existing provider session: %w", compareErr)
		}
		if !equal {
			return agentSessionID, fmt.Errorf("existing provider session has advanced; refusing to replace it with an older snapshot")
		}
		if err := dstRoot.Remove(temporaryName); err != nil {
			return agentSessionID, fmt.Errorf("remove duplicate restore: %w", err)
		}
		committed = true
		return agentSessionID, nil
	} else if !os.IsNotExist(statErr) {
		return agentSessionID, fmt.Errorf("inspect existing provider session: %w", statErr)
	}
	if err := dstRoot.Link(temporaryName, dstFile); err != nil {
		if errors.Is(err, os.ErrExist) {
			return agentSessionID, fmt.Errorf("provider session appeared during restore; retry against the latest captured conversation")
		}
		return agentSessionID, fmt.Errorf("commit provider session: %w", err)
	}
	if err := dstRoot.Remove(temporaryName); err != nil {
		_ = dstRoot.Remove(dstFile)
		return agentSessionID, fmt.Errorf("remove temporary provider session: %w", err)
	}
	directory, err := dstRoot.Open(".")
	if err != nil {
		_ = dstRoot.Remove(dstFile)
		return agentSessionID, fmt.Errorf("open provider session directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		_ = dstRoot.Remove(dstFile)
		return agentSessionID, fmt.Errorf("persist provider session: %w", err)
	}
	if err := directory.Close(); err != nil {
		_ = dstRoot.Remove(dstFile)
		return agentSessionID, fmt.Errorf("close provider session directory: %w", err)
	}
	committed = true

	return agentSessionID, nil
}

func restoreCanonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func restoreTemporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".session-restore-" + hex.EncodeToString(random[:]) + ".tmp", nil
}

func equalRootFiles(root *os.Root, leftPath, rightPath string) (bool, error) {
	left, err := root.Open(leftPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = left.Close() }()
	right, err := root.Open(rightPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = right.Close() }()
	leftInfo, err := left.Stat()
	if err != nil {
		return false, err
	}
	rightInfo, err := right.Stat()
	if err != nil {
		return false, err
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false, nil
	}
	leftBuffer := make([]byte, 32<<10)
	rightBuffer := make([]byte, 32<<10)
	for {
		leftCount, leftErr := left.Read(leftBuffer)
		rightCount, rightErr := right.Read(rightBuffer)
		if leftCount != rightCount || !bytes.Equal(leftBuffer[:leftCount], rightBuffer[:rightCount]) {
			return false, nil
		}
		if leftErr == io.EOF && rightErr == io.EOF {
			return true, nil
		}
		if leftErr != nil {
			return false, leftErr
		}
		if rightErr != nil {
			return false, rightErr
		}
	}
}

// restoreRelativeDestination returns the home-relative directory and filename
// where the provider session file should be placed.
func restoreRelativeDestination(agent, cwd, sessionDir, agentSessionID string) (dir, file string) {
	switch agent {
	case "claude":
		encoded := encodeCWDForClaude(cwd)
		return filepath.Join(sessionDir, encoded), agentSessionID + ".jsonl"
	default:
		return sessionDir, agentSessionID + ".jsonl"
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
