package taskrun

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestRunTaskPersistsFailureFromEveryEarlyExit(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude", WingID: "test-wing"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	task := &store.Task{
		ID:        "early-failure",
		Type:      "prompt",
		What:      "must not run",
		RunAt:     time.Now(),
		Agent:     "claude",
		Isolation: "privileged",
		CWD:       t.TempDir(),
	}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	err = RunTaskToWithOptions(context.Background(), cfg, taskStore, task, io.Discard, TaskRunOptions{SharedHost: true})
	if err == nil {
		t.Fatal("expected shared-host task to fail closed")
	}
	stored, getErr := taskStore.GetTask(task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored == nil || stored.Status != "failed" || stored.Error == nil || *stored.Error == "" {
		t.Fatalf("failed task state was not persisted: %#v", stored)
	}
}
