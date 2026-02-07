package store

import (
	"database/sql"
	"fmt"
	"time"
)

const timeFmt = "2006-01-02T15:04:05Z"

type Task struct {
	ID         string
	Type       string
	What       string
	RunAt      time.Time
	Agent      string
	Isolation  string
	Memory     *string
	ParentID   *string
	Status     string
	Cron       *string
	MachineID  *string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Output     *string
	Error      *string
}

func (s *Store) CreateTask(t *Task) error {
	if t.Status == "" {
		t.Status = "pending"
	}
	if t.Isolation == "" {
		t.Isolation = "standard"
	}
	if t.Type == "" {
		t.Type = "prompt"
	}
	if t.Agent == "" {
		t.Agent = "claude"
	}
	_, err := s.db.Exec(`INSERT INTO tasks (id, type, what, run_at, agent, isolation, memory, parent_id, status, cron, machine_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Type, t.What, t.RunAt.UTC().Format(timeFmt), t.Agent, t.Isolation, t.Memory, t.ParentID, t.Status, t.Cron, t.MachineID)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

func (s *Store) GetTask(id string) (*Task, error) {
	t := &Task{}
	var runAt, createdAt string
	var startedAt, finishedAt *string
	err := s.db.QueryRow(`SELECT id, type, what, run_at, agent, isolation, memory, parent_id, status, cron, machine_id,
		created_at, started_at, finished_at, output, error FROM tasks WHERE id = ?`, id).Scan(
		&t.ID, &t.Type, &t.What, &runAt, &t.Agent, &t.Isolation, &t.Memory, &t.ParentID, &t.Status, &t.Cron, &t.MachineID,
		&createdAt, &startedAt, &finishedAt, &t.Output, &t.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	t.RunAt = parseTime(runAt)
	t.CreatedAt = parseTime(createdAt)
	t.StartedAt = parseTimePtr(startedAt)
	t.FinishedAt = parseTimePtr(finishedAt)
	return t, nil
}

func (s *Store) ListPending(now time.Time) ([]*Task, error) {
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, isolation, memory, parent_id, status, cron, machine_id,
		created_at, started_at, finished_at, output, error
		FROM tasks WHERE status = 'pending' AND run_at <= ? ORDER BY run_at`, now.UTC().Format(timeFmt))
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ListRecent(n int) ([]*Task, error) {
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, isolation, memory, parent_id, status, cron, machine_id,
		created_at, started_at, finished_at, output, error
		FROM tasks ORDER BY created_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list recent: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) UpdateTaskStatus(id, status string) error {
	now := time.Now().UTC().Format(timeFmt)
	var col string
	switch status {
	case "running":
		col = "started_at"
	case "done", "failed":
		col = "finished_at"
	default:
		_, err := s.db.Exec("UPDATE tasks SET status = ? WHERE id = ?", status, id)
		return err
	}
	_, err := s.db.Exec(fmt.Sprintf("UPDATE tasks SET status = ?, %s = ? WHERE id = ?", col), status, now, id)
	return err
}

func (s *Store) SetTaskOutput(id, output string) error {
	_, err := s.db.Exec("UPDATE tasks SET output = ? WHERE id = ?", output, id)
	return err
}

func (s *Store) SetTaskError(id, errMsg string) error {
	now := time.Now().UTC().Format(timeFmt)
	_, err := s.db.Exec("UPDATE tasks SET error = ?, status = 'failed', finished_at = ? WHERE id = ?", errMsg, now, id)
	return err
}

func (s *Store) ListRecurring() ([]*Task, error) {
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, isolation, memory, parent_id, status, cron, machine_id,
		created_at, started_at, finished_at, output, error
		FROM tasks WHERE cron IS NOT NULL AND cron != '' ORDER BY run_at`)
	if err != nil {
		return nil, fmt.Errorf("list recurring: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ClearTaskCron(id string) error {
	_, err := s.db.Exec("UPDATE tasks SET cron = NULL WHERE id = ?", id)
	return err
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var tasks []*Task
	for rows.Next() {
		t := &Task{}
		var runAt, createdAt string
		var startedAt, finishedAt *string
		if err := rows.Scan(&t.ID, &t.Type, &t.What, &runAt, &t.Agent, &t.Isolation, &t.Memory, &t.ParentID,
			&t.Status, &t.Cron, &t.MachineID, &createdAt, &startedAt, &finishedAt, &t.Output, &t.Error); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		t.RunAt = parseTime(runAt)
		t.CreatedAt = parseTime(createdAt)
		t.StartedAt = parseTimePtr(startedAt)
		t.FinishedAt = parseTimePtr(finishedAt)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func parseTime(s string) time.Time {
	for _, fmt := range []string{timeFmt, "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(fmt, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func parseTimePtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t := parseTime(*s)
	if t.IsZero() {
		return nil
	}
	return &t
}
