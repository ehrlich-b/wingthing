package timeline

import (
	"context"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		retryCount int
		want       time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{8, 256 * time.Second},
		{9, 5 * time.Minute}, // 512s > 5min, capped
		{20, 5 * time.Minute},
	}
	for _, tt := range tests {
		got := RetryBackoff(tt.retryCount)
		if got != tt.want {
			t.Errorf("RetryBackoff(%d) = %v, want %v", tt.retryCount, got, tt.want)
		}
	}
}

func TestRetryScheduledOnFailure(t *testing.T) {
	ag := &mockAgent{
		output:    "",
		healthErr: nil,
	}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:         "t-retry-001",
		Type:       "prompt",
		What:       "flaky task",
		RunAt:      time.Now().Add(-time.Second),
		Agent:      "test",
		Isolation:  "standard",
		Status:     "pending",
		MaxRetries: 3,
		RetryCount: 0,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	eng.poll(context.Background())

	// Original should be failed
	got, _ := s.GetTask("t-retry-001")
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}

	// Retry task should exist
	retry, _ := s.GetTask("t-retry-001-r1")
	if retry == nil {
		t.Fatal("expected retry task t-retry-001-r1 to be created")
	}
	if retry.Status != "pending" {
		t.Errorf("retry status = %q, want pending", retry.Status)
	}
	if retry.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", retry.RetryCount)
	}
	if retry.MaxRetries != 3 {
		t.Errorf("max_retries = %d, want 3", retry.MaxRetries)
	}
	if retry.ParentID == nil || *retry.ParentID != "t-retry-001" {
		t.Errorf("parent_id = %v, want t-retry-001", retry.ParentID)
	}
	if retry.What != "flaky task" {
		t.Errorf("what = %q, want 'flaky task'", retry.What)
	}

	// Check log event
	logs, _ := s.ListLogByTask("t-retry-001")
	found := false
	for _, l := range logs {
		if l.Event == "retry_scheduled" {
			found = true
		}
	}
	if !found {
		t.Error("missing retry_scheduled log event")
	}
}

func TestRetryExhausted(t *testing.T) {
	ag := &mockAgent{output: ""}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:         "t-retry-002",
		Type:       "prompt",
		What:       "always fails",
		RunAt:      time.Now().Add(-time.Second),
		Agent:      "test",
		Isolation:  "standard",
		Status:     "pending",
		MaxRetries: 2,
		RetryCount: 2, // already at max
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	eng.poll(context.Background())

	got, _ := s.GetTask("t-retry-002")
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}

	// No retry should be created
	retry, _ := s.GetTask("t-retry-002-r3")
	if retry != nil {
		t.Error("expected no retry task when retries exhausted")
	}
}

func TestRetryCountIncrement(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	task := &store.Task{
		ID:     "t-inc-001",
		Type:   "prompt",
		What:   "test",
		RunAt:  time.Now(),
		Agent:  "claude",
		Status: "pending",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := s.IncrementRetryCount("t-inc-001"); err != nil {
		t.Fatalf("increment: %v", err)
	}

	got, _ := s.GetTask("t-inc-001")
	if got.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", got.RetryCount)
	}

	s.IncrementRetryCount("t-inc-001")
	got, _ = s.GetTask("t-inc-001")
	if got.RetryCount != 2 {
		t.Errorf("retry_count = %d, want 2", got.RetryCount)
	}
}
