package relay

import (
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// EnqueueTask stores a task in the offline queue. The payload is opaque JSON
// forwarded to the wing as-is — the relay never inspects it.
func (s *RelayStore) EnqueueTask(t *ws.QueuedTask) error {
	_, err := s.db.Exec(
		`INSERT INTO relay_tasks (id, user_id, identity, payload, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Identity, t.Payload, "pending",
		t.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// DequeueTask deletes a task from the queue after dispatch.
func (s *RelayStore) DequeueTask(taskID string) error {
	_, err := s.db.Exec(`DELETE FROM relay_tasks WHERE id = ?`, taskID)
	return err
}

// ListPendingTasks returns queued tasks for a user that haven't been dispatched.
func (s *RelayStore) ListPendingTasks(userID string) ([]*ws.QueuedTask, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, identity, payload, created_at
		 FROM relay_tasks WHERE user_id = ? AND status = 'pending' ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*ws.QueuedTask
	for rows.Next() {
		t := &ws.QueuedTask{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Identity, &t.Payload, &t.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
