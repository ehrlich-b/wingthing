package thread

import (
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/store"
)

func strPtr(s string) *string { return &s }

func sampleEntries() []*store.ThreadEntry {
	ts := time.Date(2026, 2, 6, 8, 2, 0, 0, time.UTC)
	return []*store.ThreadEntry{
		{
			ID:        1,
			Timestamp: ts,
			WingID: "mac",
			Agent:     strPtr("claude"),
			Skill:     strPtr("jira-briefing"),
			UserInput: nil,
			Summary:   "Sprint SLIDE-4521: rate engine migration in progress",
		},
		{
			ID:        2,
			Timestamp: ts.Add(73 * time.Minute),
			WingID: "mac",
			Agent:     strPtr("claude"),
			Skill:     nil,
			UserInput: strPtr("has sarah reviewed my PR yet?"),
			Summary:   "PR #892: Sarah approved 12 minutes ago",
		},
		{
			ID:        3,
			Timestamp: ts.Add(105 * time.Minute),
			WingID: "mac",
			Agent:     strPtr("claude"),
			Skill:     strPtr("scheduled"),
			UserInput: nil,
			Summary:   "PR #892 merged to main",
		},
	}
}

func TestRenderBasic(t *testing.T) {
	entries := sampleEntries()
	got := Render(entries)

	// Check header for first entry
	if !strings.Contains(got, "## 08:02 — Sprint SLIDE-4521: rate engine migration in progress [claude, jira-briefing]") {
		t.Errorf("missing first entry header in:\n%s", got)
	}
	// First entry has no user input
	if strings.Contains(got, "> User: \"\"") {
		t.Error("should not render empty user input")
	}
	// Second entry has user input
	if !strings.Contains(got, "> User: \"has sarah reviewed my PR yet?\"") {
		t.Errorf("missing user input line in:\n%s", got)
	}
	// Second entry should show ad-hoc (no skill)
	if !strings.Contains(got, "[claude, ad-hoc]") {
		t.Errorf("missing ad-hoc marker in:\n%s", got)
	}
	// Third entry has a skill
	if !strings.Contains(got, "[claude, scheduled]") {
		t.Errorf("missing scheduled skill in:\n%s", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	got := Render(nil)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestRenderNoUserInput(t *testing.T) {
	entries := []*store.ThreadEntry{
		{
			ID:        1,
			Timestamp: time.Date(2026, 2, 6, 10, 0, 0, 0, time.UTC),
			WingID: "mac",
			Agent:     strPtr("claude"),
			Skill:     strPtr("cron-check"),
			Summary:   "All systems healthy",
		},
	}
	got := Render(entries)
	if strings.Contains(got, "> User:") {
		t.Errorf("should not have user input line in:\n%s", got)
	}
	if !strings.Contains(got, "- All systems healthy") {
		t.Errorf("missing summary line in:\n%s", got)
	}
}

func TestRenderNoSkill(t *testing.T) {
	entries := []*store.ThreadEntry{
		{
			ID:        1,
			Timestamp: time.Date(2026, 2, 6, 14, 0, 0, 0, time.UTC),
			WingID: "mac",
			Agent:     strPtr("claude"),
			Skill:     nil,
			UserInput: strPtr("what time is it"),
			Summary:   "It is 2pm",
		},
	}
	got := Render(entries)
	if !strings.Contains(got, "[claude, ad-hoc]") {
		t.Errorf("expected ad-hoc for nil skill in:\n%s", got)
	}
}

func TestBudgetFitsAll(t *testing.T) {
	entries := sampleEntries()
	full := Render(entries)
	got := RenderWithBudget(entries, len(full)+100)
	if got != full {
		t.Error("expected full render when budget is large enough")
	}
}

func TestBudgetDropsOldest(t *testing.T) {
	entries := sampleEntries()
	full := Render(entries)

	// Set budget smaller than full but large enough for 2 entries
	twoEntries := Render(entries[1:])
	budget := len(twoEntries) + 1
	if budget >= len(full) {
		t.Skip("test entries too small to demonstrate truncation")
	}

	got := RenderWithBudget(entries, budget)
	// Should have dropped the oldest (first) entry
	if strings.Contains(got, "jira-briefing") {
		t.Error("expected oldest entry (jira-briefing) to be dropped")
	}
	// Should still have the newer entries
	if !strings.Contains(got, "PR #892: Sarah approved") {
		t.Error("expected second entry to remain")
	}
}

func TestBudgetDropsAllButNewest(t *testing.T) {
	entries := sampleEntries()

	// Budget only fits one entry
	oneEntry := Render(entries[2:])
	budget := len(oneEntry) + 1

	got := RenderWithBudget(entries, budget)
	if strings.Contains(got, "jira-briefing") {
		t.Error("expected oldest entry to be dropped")
	}
	if strings.Contains(got, "Sarah approved") {
		t.Error("expected middle entry to be dropped")
	}
	if !strings.Contains(got, "PR #892 merged") {
		t.Error("expected newest entry to remain")
	}
}

func TestBudgetZeroRendersAll(t *testing.T) {
	entries := sampleEntries()
	full := Render(entries)
	got := RenderWithBudget(entries, 0)
	if got != full {
		t.Error("budget=0 should render all entries")
	}
}

func TestBudgetEmpty(t *testing.T) {
	got := RenderWithBudget(nil, 100)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestRenderDay(t *testing.T) {
	s := openTestStore(t)
	date := time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC)

	ts1 := time.Date(2026, 2, 6, 8, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 2, 6, 9, 15, 0, 0, time.UTC)

	s.AppendThreadAt(&store.ThreadEntry{
		WingID: "mac",
		Agent:     strPtr("claude"),
		Skill:     strPtr("jira"),
		Summary:   "Morning briefing",
	}, ts1)
	s.AppendThreadAt(&store.ThreadEntry{
		WingID: "mac",
		Agent:     strPtr("claude"),
		UserInput: strPtr("check PR"),
		Summary:   "PR is approved",
	}, ts2)

	got, err := RenderDay(s, date, 0)
	if err != nil {
		t.Fatalf("RenderDay: %v", err)
	}
	if !strings.Contains(got, "Morning briefing") {
		t.Errorf("missing first entry in:\n%s", got)
	}
	if !strings.Contains(got, "PR is approved") {
		t.Errorf("missing second entry in:\n%s", got)
	}
}

func TestRenderDayEmpty(t *testing.T) {
	s := openTestStore(t)
	date := time.Date(2026, 2, 6, 0, 0, 0, 0, time.UTC)

	got, err := RenderDay(s, date, 0)
	if err != nil {
		t.Fatalf("RenderDay: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for empty day, got %q", got)
	}
}

func TestRender_SingleWing(t *testing.T) {
	entries := []*store.ThreadEntry{
		{
			ID: 1, Timestamp: time.Date(2026, 2, 6, 8, 0, 0, 0, time.UTC),
			WingID: "mac", Agent: strPtr("claude"), Skill: strPtr("jira"),
			Summary: "Morning briefing",
		},
		{
			ID: 2, Timestamp: time.Date(2026, 2, 6, 9, 0, 0, 0, time.UTC),
			WingID: "mac", Agent: strPtr("claude"), Summary: "PR review",
		},
	}
	got := Render(entries)
	// Single machine — should NOT show wing_id
	if strings.Contains(got, "mac]") {
		t.Errorf("single wing should not show wing_id in:\n%s", got)
	}
	if !strings.Contains(got, "[claude, jira]") {
		t.Errorf("missing expected header format in:\n%s", got)
	}
}

func TestRender_MultiWing(t *testing.T) {
	entries := []*store.ThreadEntry{
		{
			ID: 1, Timestamp: time.Date(2026, 2, 6, 8, 0, 0, 0, time.UTC),
			WingID: "mac", Agent: strPtr("claude"), Skill: strPtr("jira"),
			Summary: "Morning briefing",
		},
		{
			ID: 2, Timestamp: time.Date(2026, 2, 6, 8, 30, 0, 0, time.UTC),
			WingID: "wsl", Agent: strPtr("claude"), Skill: strPtr("build"),
			Summary: "GPU compile job",
		},
		{
			ID: 3, Timestamp: time.Date(2026, 2, 6, 9, 0, 0, 0, time.UTC),
			WingID: "mac", Agent: strPtr("claude"), Summary: "PR review",
		},
	}
	got := Render(entries)
	// Multi wing — should show wing_id for all entries
	if !strings.Contains(got, "[claude, jira, mac]") {
		t.Errorf("missing mac wing_id in header:\n%s", got)
	}
	if !strings.Contains(got, "[claude, build, wsl]") {
		t.Errorf("missing wsl wing_id in header:\n%s", got)
	}
	if !strings.Contains(got, "[claude, ad-hoc, mac]") {
		t.Errorf("missing ad-hoc with wing_id in header:\n%s", got)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
