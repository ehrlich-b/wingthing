package wingsession

import (
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestMCPTaskRetentionNeverExpiresAnActiveRun(t *testing.T) {
	created := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	retention := int64(1000)
	r := &Run{ID: "run", CreatedAt: created, MCPTask: &MCPTaskRecord{RetentionMillis: &retention}, Result: egg.RunTurnResult{Status: "running", StartedAt: created.Add(time.Second)}}
	if task, ok := r.mcpTask(created.Add(24 * time.Hour)); !ok || task.Status != "working" || task.TTL != nil {
		t.Fatalf("active task: %+v %v", task, ok)
	}
	r.Result.Status = "done"
	r.Result.EndedAt = created.Add(time.Hour)
	task, ok := r.mcpTask(r.Result.EndedAt)
	if !ok || task.Status != "completed" || task.TTL == nil || *task.TTL != 3601000 {
		t.Fatalf("completed task: %+v %v", task, ok)
	}
	if _, ok := r.mcpTask(r.Result.EndedAt.Add(time.Second)); ok {
		t.Fatal("expired task retained")
	}
	if r.Result.Status != "done" {
		t.Fatal("expiration altered durable result")
	}
}

func TestMCPTaskStatusAndCancellationRemainTerminal(t *testing.T) {
	created := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	for status, want := range map[string]string{"pending": "working", "running": "working", "done": "completed", "failed": "failed", "timeout": "failed", "stopped": "cancelled"} {
		r := &Run{ID: "run", CreatedAt: created, MCPTask: &MCPTaskRecord{}, Result: egg.RunTurnResult{Status: status, EndedAt: created.Add(time.Second)}}
		task, ok := r.mcpTask(created)
		if !ok || task.Status != want {
			t.Fatalf("%s: %+v %v", status, task, ok)
		}
		r.MCPTask.CancelledAt = created.Add(2 * time.Second)
		task, _ = r.mcpTask(created)
		if task.Status != "cancelled" || task.LastUpdatedAt != r.MCPTask.CancelledAt.Format(time.RFC3339Nano) {
			t.Fatalf("cancellation changed: %+v", task)
		}
	}
}
