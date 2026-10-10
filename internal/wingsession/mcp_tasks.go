package wingsession

import (
	"context"
	"errors"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

// MCPTaskRecord is committed in the same admission transaction as the run.
// Retention starts at completion; active runs never expire or lose their timeout.
type MCPTaskRecord struct {
	RetentionMillis *int64             `json:"retention_millis"`
	CancelledAt     time.Time          `json:"cancelled_at,omitempty"`
	CancelledResult *egg.RunTurnResult `json:"cancelled_result,omitempty"`
}

func (r *Run) mcpTask(now time.Time) (control.MCPTask, bool) {
	if r.MCPTask == nil {
		return control.MCPTask{}, false
	}
	updated := r.CreatedAt
	status := "working"
	if !r.Result.StartedAt.IsZero() {
		updated = r.Result.StartedAt
	}
	if r.Result.Terminal() {
		updated = r.Result.EndedAt
		if updated.IsZero() {
			updated = r.CreatedAt
		}
		switch r.Result.Status {
		case "done":
			status = "completed"
		case "stopped":
			status = "cancelled"
		default:
			status = "failed"
		}
	}
	if !r.MCPTask.CancelledAt.IsZero() {
		status = "cancelled"
		updated = r.MCPTask.CancelledAt
	}
	task := control.MCPTask{TaskID: r.ID, Status: status, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano), LastUpdatedAt: updated.UTC().Format(time.RFC3339Nano), PollInterval: 5000}
	if status != "working" && r.MCPTask.RetentionMillis != nil {
		retention := time.Duration(*r.MCPTask.RetentionMillis) * time.Millisecond
		if !now.Before(updated.Add(retention)) {
			return control.MCPTask{}, false
		}
		ttl := max(int64(0), updated.Sub(r.CreatedAt).Milliseconds()) + *r.MCPTask.RetentionMillis
		task.TTL = &ttl
	}
	if status == "failed" {
		task.StatusMessage = r.Result.Error
	}
	return task, true
}

func (m *Runs) Task(a Authority, id string) (control.MCPTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[id]
	if m.owned(a, r) {
		if task, ok := r.mcpTask(time.Now()); ok {
			return task, nil
		}
	}
	return control.MCPTask{}, errors.New("task not found or not owned by caller")
}

func (m *Runs) Tasks(a Authority) []control.MCPTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	tasks := []control.MCPTask{}
	for _, r := range m.records {
		if m.owned(a, r) {
			if task, ok := r.mcpTask(now); ok {
				tasks = append(tasks, task)
			}
		}
	}
	return tasks
}

// WaitTask observes the durable state without imposing a tool wait timeout.
func (m *Runs) WaitTask(ctx context.Context, a Authority, id string) error {
	for {
		m.mu.Lock()
		r := m.records[id]
		if !m.owned(a, r) {
			m.mu.Unlock()
			return errors.New("task not found or not owned by caller")
		}
		task, ok := r.mcpTask(time.Now())
		changed := m.changed
		m.mu.Unlock()
		if !ok {
			return errors.New("task not found or not owned by caller")
		}
		if task.Status != "working" && task.Status != "input_required" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-changed:
		}
	}
}

// CancelTask durably publishes cancellation before responding. The ordinary
// stop path still owns descendant cleanup, independently of the request lifetime.
func (m *Runs) CancelTask(a Authority, id string) (control.MCPTask, error) {
	m.mu.Lock()
	r := m.records[id]
	if !m.owned(a, r) {
		m.mu.Unlock()
		return control.MCPTask{}, errors.New("task not found or not owned by caller")
	}
	task, ok := r.mcpTask(time.Now())
	if !ok || task.Status != "working" && task.Status != "input_required" {
		m.mu.Unlock()
		return control.MCPTask{}, errors.New("task is missing or already terminal")
	}
	r = cloneRun(r)
	// Keep the underlying run nonterminal until the egg acknowledges stopping,
	// so a wing restart will reconcile this durable stop intent. Freeze the
	// client-visible cancelled result before replying, as required by MCP.
	r.Cancelled = true
	r.MCPTask.CancelledAt = time.Now().UTC()
	cancelled := cloneRun(r)
	markRunStopped(cancelled)
	cancelled.Result.EndedAt = r.MCPTask.CancelledAt
	r.MCPTask.CancelledResult = &cancelled.Result
	if err := m.save(r, "mcp_task_cancelled"); err != nil {
		m.mu.Unlock()
		return control.MCPTask{}, err
	}
	// Even a zero-retention cancellation must return its terminal snapshot.
	task, _ = r.mcpTask(r.MCPTask.CancelledAt.Add(-time.Nanosecond))
	m.workers.Add(1)
	m.mu.Unlock()
	go func() { defer m.workers.Done(); _, _ = m.Stop(a, id) }()
	return task, nil
}
