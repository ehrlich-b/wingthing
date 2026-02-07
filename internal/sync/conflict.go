package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Conflict struct {
	Path       string `json:"path"`
	LocalHash  string `json:"local_hash"`
	RemoteHash string `json:"remote_hash"`
	Timestamp  int64  `json:"timestamp"`
	Resolution string `json:"resolution"`
}

func logConflict(conflictsDir string, c *Conflict) error {
	if err := os.MkdirAll(conflictsDir, 0o755); err != nil {
		return fmt.Errorf("create conflicts dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal conflict: %w", err)
	}

	ts := time.Unix(c.Timestamp, 0).UTC().Format("20060102T150405Z")
	// Use a sanitized path for the filename
	safePath := filepath.Base(c.Path)
	name := fmt.Sprintf("%s_%s.json", ts, safePath)
	path := filepath.Join(conflictsDir, name)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write conflict: %w", err)
	}
	return nil
}
