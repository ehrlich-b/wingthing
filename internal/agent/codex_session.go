package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func codexRolloutPath(home, threadID string) string {
	if home == "" || threadID == "" {
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
	ambiguous := false
	err = filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), "-"+threadID+".jsonl") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		line, readErr := bufio.NewReader(io.LimitReader(file, maxCodexRolloutHeader+1)).ReadBytes('\n')
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		valid := (readErr == nil || readErr == io.EOF) && len(line) <= maxCodexRolloutHeader && json.Unmarshal(line, &meta) == nil && meta.Type == "session_meta" && meta.Payload.ID == threadID
		_ = file.Close()
		if valid {
			if found != "" {
				ambiguous = true
			}
			found = path
		}
		return nil
	})
	if err != nil || ambiguous {
		return ""
	}
	return found
}
