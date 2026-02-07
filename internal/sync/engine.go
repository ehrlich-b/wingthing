package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

type Engine struct {
	MemoryDir string
	MachineID string
	Store     *store.Store
}

func (e *Engine) BuildLocalManifest() (*Manifest, error) {
	return BuildManifest(e.MemoryDir, e.MachineID)
}

func (e *Engine) ApplyDiffs(diffs []FileDiff, getContent func(path string) ([]byte, error)) error {
	conflictsDir := filepath.Join(e.MemoryDir, ".conflicts")

	for _, d := range diffs {
		content, err := getContent(d.Path)
		if err != nil {
			return fmt.Errorf("get content for %s: %w", d.Path, err)
		}

		targetPath := filepath.Join(e.MemoryDir, d.Path)

		if d.Op == OpUpdate {
			// Check for three-way conflict: local file changed from what we expected
			existing, err := os.ReadFile(targetPath)
			if err == nil {
				localHash := sha256.Sum256(existing)
				remoteHash := sha256.Sum256(content)
				localHex := hex.EncodeToString(localHash[:])
				remoteHex := hex.EncodeToString(remoteHash[:])

				if localHex != remoteHex {
					c := &Conflict{
						Path:       d.Path,
						LocalHash:  localHex,
						RemoteHash: remoteHex,
						Timestamp:  time.Now().UTC().Unix(),
						Resolution: "remote_wins",
					}
					if err := logConflict(conflictsDir, c); err != nil {
						return fmt.Errorf("log conflict for %s: %w", d.Path, err)
					}
				}
			}
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", d.Path, err)
		}

		if err := os.WriteFile(targetPath, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", d.Path, err)
		}
	}

	return nil
}

const timeFmt = "2006-01-02T15:04:05Z"

func (e *Engine) ExportThreadEntries(since time.Time) ([]*store.ThreadEntry, error) {
	rows, err := e.Store.DB().Query(
		`SELECT id, task_id, timestamp, machine_id, agent, skill, user_input, summary, tokens_used
		FROM thread_entries WHERE timestamp >= ? AND machine_id = ? ORDER BY timestamp`,
		since.UTC().Format(timeFmt), e.MachineID)
	if err != nil {
		return nil, fmt.Errorf("export thread entries: %w", err)
	}
	defer rows.Close()

	var entries []*store.ThreadEntry
	for rows.Next() {
		entry := &store.ThreadEntry{}
		var ts string
		if err := rows.Scan(&entry.ID, &entry.TaskID, &ts, &entry.MachineID,
			&entry.Agent, &entry.Skill, &entry.UserInput, &entry.Summary, &entry.TokensUsed); err != nil {
			return nil, fmt.Errorf("scan thread entry: %w", err)
		}
		entry.Timestamp = parseTime(ts)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (e *Engine) ImportThreadEntries(entries []*store.ThreadEntry) error {
	for _, entry := range entries {
		// Skip duplicates: check if entry with same task_id + machine_id + timestamp already exists
		var count int
		if entry.TaskID != nil {
			err := e.Store.DB().QueryRow(
				`SELECT COUNT(*) FROM thread_entries WHERE task_id = ? AND machine_id = ? AND timestamp = ?`,
				entry.TaskID, entry.MachineID, entry.Timestamp.UTC().Format(timeFmt)).Scan(&count)
			if err != nil {
				return fmt.Errorf("check duplicate: %w", err)
			}
			if count > 0 {
				continue
			}
		}

		if err := e.Store.AppendThreadAt(entry, entry.Timestamp); err != nil {
			return fmt.Errorf("import thread entry: %w", err)
		}
	}
	return nil
}

func parseTime(s string) time.Time {
	for _, f := range []string{timeFmt, "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
