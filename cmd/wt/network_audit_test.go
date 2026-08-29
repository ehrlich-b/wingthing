package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestUnconfinedEgressAuditIsDurable(t *testing.T) {
	taskStore, err := store.Open(filepath.Join(t.TempDir(), "wingthing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, "task store", taskStore)
	task := &store.Task{ID: "unconfined-audit", Type: "prompt", What: "test", RunAt: time.Now(), Agent: "claude"}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	detail, err := appendNetworkEnforcementAudit(taskStore, task.ID, "unconfined_egress", "outer-boundary", sandbox.NetworkFull, []string{"*"}, []int{11434})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := taskStore.ListLogByTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Event != "unconfined_egress" || entries[0].Detail == nil || *entries[0].Detail != detail {
		t.Fatalf("unconfined audit entries = %#v, detail = %q", entries, detail)
	}
	if detail != "network=full enforcement=outer-boundary domains=1 local_ports=[11434]" {
		t.Fatalf("unconfined audit detail = %q", detail)
	}
}
