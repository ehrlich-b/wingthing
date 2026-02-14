package sync

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ehrlich-b/wingthing/internal/store"
)

// MergeThreadEntries merges remote thread entries into the local store.
// Deduplicates by (task_id, wing_id, timestamp) triple.
// Entries without task_id use (wing_id, timestamp, summary) for dedup.
// Returns count of entries imported.
func (e *Engine) MergeThreadEntries(remote []*store.ThreadEntry) (int, error) {
	// Sort remote entries by timestamp for consistent ordering.
	sorted := make([]*store.ThreadEntry, len(remote))
	copy(sorted, remote)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	var imported int
	var errs []error

	for _, entry := range sorted {
		var exists bool
		var err error

		if entry.TaskID != nil {
			exists, err = e.Store.ThreadEntryExists(entry.TaskID, entry.WingID, entry.Timestamp)
		} else {
			exists, err = e.Store.ThreadEntryExistsBySummary(entry.WingID, entry.Timestamp, entry.Summary)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("check dedup for entry %q: %w", entry.Summary, err))
			continue
		}
		if exists {
			continue
		}

		if err := e.Store.AppendThreadAt(entry, entry.Timestamp); err != nil {
			errs = append(errs, fmt.Errorf("import entry %q: %w", entry.Summary, err))
			continue
		}
		imported++
	}

	return imported, errors.Join(errs...)
}
