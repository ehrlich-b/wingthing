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
		capturedPrefix, compareErr := rootFileHasPrefix(dstRoot, dstFile, temporaryName)
		if compareErr != nil {
			return agentSessionID, fmt.Errorf("compare existing provider session: %w", compareErr)
		}
		if !capturedPrefix {
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

func restoreTemporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".session-restore-" + hex.EncodeToString(random[:]) + ".tmp", nil
}

// rootFileHasPrefix reports whether fullPath starts with every byte in
// prefixPath. Providers append to native transcripts while Wingthing is down;
// an existing valid suffix is newer data and must be kept rather than treated
// as a restore conflict.
func rootFileHasPrefix(root *os.Root, fullPath, prefixPath string) (bool, error) {
	full, err := root.Open(fullPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = full.Close() }()
	prefix, err := root.Open(prefixPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = prefix.Close() }()
	prefixInfo, err := prefix.Stat()
	if err != nil {
		return false, err
	}
	fullInfo, err := full.Stat()
	if err != nil {
		return false, err
	}
	if fullInfo.Size() < prefixInfo.Size() {
		return false, nil
	}
	left := make([]byte, 32<<10)
	right := make([]byte, 32<<10)
	for {
		prefixCount, prefixErr := prefix.Read(left)
		if prefixCount > 0 {
			fullCount, fullErr := io.ReadFull(full, right[:prefixCount])
			if fullCount != prefixCount || !bytes.Equal(left[:prefixCount], right[:fullCount]) {
				return false, nil
			}
			if fullErr != nil && fullErr != io.EOF && fullErr != io.ErrUnexpectedEOF {
				return false, fullErr
			}
		}
		if prefixErr == io.EOF {
			return true, nil
		}
		if prefixErr != nil {
			return false, prefixErr
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
