package eggclient

import (
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// loadConfigForEgg rejects unusable socket addresses before config.Load can
// create state. Runtime callers with an existing config use the same preflight
// in spawnEgg, so CLI and MCP launches report the same actionable error.
func LoadConfigForEgg(sessionID string) (*config.Config, error) {
	dir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	if err := egg.ValidateSocketPath(filepath.Join(dir, "eggs", sessionID, "egg.sock")); err != nil {
		return nil, err
	}
	return config.Load()
}
