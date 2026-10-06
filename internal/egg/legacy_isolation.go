package egg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
)

const ControlIsolationVersion = "2"

// The marker is written by the host before publishing a new egg's PID. Missing
// markers include v0.147.0 and earlier hardening builds with incomplete isolation.
func HasCurrentControlIsolation(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "control_isolation="+ControlIsolationVersion {
			return true
		}
	}
	return false
}

// Legacy PTYs remain usable, but no new sandbox may publish a controller secret
// until their old read authority is gone. Check both channels and selected state.
func RequireCurrentEggIsolation(sessionDir string) error {
	home, _ := os.UserHomeDir()
	states := []string{filepath.Join(home, ".wingthing"), filepath.Join(home, ".wingthing-preview"), filepath.Dir(filepath.Dir(sessionDir))}
	if state, err := config.StateDir(); err == nil {
		states = append(states, state)
	}
	seen := make(map[string]bool)
	for _, state := range states {
		state = config.CanonicalProviderPath(state)
		if seen[state] {
			continue
		}
		seen[state] = true
		entries, err := os.ReadDir(filepath.Join(state, "eggs"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("check legacy egg isolation: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(state, "eggs", entry.Name())
			if HasCurrentControlIsolation(dir) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect legacy egg PID: %w", err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && procinfo.OwnedProcessIsAlive(pid) {
				return fmt.Errorf("legacy egg %s requires replacement before new sandboxed sessions or tool capability recovery; its PTY remains available", entry.Name())
			}
		}
	}
	return nil
}
