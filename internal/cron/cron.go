package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule represents a parsed cron expression.
type Schedule struct {
	Minute     []int
	Hour       []int
	DayOfMonth []int
	Month      []int
	DayOfWeek  []int
}

// Parse validates and parses a standard 5-field cron expression:
// minute hour day-of-month month day-of-week
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}

	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron: minute: %w", err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron: hour: %w", err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron: day-of-month: %w", err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron: month: %w", err)
	}
	dow, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("cron: day-of-week: %w", err)
	}

	return &Schedule{
		Minute:     minute,
		Hour:       hour,
		DayOfMonth: dom,
		Month:      month,
		DayOfWeek:  dow,
	}, nil
}

// Next returns the next fire time strictly after from.
func (s *Schedule) Next(from time.Time) time.Time {
	// Start from the next minute boundary.
	t := from.Truncate(time.Minute).Add(time.Minute)

	// Safety: don't search more than 4 years out.
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !contains(s.Month, int(t.Month())) {
			// Advance to first day of next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !contains(s.DayOfMonth, t.Day()) || !contains(s.DayOfWeek, int(t.Weekday())) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !contains(s.Hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !contains(s.Minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}

	// Should not happen with valid schedules, but return zero if we exhaust the search.
	return time.Time{}
}

func contains(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// parseField parses a single cron field with support for *, ranges, steps, and lists.
func parseField(field string, min, max int) ([]int, error) {
	var result []int
	seen := make(map[int]bool)

	parts := strings.Split(field, ",")
	for _, part := range parts {
		vals, err := parsePart(part, min, max)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				result = append(result, v)
			}
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("empty field")
	}

	// Sort for deterministic iteration.
	sortInts(result)
	return result, nil
}

func parsePart(part string, min, max int) ([]int, error) {
	// Check for step: */N or range/N
	var step int
	if idx := strings.Index(part, "/"); idx >= 0 {
		s, err := strconv.Atoi(part[idx+1:])
		if err != nil || s <= 0 {
			return nil, fmt.Errorf("invalid step %q", part[idx+1:])
		}
		step = s
		part = part[:idx]
	}

	var low, high int
	if part == "*" {
		low, high = min, max
	} else if idx := strings.Index(part, "-"); idx >= 0 {
		var err error
		low, err = strconv.Atoi(part[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid range start %q", part[:idx])
		}
		high, err = strconv.Atoi(part[idx+1:])
		if err != nil {
			return nil, fmt.Errorf("invalid range end %q", part[idx+1:])
		}
	} else {
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q", part)
		}
		if step > 0 {
			low, high = v, max
		} else {
			if v < min || v > max {
				return nil, fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
			}
			return []int{v}, nil
		}
	}

	if low < min || high > max || low > high {
		return nil, fmt.Errorf("range %d-%d out of bounds [%d, %d]", low, high, min, max)
	}

	if step == 0 {
		step = 1
	}

	var vals []int
	for i := low; i <= high; i += step {
		vals = append(vals, i)
	}
	return vals, nil
}

func sortInts(a []int) {
	// Simple insertion sort — fields are small.
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j] > key {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}
