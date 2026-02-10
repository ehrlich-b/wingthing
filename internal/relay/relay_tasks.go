package relay

import (
	"database/sql"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func (s *RelayStore) CreateRelayTask(t *ws.RelayTask) error {
	_, err := s.db.Exec(
		`INSERT INTO relay_tasks (id, user_id, identity, prompt, skill, agent, isolation, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Identity, t.Prompt, t.Skill, t.Agent, t.Isolation, t.Status,
		t.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

func (s *RelayStore) UpdateRelayTask(t *ws.RelayTask) error {
	var startedAt *string
	if t.StartedAt != nil {
		s := t.StartedAt.UTC().Format("2006-01-02 15:04:05")
		startedAt = &s
	}
	_, err := s.db.Exec(
		`UPDATE relay_tasks SET status = ?, wing_id = ?, started_at = ? WHERE id = ?`,
		t.Status, t.WingID, startedAt, t.ID,
	)
	return err
}

func (s *RelayStore) CompleteRelayTask(taskID, output string, finishedAt *time.Time) error {
	var fin *string
	if finishedAt != nil {
		s := finishedAt.UTC().Format("2006-01-02 15:04:05")
		fin = &s
	}
	_, err := s.db.Exec(
		`UPDATE relay_tasks SET status = 'done', output = ?, finished_at = ? WHERE id = ?`,
		output, fin, taskID,
	)
	return err
}

func (s *RelayStore) FailRelayTask(taskID, errMsg string, finishedAt *time.Time) error {
	var fin *string
	if finishedAt != nil {
		s := finishedAt.UTC().Format("2006-01-02 15:04:05")
		fin = &s
	}
	_, err := s.db.Exec(
		`UPDATE relay_tasks SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`,
		errMsg, fin, taskID,
	)
	return err
}

func (s *RelayStore) ListPendingRelayTasks(userID string) ([]*ws.RelayTask, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, identity, prompt, skill, agent, isolation, status, created_at
		 FROM relay_tasks WHERE user_id = ? AND status = 'pending' ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelayTasks(rows)
}

func (s *RelayStore) ListRelayTasksForUser(userID string, limit int) ([]*ws.RelayTask, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, identity, prompt, skill, agent, isolation, status, output, error, wing_id, created_at, started_at, finished_at
		 FROM relay_tasks WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelayTasksFull(rows)
}

func (s *RelayStore) GetRelayTask(taskID string) (*ws.RelayTask, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, identity, prompt, skill, agent, isolation, status, output, error, wing_id, created_at, started_at, finished_at
		 FROM relay_tasks WHERE id = ?`, taskID,
	)
	t := &ws.RelayTask{}
	var output, errStr, wingID sql.NullString
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(&t.ID, &t.UserID, &t.Identity, &t.Prompt, &t.Skill, &t.Agent, &t.Isolation,
		&t.Status, &output, &errStr, &wingID, &t.CreatedAt, &startedAt, &finishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if output.Valid {
		t.Output = output.String
	}
	if errStr.Valid {
		t.Error = errStr.String
	}
	if wingID.Valid {
		t.WingID = wingID.String
	}
	if startedAt.Valid {
		t.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		t.FinishedAt = &finishedAt.Time
	}
	return t, nil
}

func scanRelayTasks(rows *sql.Rows) ([]*ws.RelayTask, error) {
	var tasks []*ws.RelayTask
	for rows.Next() {
		t := &ws.RelayTask{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Identity, &t.Prompt, &t.Skill, &t.Agent, &t.Isolation, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func scanRelayTasksFull(rows *sql.Rows) ([]*ws.RelayTask, error) {
	var tasks []*ws.RelayTask
	for rows.Next() {
		t := &ws.RelayTask{}
		var output, errStr, wingID sql.NullString
		var startedAt, finishedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.UserID, &t.Identity, &t.Prompt, &t.Skill, &t.Agent, &t.Isolation,
			&t.Status, &output, &errStr, &wingID, &t.CreatedAt, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		if output.Valid {
			t.Output = output.String
		}
		if errStr.Valid {
			t.Error = errStr.String
		}
		if wingID.Valid {
			t.WingID = wingID.String
		}
		if startedAt.Valid {
			t.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			t.FinishedAt = &finishedAt.Time
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
