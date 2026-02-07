package sync

import (
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func strPtr(s string) *string { return &s }

func createTestTask(t *testing.T, s *store.Store, id string) {
	t.Helper()
	s.CreateTask(&store.Task{
		ID: id, Type: "prompt", What: "test", RunAt: time.Now().UTC(), Agent: "claude",
	})
}

func TestMergeThreadEntries_BasicMerge(t *testing.T) {
	s := openTestStore(t)
	eng := &Engine{Store: s, MachineID: "mac"}

	ts1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)
	taskID := "task-001"
	createTestTask(t, s, taskID)

	// Insert a local entry from mac
	s.AppendThreadAt(&store.ThreadEntry{
		TaskID: &taskID, MachineID: "mac", Summary: "local work",
	}, ts1)

	// Merge remote entries from wsl
	remote := []*store.ThreadEntry{
		{TaskID: &taskID, MachineID: "wsl", Summary: "remote work A", Timestamp: ts1},
		{TaskID: &taskID, MachineID: "wsl", Summary: "remote work B", Timestamp: ts2},
	}

	imported, err := eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if imported != 2 {
		t.Errorf("imported = %d, want 2", imported)
	}

	// Should have 3 total entries (1 local + 2 remote)
	entries, err := s.ListRecentThread(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("total entries = %d, want 3", len(entries))
	}
}

func TestMergeThreadEntries_Dedup(t *testing.T) {
	s := openTestStore(t)
	eng := &Engine{Store: s, MachineID: "mac"}

	ts := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	taskID := "task-001"
	createTestTask(t, s, taskID)

	remote := []*store.ThreadEntry{
		{TaskID: &taskID, MachineID: "wsl", Summary: "remote work", Timestamp: ts},
	}

	// First merge
	imported, err := eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if imported != 1 {
		t.Errorf("first imported = %d, want 1", imported)
	}

	// Second merge with same entries — should skip all
	imported, err = eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if imported != 0 {
		t.Errorf("second imported = %d, want 0", imported)
	}

	entries, err := s.ListRecentThread(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("total entries = %d, want 1", len(entries))
	}
}

func TestMergeThreadEntries_NilTaskID(t *testing.T) {
	s := openTestStore(t)
	eng := &Engine{Store: s, MachineID: "mac"}

	ts := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)

	remote := []*store.ThreadEntry{
		{MachineID: "wsl", Summary: "ad-hoc task", Timestamp: ts, Agent: strPtr("claude")},
	}

	// First merge
	imported, err := eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if imported != 1 {
		t.Errorf("first imported = %d, want 1", imported)
	}

	// Same entry again — dedup by summary
	imported, err = eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if imported != 0 {
		t.Errorf("second imported = %d, want 0 (dedup by summary)", imported)
	}

	// Different summary at same timestamp — should import
	remote2 := []*store.ThreadEntry{
		{MachineID: "wsl", Summary: "different ad-hoc", Timestamp: ts, Agent: strPtr("claude")},
	}
	imported, err = eng.MergeThreadEntries(remote2)
	if err != nil {
		t.Fatalf("third merge: %v", err)
	}
	if imported != 1 {
		t.Errorf("third imported = %d, want 1", imported)
	}

	entries, err := s.ListRecentThread(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("total entries = %d, want 2", len(entries))
	}
}

func TestMergeThreadEntries_Ordering(t *testing.T) {
	s := openTestStore(t)
	eng := &Engine{Store: s, MachineID: "mac"}

	ts1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)
	ts3 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	// Insert a local entry in the middle
	s.AppendThreadAt(&store.ThreadEntry{
		MachineID: "mac", Summary: "local middle", Agent: strPtr("claude"),
	}, ts2)

	// Merge remote entries before and after
	remote := []*store.ThreadEntry{
		{MachineID: "wsl", Summary: "remote early", Timestamp: ts1, Agent: strPtr("claude")},
		{MachineID: "wsl", Summary: "remote late", Timestamp: ts3, Agent: strPtr("claude")},
	}

	_, err := eng.MergeThreadEntries(remote)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	// List by date — should be in timestamp order
	entries, err := s.ListThreadByDate(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("total entries = %d, want 3", len(entries))
	}

	if entries[0].Summary != "remote early" {
		t.Errorf("entries[0] = %q, want 'remote early'", entries[0].Summary)
	}
	if entries[1].Summary != "local middle" {
		t.Errorf("entries[1] = %q, want 'local middle'", entries[1].Summary)
	}
	if entries[2].Summary != "remote late" {
		t.Errorf("entries[2] = %q, want 'remote late'", entries[2].Summary)
	}
}

func TestMergeThreadEntries_EmptyRemote(t *testing.T) {
	s := openTestStore(t)
	eng := &Engine{Store: s, MachineID: "mac"}

	imported, err := eng.MergeThreadEntries(nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if imported != 0 {
		t.Errorf("imported = %d, want 0", imported)
	}

	imported, err = eng.MergeThreadEntries([]*store.ThreadEntry{})
	if err != nil {
		t.Fatalf("merge empty slice: %v", err)
	}
	if imported != 0 {
		t.Errorf("imported = %d, want 0", imported)
	}
}
