package store

import (
	"database/sql"
	"fmt"
	"time"
)

type ThreadEntry struct {
	ID         int64
	TaskID     *string
	Timestamp  time.Time
	MachineID  string
	Agent      *string
	Skill      *string
	UserInput  *string
	Summary    string
	TokensUsed *int
}

func (s *Store) AppendThread(e *ThreadEntry) error {
	ts := time.Now().UTC().Format(timeFmt)
	res, err := s.db.Exec(`INSERT INTO thread_entries (task_id, timestamp, machine_id, agent, skill, user_input, summary, tokens_used)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TaskID, ts, e.MachineID, e.Agent, e.Skill, e.UserInput, e.Summary, e.TokensUsed)
	if err != nil {
		return fmt.Errorf("append thread: %w", err)
	}
	id, _ := res.LastInsertId()
	e.ID = id
	return nil
}

func (s *Store) AppendThreadAt(e *ThreadEntry, ts time.Time) error {
	res, err := s.db.Exec(`INSERT INTO thread_entries (task_id, timestamp, machine_id, agent, skill, user_input, summary, tokens_used)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TaskID, ts.UTC().Format(timeFmt), e.MachineID, e.Agent, e.Skill, e.UserInput, e.Summary, e.TokensUsed)
	if err != nil {
		return fmt.Errorf("append thread: %w", err)
	}
	id, _ := res.LastInsertId()
	e.ID = id
	return nil
}

func (s *Store) ListThreadByDate(date time.Time) ([]*ThreadEntry, error) {
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).Format(timeFmt)
	dayEnd := time.Date(date.Year(), date.Month(), date.Day()+1, 0, 0, 0, 0, time.UTC).Format(timeFmt)
	rows, err := s.db.Query(`SELECT id, task_id, timestamp, machine_id, agent, skill, user_input, summary, tokens_used
		FROM thread_entries WHERE timestamp >= ? AND timestamp < ? ORDER BY timestamp`, dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("list thread by date: %w", err)
	}
	defer rows.Close()
	return scanThreadEntries(rows)
}

func (s *Store) ListRecentThread(n int) ([]*ThreadEntry, error) {
	rows, err := s.db.Query(`SELECT id, task_id, timestamp, machine_id, agent, skill, user_input, summary, tokens_used
		FROM thread_entries ORDER BY timestamp DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list recent thread: %w", err)
	}
	defer rows.Close()
	return scanThreadEntries(rows)
}

func (s *Store) DeleteThreadOlderThan(ts time.Time) (int64, error) {
	res, err := s.db.Exec("DELETE FROM thread_entries WHERE timestamp < ?", ts.UTC().Format(timeFmt))
	if err != nil {
		return 0, fmt.Errorf("delete old thread: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) SumTokensByDateRange(start, end time.Time) (int, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(tokens_used) FROM thread_entries WHERE timestamp >= ? AND timestamp < ?`,
		start.UTC().Format(timeFmt), end.UTC().Format(timeFmt)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum tokens: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return int(total.Int64), nil
}

// ThreadEntryExists checks if a thread entry with the given task_id, machine_id, and timestamp exists.
func (s *Store) ThreadEntryExists(taskID *string, machineID string, timestamp time.Time) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM thread_entries WHERE task_id = ? AND machine_id = ? AND timestamp = ?`,
		taskID, machineID, timestamp.UTC().Format(timeFmt)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check thread entry exists: %w", err)
	}
	return count > 0, nil
}

// ThreadEntryExistsBySummary checks if a thread entry with the given machine_id, timestamp, and summary exists.
func (s *Store) ThreadEntryExistsBySummary(machineID string, timestamp time.Time, summary string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM thread_entries WHERE machine_id = ? AND timestamp = ? AND summary = ?`,
		machineID, timestamp.UTC().Format(timeFmt), summary).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check thread entry exists by summary: %w", err)
	}
	return count > 0, nil
}

func scanThreadEntries(rows *sql.Rows) ([]*ThreadEntry, error) {
	var entries []*ThreadEntry
	for rows.Next() {
		e := &ThreadEntry{}
		var ts string
		if err := rows.Scan(&e.ID, &e.TaskID, &ts, &e.MachineID, &e.Agent, &e.Skill, &e.UserInput, &e.Summary, &e.TokensUsed); err != nil {
			return nil, fmt.Errorf("scan thread entry: %w", err)
		}
		e.Timestamp = parseTime(ts)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
