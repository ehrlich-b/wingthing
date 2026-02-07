package timeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/store"
)

const maxRetryBackoff = 5 * time.Minute

// Engine drives the task execution loop.
type Engine struct {
	Store        *store.Store
	Builder      *orchestrator.Builder
	Config       *config.Config
	Agents       map[string]agent.Agent
	PollInterval time.Duration
	MemoryDir    string
}

// Run polls for pending tasks and dispatches them until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if e.PollInterval == 0 {
		e.PollInterval = time.Second
	}

	ticker := time.NewTicker(e.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.poll(ctx); err != nil {
				log.Printf("poll error: %v", err)
			}
		}
	}
}

func (e *Engine) poll(ctx context.Context) error {
	tasks, err := e.Store.ListPending(time.Now())
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	if len(tasks) == 0 {
		return nil
	}

	// Take the first (oldest run_at)
	task := tasks[0]

	if err := e.Store.UpdateTaskStatus(task.ID, "running"); err != nil {
		return fmt.Errorf("set running: %w", err)
	}
	e.Store.AppendLog(task.ID, "started", nil)

	if err := e.dispatch(ctx, task); err != nil {
		errMsg := err.Error()
		e.Store.SetTaskError(task.ID, errMsg)
		e.Store.AppendLog(task.ID, "failed", &errMsg)
		_, _ = fmt.Fprintf(os.Stderr, "task %s failed: %v\n", task.ID, err)

		if task.RetryCount < task.MaxRetries {
			e.scheduleRetry(task)
		}
	}

	return nil
}

// RetryBackoff computes exponential backoff capped at maxRetryBackoff.
func RetryBackoff(retryCount int) time.Duration {
	d := time.Duration(1<<uint(retryCount)) * time.Second
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	return d
}

func (e *Engine) scheduleRetry(task *store.Task) {
	backoff := RetryBackoff(task.RetryCount)
	now := time.Now()
	retryTask := &store.Task{
		ID:         fmt.Sprintf("%s-r%d", task.ID, task.RetryCount+1),
		Type:       task.Type,
		What:       task.What,
		RunAt:      now.Add(backoff),
		Agent:      task.Agent,
		Isolation:  task.Isolation,
		Memory:     task.Memory,
		Cron:       task.Cron,
		ParentID:   &task.ID,
		Status:     "pending",
		RetryCount: task.RetryCount + 1,
		MaxRetries: task.MaxRetries,
	}
	if err := e.Store.CreateTask(retryTask); err != nil {
		msg := fmt.Sprintf("schedule retry: %v", err)
		e.Store.AppendLog(task.ID, "retry_error", &msg)
		return
	}
	detail := fmt.Sprintf("retry %d/%d in %s", retryTask.RetryCount, retryTask.MaxRetries, backoff)
	e.Store.AppendLog(task.ID, "retry_scheduled", &detail)
}
