package cron

import (
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		expr string
	}{
		{"* * * * *"},
		{"0 8 * * 1-5"},
		{"*/5 * * * *"},
		{"0 0 1 * *"},
		{"30 4 1,15 * *"},
		{"0 0 * * 0"},
		{"0 12 * 1-6 1,3,5"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.expr)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", tt.expr, err)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		expr string
	}{
		{""},
		{"* * *"},
		{"* * * * * *"},
		{"60 * * * *"},
		{"* 24 * * *"},
		{"* * 0 * *"},
		{"* * * 13 *"},
		{"* * * * 7"},
		{"abc * * * *"},
		{"*/0 * * * *"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.expr)
		if err == nil {
			t.Errorf("Parse(%q) expected error", tt.expr)
		}
	}
}

func TestParseFields(t *testing.T) {
	s, err := Parse("0 8 * * 1-5")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Minute) != 1 || s.Minute[0] != 0 {
		t.Errorf("minute = %v, want [0]", s.Minute)
	}
	if len(s.Hour) != 1 || s.Hour[0] != 8 {
		t.Errorf("hour = %v, want [8]", s.Hour)
	}
	if len(s.DayOfWeek) != 5 {
		t.Errorf("dow = %v, want 5 entries", s.DayOfWeek)
	}
}

func TestNextEveryFiveMinutes(t *testing.T) {
	s, _ := Parse("*/5 * * * *")
	from := time.Date(2026, 2, 7, 10, 3, 0, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 2, 7, 10, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextWeekdays8am(t *testing.T) {
	s, _ := Parse("0 8 * * 1-5")
	// Saturday Feb 7 2026
	from := time.Date(2026, 2, 7, 9, 0, 0, 0, time.UTC)
	next := s.Next(from)
	// Next weekday 8am is Monday Feb 9
	want := time.Date(2026, 2, 9, 8, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextMonthly(t *testing.T) {
	s, _ := Parse("0 0 1 * *")
	from := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextExactBoundary(t *testing.T) {
	s, _ := Parse("30 10 * * *")
	// Exactly at 10:30 — should return the NEXT 10:30, not this one.
	from := time.Date(2026, 2, 7, 10, 30, 0, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 2, 8, 10, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextStepMinute(t *testing.T) {
	s, _ := Parse("*/15 * * * *")
	from := time.Date(2026, 2, 7, 10, 14, 30, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 2, 7, 10, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextListValues(t *testing.T) {
	s, _ := Parse("0 9,17 * * *")
	from := time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 2, 7, 17, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestNextEveryMinute(t *testing.T) {
	s, _ := Parse("* * * * *")
	from := time.Date(2026, 2, 7, 10, 30, 45, 0, time.UTC)
	next := s.Next(from)
	want := time.Date(2026, 2, 7, 10, 31, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}
