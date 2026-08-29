package egg

import (
	"log"
	"os"
	"path/filepath"
)

// agentConfigFiles maps agent names to their critical config files (relative to $HOME).
var agentConfigFiles = map[string][]string{
	"claude": {"~/.claude/settings.json"},
	"codex":  {"~/.codex/config.json"},
	"cursor": {"~/.cursor/settings.json"},
}

// ConfigSnapshot holds copies of agent config files taken before a session.
type ConfigSnapshot struct {
	files map[string][]byte // path -> original content (nil = didn't exist)
}

// SnapshotAgentConfig reads critical config files for the given agent and saves their contents.
func SnapshotAgentConfig(agent string) *ConfigSnapshot {
	paths, ok := agentConfigFiles[agent]
	if !ok {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	snap := &ConfigSnapshot{files: make(map[string][]byte)}
	for _, p := range paths {
		abs := expandTilde(p, home)
		data, err := os.ReadFile(abs)
		if err != nil {
			snap.files[abs] = nil // file didn't exist
		} else {
			snap.files[abs] = data
		}
	}
	return snap
}

// Restore reverts config files to their pre-session state.
func (s *ConfigSnapshot) Restore() {
	if s == nil {
		return
	}
	for path, data := range s.files {
		if data == nil {
			// File didn't exist before — remove if agent created it
			if _, err := os.Lstat(path); err == nil {
				log.Printf("egg: snapshot: removing agent-created config %s", path)
				if err := os.Remove(path); err != nil {
					log.Printf("egg: snapshot: remove %s: %v", path, err)
				}
			}
		} else {
			current, err := os.ReadFile(path)
			if err != nil || string(current) != string(data) {
				log.Printf("egg: snapshot: restoring %s", path)
				dir := filepath.Dir(path)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					log.Printf("egg: snapshot: create directory %s: %v", dir, err)
					continue
				}
				if err := atomicWritePrivate(path, data); err != nil {
					log.Printf("egg: snapshot: restore %s: %v", path, err)
				}
			}
		}
	}
}
