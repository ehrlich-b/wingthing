package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const timeFmt = "2006-01-02T15:04:05Z"

type Task struct {
	ID                  string
	Type                string
	What                string
	RunAt               time.Time
	Agent               string
	Model               string
	TimeoutSeconds      int
	Isolation           string
	Memory              *string
	ParentID            *string
	Status              string
	Cron                *string
	WingID              *string
	CreatedAt           time.Time
	StartedAt           *time.Time
	FinishedAt          *time.Time
	Output              *string
	Error               *string
	ErrorKind           string
	ProviderThreadID    string
	ProviderRolloutPath string
	RetryCount          int
	MaxRetries          int
	DependsOn           *string
	CWD                 string
	PromptName          string
	PromptRevision      string
	Principal           string
	RunnerPID           int
	RunnerIdentity      string
	EggConfigYAML       string
}

func (s *Store) CreateTask(t *Task) error {
	if t.Status == "" {
		t.Status = "pending"
	}
	if t.Isolation == "" {
		t.Isolation = "none"
	}
	if t.Type == "" {
		t.Type = "prompt"
	}
	_, err := s.db.Exec(`INSERT INTO tasks (id, type, what, run_at, agent, model, timeout_seconds, isolation, memory, parent_id, status, cron, wing_id, retry_count, max_retries, depends_on, cwd, prompt_name, prompt_revision, principal, runner_pid, runner_identity, egg_config, error_kind, provider_thread_id, provider_rollout_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Type, t.What, t.RunAt.UTC().Format(timeFmt), t.Agent, t.Model, t.TimeoutSeconds, t.Isolation, t.Memory, t.ParentID, t.Status, t.Cron, t.WingID, t.RetryCount, t.MaxRetries, t.DependsOn, t.CWD, t.PromptName, t.PromptRevision, t.Principal, t.RunnerPID, t.RunnerIdentity, t.EggConfigYAML, t.ErrorKind, t.ProviderThreadID, t.ProviderRolloutPath)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

func (s *Store) GetTask(id string) (*Task, error) {
	t := &Task{}
	var runAt, createdAt string
	var startedAt, finishedAt *string
	err := s.db.QueryRow(`SELECT id, type, what, run_at, agent, model, timeout_seconds, isolation, memory, parent_id, status, cron, wing_id,
		created_at, started_at, finished_at, output, error, retry_count, max_retries, depends_on, cwd, prompt_name, prompt_revision, principal, runner_pid, runner_identity, egg_config, error_kind, provider_thread_id, provider_rollout_path FROM tasks WHERE id = ?`, id).Scan(
		&t.ID, &t.Type, &t.What, &runAt, &t.Agent, &t.Model, &t.TimeoutSeconds, &t.Isolation, &t.Memory, &t.ParentID, &t.Status, &t.Cron, &t.WingID,
		&createdAt, &startedAt, &finishedAt, &t.Output, &t.Error, &t.RetryCount, &t.MaxRetries, &t.DependsOn, &t.CWD, &t.PromptName, &t.PromptRevision, &t.Principal, &t.RunnerPID, &t.RunnerIdentity, &t.EggConfigYAML, &t.ErrorKind, &t.ProviderThreadID, &t.ProviderRolloutPath)
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
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, model, timeout_seconds, isolation, memory, parent_id, status, cron, wing_id,
		created_at, started_at, finished_at, output, error, retry_count, max_retries, depends_on, cwd, prompt_name, prompt_revision, principal, runner_pid, runner_identity, egg_config, error_kind, provider_thread_id, provider_rollout_path
		FROM tasks WHERE status = 'pending' AND run_at <= ? ORDER BY run_at`, now.UTC().Format(timeFmt))
	if err != nil {
		return nil, fmt.Errorf("list pending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

func (s *Store) ListRecent(n int) ([]*Task, error) {
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, model, timeout_seconds, isolation, memory, parent_id, status, cron, wing_id,
		created_at, started_at, finished_at, output, error, retry_count, max_retries, depends_on, cwd, prompt_name, prompt_revision, principal, runner_pid, runner_identity, egg_config, error_kind, provider_thread_id, provider_rollout_path
		FROM tasks ORDER BY created_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("list recent: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

// ListReady returns pending tasks where all dependencies are done (or have no dependencies).
func (s *Store) ListReady(now time.Time) ([]*Task, error) {
	tasks, err := s.ListPending(now)
	if err != nil {
		return nil, err
	}
	var ready []*Task
	for _, t := range tasks {
		if t.DependsOn == nil {
			ready = append(ready, t)
			continue
		}
		var depIDs []string
		if err := json.Unmarshal([]byte(*t.DependsOn), &depIDs); err != nil {
			return nil, fmt.Errorf("decode dependencies for task %s: %w", t.ID, err)
		}
		allDone := true
		for _, depID := range depIDs {
			dep, err := s.GetTask(depID)
			if err != nil {
				return nil, fmt.Errorf("load dependency %s for task %s: %w", depID, t.ID, err)
			}
			if dep == nil || dep.Status != "done" {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, t)
		}
	}
	return ready, nil
}

func (s *Store) UpdateTaskStatus(id, status string) error {
	now := time.Now().UTC().Format(timeFmt)
	var col string
	switch status {
	case "running":
		col = "started_at"
	case "done", "failed", "orphaned":
		col = "finished_at"
	default:
		_, err := s.db.Exec("UPDATE tasks SET status = ? WHERE id = ?", status, id)
		return err
	}
	_, err := s.db.Exec(fmt.Sprintf("UPDATE tasks SET status = ?, %s = ? WHERE id = ?", col), status, now, id)
	return err
}

// SetTaskResolved records the runtime choices made by the orchestrator. Tasks
// may be submitted without either field, so the stored values must be updated
// after skill/config precedence has been resolved if API clients are to see
// what actually ran.
func (s *Store) SetTaskResolved(id, agent, isolation string) error {
	_, err := s.db.Exec("UPDATE tasks SET agent = ?, isolation = ? WHERE id = ?", agent, isolation, id)
	return err
}

// SetTaskWhat records the final prompt used to execute a task. Some task
// types, such as queued agent follow-ups, cannot assemble their complete
// prompt until a dependency has finished.
func (s *Store) SetTaskWhat(id, what string) error {
	_, err := s.db.Exec("UPDATE tasks SET what = ? WHERE id = ?", what, id)
	return err
}

func (s *Store) SetTaskOutput(id, output string) error {
	_, err := s.db.Exec("UPDATE tasks SET output = ? WHERE id = ?", output, id)
	return err
}

// AgentRunOutput reconstructs a live or orphaned transcript from append-only
// events, including chunks received since the last full snapshot.
func (s *Store) AgentRunOutput(id string) (*string, error) {
	var output *string
	err := s.db.QueryRow(`SELECT group_concat(detail, '') FROM
		(SELECT detail FROM task_log WHERE task_id = ? AND event = 'agent_message' ORDER BY id)`, id).Scan(&output)
	return output, err
}

func (s *Store) SetTaskError(id, errMsg string) error {
	return s.SetTaskFailure(id, errMsg, "")
}

func (s *Store) SetTaskFailure(id, errMsg, kind string) error {
	now := time.Now().UTC().Format(timeFmt)
	_, err := s.db.Exec("UPDATE tasks SET error = ?, error_kind = ?, status = 'failed', finished_at = ? WHERE id = ?", errMsg, kind, now, id)
	return err
}

func (s *Store) SetTaskProviderSession(id, threadID, rolloutPath string) error {
	_, err := s.db.Exec("UPDATE tasks SET provider_thread_id = ?, provider_rollout_path = ? WHERE id = ?", threadID, rolloutPath, id)
	return err
}

func (s *Store) ClaimAgentRunSupervisor(id string, launcherPID, supervisorPID int, identity string) error {
	result, err := s.db.Exec(`UPDATE tasks SET runner_pid = ?, runner_identity = ? WHERE id = ? AND type = 'agent_run' AND status = 'pending' AND runner_pid = ?`, supervisorPID, identity, id, launcherPID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("agent run %q is already claimed or terminal", id)
	}
	return nil
}

// Reconciliation must not overwrite a completion racing the liveness probe,
// or confuse losing a supervisor with a known provider failure.
func (s *Store) MarkAgentRunOrphaned(id string, pid int, message string) error {
	_, err := s.db.Exec(`UPDATE tasks SET status = 'orphaned', finished_at = ?,
		output = COALESCE((SELECT group_concat(detail, '') FROM
			(SELECT detail FROM task_log WHERE task_id = ? AND event = 'agent_message' ORDER BY id)), output),
		error = CASE WHEN error IS NULL OR error = '' THEN ? ELSE error || char(10) || ? END
		WHERE id = ? AND type = 'agent_run' AND runner_pid = ? AND status IN ('pending','running')`, time.Now().UTC().Format(timeFmt), id, message, message, id, pid)
	return err
}

func (s *Store) ListRecurring() ([]*Task, error) {
	rows, err := s.db.Query(`SELECT id, type, what, run_at, agent, model, timeout_seconds, isolation, memory, parent_id, status, cron, wing_id,
		created_at, started_at, finished_at, output, error, retry_count, max_retries, depends_on, cwd, prompt_name, prompt_revision, principal, runner_pid, runner_identity, egg_config, error_kind, provider_thread_id, provider_rollout_path
		FROM tasks WHERE cron IS NOT NULL AND cron != '' ORDER BY run_at`)
	if err != nil {
		return nil, fmt.Errorf("list recurring: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

func (s *Store) ClearTaskCron(id string) error {
	_, err := s.db.Exec("UPDATE tasks SET cron = NULL WHERE id = ?", id)
	return err
}

func (s *Store) IncrementRetryCount(taskID string) error {
	_, err := s.db.Exec("UPDATE tasks SET retry_count = retry_count + 1 WHERE id = ?", taskID)
	return err
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var tasks []*Task
	for rows.Next() {
		t := &Task{}
		var runAt, createdAt string
		var startedAt, finishedAt *string
		if err := rows.Scan(&t.ID, &t.Type, &t.What, &runAt, &t.Agent, &t.Model, &t.TimeoutSeconds, &t.Isolation, &t.Memory, &t.ParentID,
			&t.Status, &t.Cron, &t.WingID, &createdAt, &startedAt, &finishedAt, &t.Output, &t.Error, &t.RetryCount, &t.MaxRetries, &t.DependsOn, &t.CWD, &t.PromptName, &t.PromptRevision, &t.Principal, &t.RunnerPID, &t.RunnerIdentity, &t.EggConfigYAML, &t.ErrorKind, &t.ProviderThreadID, &t.ProviderRolloutPath); err != nil {
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
