package egg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellReportOrderingDuplicatesAcknowledgementAndExit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "immutable-shell")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report := func(state string, revision, after int64) SessionReport {
		return SessionReport{Goal: "Goal", Update: "Update", NextAction: "Next", Shell: &ShellReport{SessionID: filepath.Base(dir), State: state, Revision: revision, After: after}}
	}
	record := func(r SessionReport) SessionEvent {
		t.Helper()
		e, err := RecordSessionReport(dir, r)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	reject := func(r SessionReport) {
		t.Helper()
		if _, err := RecordSessionReport(dir, r); err == nil {
			t.Fatalf("accepted %+v", r.Shell)
		}
	}
	view := func(alive bool) SessionView {
		t.Helper()
		v, err := ReadSessionLifecycle(dir, "", "", "", "", alive, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view(true); v.Status != "unknown" {
		t.Fatal(v)
	}
	reject(report("waiting", 1, 0))
	first := record(report("working", 1, 0))
	if v := view(true); v.Status != "working" || v.StateSource != "shell_report" || v.Ready {
		t.Fatal(v)
	}
	waiting := report("waiting", 2, first.Sequence)
	wait := record(waiting)
	if again := record(waiting); again.Sequence != wait.Sequence {
		t.Fatal("duplicate appended")
	}
	if v := view(true); v.Status != "blocked" || v.State != "needs_input" {
		t.Fatal(v)
	}
	wrong := report("continue", 3, wait.Sequence)
	wrong.Shell.SessionID = "other-shell"
	reject(wrong)
	reject(report("continue", 3, first.Sequence))
	reject(report("working", 3, wait.Sequence))
	reject(report("waiting", 1, 0))
	continued := record(report("continue", 3, wait.Sequence))
	if v := view(true); v.Status != "working" {
		t.Fatal(v)
	}
	record(report("finished", 4, continued.Sequence))
	if v := view(true); v.Status != "done" {
		t.Fatal(v)
	}
	reject(report("working", 5, 4))
	if err := recordSessionProcessExit(dir, 0, false); err != nil {
		t.Fatal(err)
	}
	if v := view(false); v.Status != "done" || v.ProcessAlive {
		t.Fatal(v)
	}
	reject(report("finished", 4, continued.Sequence))
}

func TestShellReportProcessExitClearsWaitAndUninstrumentedUnknown(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=shell\n"), 0600)
		for i, state := range []string{"working", "waiting"} {
			_, err := RecordSessionReport(dir, SessionReport{Goal: "g", Update: "u", NextAction: "n", Shell: &ShellReport{SessionID: filepath.Base(dir), State: state, Revision: int64(i + 1), After: int64(i)}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := recordSessionProcessExit(dir, exitCode, false); err != nil {
			t.Fatal(err)
		}
		v, err := ReadSessionLifecycle(dir, "shell", "", "", "", false, 0, 200)
		if err != nil || v.Status != "exited" || v.Ready {
			t.Fatalf("%+v %v", v, err)
		}
	}
	dir := t.TempDir()
	for _, alive := range []bool{true, false} {
		v, err := ReadSessionLifecycle(dir, "", "", "", "", alive, 0, 200)
		if err != nil || v.Status != "unknown" {
			t.Fatalf("%+v %v", v, err)
		}
	}
}
