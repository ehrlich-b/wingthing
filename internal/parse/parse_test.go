package parse

import (
	"testing"
	"time"
)

func TestScheduleWithDelay(t *testing.T) {
	r := Parse(`Some output <!-- wt:schedule delay=10m -->Check build status<!-- /wt:schedule --> more text`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if r.Schedules[0].Delay != 10*time.Minute {
		t.Errorf("delay = %v, want 10m", r.Schedules[0].Delay)
	}
	if r.Schedules[0].Content != "Check build status" {
		t.Errorf("content = %q, want %q", r.Schedules[0].Content, "Check build status")
	}
	if len(r.Warnings) != 0 {
		t.Errorf("got %d warnings, want 0", len(r.Warnings))
	}
}

func TestScheduleWithAt(t *testing.T) {
	r := Parse(`<!-- wt:schedule at=2026-02-07T08:00:00Z -->Morning briefing<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	want := time.Date(2026, 2, 7, 8, 0, 0, 0, time.UTC)
	if !r.Schedules[0].At.Equal(want) {
		t.Errorf("at = %v, want %v", r.Schedules[0].At, want)
	}
	if r.Schedules[0].Content != "Morning briefing" {
		t.Errorf("content = %q", r.Schedules[0].Content)
	}
}

func TestScheduleDelayExceedsCap(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=48h -->Too far out<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if r.Schedules[0].Delay != 24*time.Hour {
		t.Errorf("delay = %v, want 24h", r.Schedules[0].Delay)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestScheduleMissingAttribute(t *testing.T) {
	r := Parse(`<!-- wt:schedule -->No attrs<!-- /wt:schedule -->`)
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0", len(r.Schedules))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestScheduleInvalidDelay(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=notaduration -->Bad delay<!-- /wt:schedule -->`)
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0", len(r.Schedules))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestScheduleInvalidAt(t *testing.T) {
	r := Parse(`<!-- wt:schedule at=not-a-time -->Bad at<!-- /wt:schedule -->`)
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0", len(r.Schedules))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestScheduleEmptyContent(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=5m -->   <!-- /wt:schedule -->`)
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0", len(r.Schedules))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestMemoryDirective(t *testing.T) {
	r := Parse(`<!-- wt:memory file="research-competitors" -->## Competitor Analysis
- Company A
- Company B<!-- /wt:memory -->`)
	if len(r.Memories) != 1 {
		t.Fatalf("got %d memories, want 1", len(r.Memories))
	}
	if r.Memories[0].File != "research-competitors" {
		t.Errorf("file = %q, want %q", r.Memories[0].File, "research-competitors")
	}
	if r.Memories[0].Content != "## Competitor Analysis\n- Company A\n- Company B" {
		t.Errorf("content = %q", r.Memories[0].Content)
	}
}

func TestMemoryMissingFile(t *testing.T) {
	r := Parse(`<!-- wt:memory -->Some content<!-- /wt:memory -->`)
	if len(r.Memories) != 0 {
		t.Errorf("got %d memories, want 0", len(r.Memories))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestMemoryEmptyContent(t *testing.T) {
	r := Parse(`<!-- wt:memory file="test" -->   <!-- /wt:memory -->`)
	if len(r.Memories) != 0 {
		t.Errorf("got %d memories, want 0", len(r.Memories))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(r.Warnings))
	}
}

func TestMixedValidAndInvalid(t *testing.T) {
	output := `Here is my analysis.
<!-- wt:schedule delay=10m -->Check build<!-- /wt:schedule -->
<!-- wt:schedule delay=badvalue -->Broken one<!-- /wt:schedule -->
<!-- wt:memory file="notes" -->Some notes<!-- /wt:memory -->
<!-- wt:memory -->Missing file attr<!-- /wt:memory -->
Done.`
	r := Parse(output)
	if len(r.Schedules) != 1 {
		t.Errorf("got %d schedules, want 1", len(r.Schedules))
	}
	if len(r.Memories) != 1 {
		t.Errorf("got %d memories, want 1", len(r.Memories))
	}
	if len(r.Warnings) != 2 {
		t.Errorf("got %d warnings, want 2", len(r.Warnings))
	}
}

func TestNestedMarkersIgnoreInner(t *testing.T) {
	// Regex is non-greedy so the inner closing tag matches the first opener.
	// The outer schedule gets the text up to the inner close tag.
	// The "inner" between the two close tags doesn't match anything.
	output := `<!-- wt:schedule delay=5m -->Outer <!-- wt:schedule delay=1m -->Inner<!-- /wt:schedule --> leftover<!-- /wt:schedule -->`
	r := Parse(output)
	// Non-greedy: first match is outer-open to first close. Content = "Outer <!-- wt:schedule delay=1m -->Inner"
	// The " leftover<!-- /wt:schedule -->" leftover has no opening tag, so no second match.
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if r.Schedules[0].Delay != 5*time.Minute {
		t.Errorf("delay = %v, want 5m", r.Schedules[0].Delay)
	}
}

func TestNoMarkers(t *testing.T) {
	r := Parse("Just some plain text with no markers at all.")
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0", len(r.Schedules))
	}
	if len(r.Memories) != 0 {
		t.Errorf("got %d memories, want 0", len(r.Memories))
	}
	if len(r.Warnings) != 0 {
		t.Errorf("got %d warnings, want 0", len(r.Warnings))
	}
}

func TestEmptyInput(t *testing.T) {
	r := Parse("")
	if len(r.Schedules) != 0 || len(r.Memories) != 0 || len(r.Warnings) != 0 {
		t.Errorf("expected empty result for empty input")
	}
}

func TestMultipleValidSchedules(t *testing.T) {
	output := `<!-- wt:schedule delay=5m -->First<!-- /wt:schedule -->
<!-- wt:schedule delay=15m -->Second<!-- /wt:schedule -->
<!-- wt:schedule at=2026-03-01T12:00:00Z -->Third<!-- /wt:schedule -->`
	r := Parse(output)
	if len(r.Schedules) != 3 {
		t.Fatalf("got %d schedules, want 3", len(r.Schedules))
	}
	if r.Schedules[0].Delay != 5*time.Minute {
		t.Errorf("[0] delay = %v", r.Schedules[0].Delay)
	}
	if r.Schedules[1].Delay != 15*time.Minute {
		t.Errorf("[1] delay = %v", r.Schedules[1].Delay)
	}
	if r.Schedules[2].At.IsZero() {
		t.Error("[2] at should not be zero")
	}
}

func TestUnquotedFileAttribute(t *testing.T) {
	r := Parse(`<!-- wt:memory file=myfile -->Content here<!-- /wt:memory -->`)
	if len(r.Memories) != 1 {
		t.Fatalf("got %d memories, want 1", len(r.Memories))
	}
	if r.Memories[0].File != "myfile" {
		t.Errorf("file = %q, want %q", r.Memories[0].File, "myfile")
	}
}

func TestMalformedUnclosedMarker(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=5m -->No closing tag here`)
	if len(r.Schedules) != 0 {
		t.Errorf("got %d schedules, want 0 for unclosed marker", len(r.Schedules))
	}
}

func TestScheduleWithMemorySingle(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=10m memory="deploy-log" -->check deploy<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if len(r.Schedules[0].Memory) != 1 {
		t.Fatalf("got %d memory entries, want 1", len(r.Schedules[0].Memory))
	}
	if r.Schedules[0].Memory[0] != "deploy-log" {
		t.Errorf("memory[0] = %q, want %q", r.Schedules[0].Memory[0], "deploy-log")
	}
}

func TestScheduleWithMemoryMultiple(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=10m memory="deploy-log,projects" -->check deploy<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if len(r.Schedules[0].Memory) != 2 {
		t.Fatalf("got %d memory entries, want 2", len(r.Schedules[0].Memory))
	}
	if r.Schedules[0].Memory[0] != "deploy-log" {
		t.Errorf("memory[0] = %q, want %q", r.Schedules[0].Memory[0], "deploy-log")
	}
	if r.Schedules[0].Memory[1] != "projects" {
		t.Errorf("memory[1] = %q, want %q", r.Schedules[0].Memory[1], "projects")
	}
}

func TestScheduleWithoutMemory(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=5m -->no memory here<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if len(r.Schedules[0].Memory) != 0 {
		t.Errorf("got %d memory entries, want 0", len(r.Schedules[0].Memory))
	}
}

func TestScheduleWithAfter(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=10m after="t-dep-001" -->Check after dep<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if r.Schedules[0].After != "t-dep-001" {
		t.Errorf("after = %q, want %q", r.Schedules[0].After, "t-dep-001")
	}
	if r.Schedules[0].Content != "Check after dep" {
		t.Errorf("content = %q, want %q", r.Schedules[0].Content, "Check after dep")
	}
}

func TestScheduleWithoutAfter(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=5m -->No after<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if r.Schedules[0].After != "" {
		t.Errorf("after = %q, want empty", r.Schedules[0].After)
	}
}

func TestScheduleMemoryWithSpaces(t *testing.T) {
	r := Parse(`<!-- wt:schedule delay=5m memory="deploy-log, projects, notes" -->check stuff<!-- /wt:schedule -->`)
	if len(r.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(r.Schedules))
	}
	if len(r.Schedules[0].Memory) != 3 {
		t.Fatalf("got %d memory entries, want 3", len(r.Schedules[0].Memory))
	}
	if r.Schedules[0].Memory[0] != "deploy-log" {
		t.Errorf("memory[0] = %q, want %q", r.Schedules[0].Memory[0], "deploy-log")
	}
	if r.Schedules[0].Memory[1] != "projects" {
		t.Errorf("memory[1] = %q, want %q", r.Schedules[0].Memory[1], "projects")
	}
	if r.Schedules[0].Memory[2] != "notes" {
		t.Errorf("memory[2] = %q, want %q", r.Schedules[0].Memory[2], "notes")
	}
}
