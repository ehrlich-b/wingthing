package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const maxCodexRolloutHeader = 1024 * 1024

func codexHome(cmd *exec.Cmd) string {
	var home, dataHome string
	for _, entry := range cmd.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "CODEX_HOME":
			dataHome = value
		case "HOME":
			home = value
		}
	}
	if dataHome == "" && home != "" {
		dataHome = filepath.Join(home, ".codex")
	}
	if dataHome == "" {
		return ""
	}
	if !filepath.IsAbs(dataHome) && cmd.Dir != "" {
		dataHome = filepath.Join(cmd.Dir, dataHome)
	}
	return dataHome
}

// Resolve only an existing Codex rollout whose session_meta binds the exact
// thread. Never select the newest file or synthesize a plausible path.
func codexRolloutPath(home, threadID string, startedAt time.Time) string {
	if home == "" || !validCodexThreadID(threadID) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		result <- findCodexRollout(ctx, home, threadID, startedAt, time.Now())
	}()
	// Filesystem calls can block independently of the provider. Metadata lookup
	// must never hold up or alter the terminal result beyond this short deadline.
	select {
	case path := <-result:
		if ctx.Err() == nil {
			return path
		}
	case <-ctx.Done():
	}
	return ""
}

func findCodexRollout(ctx context.Context, home, threadID string, startedAt, now time.Time) string {
	if ctx.Err() != nil {
		return ""
	}
	root, err := filepath.EvalSymlinks(home)
	if err != nil {
		return ""
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return ""
	}
	var found string
	start, end := startedAt.In(time.Local), now.In(time.Local)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	lastDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.Local)
	for ; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		if ctx.Err() != nil {
			return ""
		}
		dir := filepath.Join(root, "sessions", day.Format("2006/01/02"))
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return ""
			}
			if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), "-"+threadID+".jsonl") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if codexRolloutMatchesThread(path, threadID) {
				if found != "" {
					return ""
				}
				found = path
			}
		}
	}
	if ctx.Err() != nil {
		return ""
	}
	return found
}

func codexRolloutMatchesThread(path, threadID string) bool {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	line, readErr := bufio.NewReader(io.LimitReader(file, maxCodexRolloutHeader+1)).ReadBytes('\n')
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	return (readErr == nil || readErr == io.EOF) && len(line) <= maxCodexRolloutHeader && json.Unmarshal(line, &meta) == nil && meta.Type == "session_meta" && meta.Payload.ID == threadID
}
