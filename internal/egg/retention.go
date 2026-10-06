package egg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// HasRetainedSessionData keeps ownership and provider identity alongside native
// journals and retry reservations. An unreadable artifact is never absence.
func HasRetainedSessionData(dir string) bool {
	for _, name := range []string{LaunchIntentFile, "audit.pty.gz", "audit.log", "chat.jsonl.gz", "lifecycle.jsonl", "egg.failed.log", "prompt.lock"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "prompt.") && strings.HasSuffix(entry.Name(), ".json") {
			return true
		}
	}
	return false
}
