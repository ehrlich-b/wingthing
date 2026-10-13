package egg

import (
	"strings"
	"testing"
)

func TestSessionReportDurableReplayDoesNotChangeNativeLifecycle(t *testing.T) {
	dir := t.TempDir()
	report := SessionReport{Goal: "Finish the change", Update: "Tests passed", NextAction: "Review results", Presence: "asleep", LastWake: "2026-10-12T22:00:00Z", NextWake: "2026-10-12T23:00:00Z", Artifacts: []ReportArtifact{{Label: "Results", Path: "results.md"}}, Links: []ReportLink{{Label: "Docs", URL: "https://example.com/docs"}}}
	first, err := RecordSessionReport(dir, report)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionProcessEvent(dir, "session_exit", "stopped", "explicit stop"); err != nil {
		t.Fatal(err)
	}
	view, err := ReadSessionLifecycle(dir, "codex", "", "", "", false, first.Sequence, 1)
	if err != nil {
		t.Fatal(err)
	}
	if view.Report == nil || view.Report.Goal != report.Goal || view.ReportedAt != first.Timestamp || view.ReportCursor != first.Sequence || view.Status != "unknown" || view.ProcessAlive || view.Ready {
		t.Fatalf("view=%+v", view)
	}
	if len(view.Events) != 1 || view.Events[0].Type != "session_exit" {
		t.Fatalf("replay=%+v", view.Events)
	}
	again, err := ReadSessionLifecycle(dir, "codex", "", "", "", false, 0, 1)
	if err != nil || len(again.Events) != 1 || again.Events[0].Report == nil || again.Events[0].State != "" || !again.HasMore {
		t.Fatalf("first page=%+v %v", again, err)
	}
}

func TestSessionReportValidation(t *testing.T) {
	valid := SessionReport{Goal: "Goal", Update: "Update", NextAction: "Next"}
	for _, mutate := range []func(*SessionReport){
		func(r *SessionReport) { r.Goal = " " },
		func(r *SessionReport) { r.Update = strings.Repeat("x", 4097) },
		func(r *SessionReport) { r.NextAction = "a\x00b" },
		func(r *SessionReport) { r.Presence = "working" },
		func(r *SessionReport) { r.LastWake = "tomorrow" },
		func(r *SessionReport) { r.LastWake = "2026-10-13T02:00:00Z"; r.NextWake = "2026-10-13T01:00:00Z" },
		func(r *SessionReport) { r.Links = []ReportLink{{Label: "bad", URL: "javascript:alert(1)"}} },
		func(r *SessionReport) {
			r.Links = []ReportLink{{Label: "bad", URL: "https://user:password@example.com"}}
		},
		func(r *SessionReport) { r.Artifacts = make([]ReportArtifact, 9) },
	} {
		r := valid
		mutate(&r)
		if _, err := RecordSessionReport(t.TempDir(), r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
