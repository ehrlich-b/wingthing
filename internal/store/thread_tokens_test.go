package store

import (
	"testing"
	"time"
)

func TestSumTokensByDateRange(t *testing.T) {
	s := openTestStore(t)

	day := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	tokens100 := 100
	tokens250 := 250

	s.AppendThreadAt(&ThreadEntry{MachineID: "test", Summary: "a", TokensUsed: &tokens100}, day.Add(2*time.Hour))
	s.AppendThreadAt(&ThreadEntry{MachineID: "test", Summary: "b", TokensUsed: &tokens250}, day.Add(4*time.Hour))
	s.AppendThreadAt(&ThreadEntry{MachineID: "test", Summary: "c"}, day.Add(6*time.Hour)) // nil tokens

	start := day
	end := day.AddDate(0, 0, 1)
	total, err := s.SumTokensByDateRange(start, end)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total != 350 {
		t.Errorf("total = %d, want 350", total)
	}
}

func TestSumTokensByDateRangeEmpty(t *testing.T) {
	s := openTestStore(t)

	start := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	total, err := s.SumTokensByDateRange(start, end)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestSumTokensByDateRangeRespectsRange(t *testing.T) {
	s := openTestStore(t)

	day1 := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 2, 6, 12, 0, 0, 0, time.UTC)
	tokens100 := 100
	tokens200 := 200

	s.AppendThreadAt(&ThreadEntry{MachineID: "test", Summary: "day1", TokensUsed: &tokens100}, day1)
	s.AppendThreadAt(&ThreadEntry{MachineID: "test", Summary: "day2", TokensUsed: &tokens200}, day2)

	start := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC)
	total, err := s.SumTokensByDateRange(start, end)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if total != 100 {
		t.Errorf("total = %d, want 100 (only day1)", total)
	}
}
