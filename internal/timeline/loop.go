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
	}

	return nil
}
