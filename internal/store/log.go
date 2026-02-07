package store

import (
	"fmt"
	"time"
)

type LogEntry struct {
	ID        int64
	TaskID    string
	Timestamp time.Time
	Event     string
	Detail    *string
}

func (s *Store) AppendLog(taskID, event string, detail *string) error {
	_, err := s.db.Exec("INSERT INTO task_log (task_id, event, detail) VALUES (?, ?, ?)", taskID, event, detail)
	if err != nil {
		return fmt.Errorf("append log: %w", err)
	}
	return nil
}

func (s *Store) ListLogByTask(taskID string) ([]*LogEntry, error) {
	rows, err := s.db.Query(`SELECT id, task_id, timestamp, event, detail
		FROM task_log WHERE task_id = ? ORDER BY timestamp`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list log by task: %w", err)
	}
	defer rows.Close()
	var entries []*LogEntry
	for rows.Next() {
		e := &LogEntry{}
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Timestamp, &e.Event, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan log entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
