package runinspect

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunInspectReviewCaseAliasedManifest(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "run", "execution", "", "terminal", "done", str("output"))
	})
	file := filepath.Join(path, ManifestName)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"completed": true`), []byte(`"completed": false,"Completed": true`), 1)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := fingerprints(t, path)
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err == nil || report != nil {
		t.Fatal("incomplete, case-aliased manifest establishes completion")
	}
	if !reflect.DeepEqual(before, fingerprints(t, path)) {
		t.Fatal("refusal changed evidence")
	}
}

func TestRunInspectReviewCaseAliasedReceipt(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addEgg(t, path, "run", "execution", "done", str("output"))
		file := filepath.Join(path, outputPath("execution", "run"))
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(`"version":1`), []byte(`"version":99,"Version":1`), 1)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range report.Runs {
		if run.Diagnosis == "completed_with_output" {
			t.Fatal("ambiguous unsupported receipt establishes completion")
		}
	}
	if len(report.Issues) == 0 {
		t.Fatal("ambiguous evidence lacks an issue")
	}
}

func TestRunInspectReviewArchiveEggBinding(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "run", "execution", "", "terminal", "timeout", str("partial"))
		addEgg(t, path, "run", "execution", "timeout", str("partial"))
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','intent',1,1)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		sqlExec(t, db, "INSERT INTO conversation_events VALUES (1,'logical','logical','execution')")
		addArchive(t, path, "execution", "native-thread")
		if err := os.WriteFile(filepath.Join(path, "eggs", "execution", "egg.meta"), []byte("agent=claude\nprovider_session_id=foreign-thread\ncwd=/fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if report.Archives[0].State == "complete_bound_archive" || hasAction(report.Runs[0], "manual_resume_eligible") {
		t.Fatal("contradictory egg binding recommends resume")
	}
}

func TestRunInspectReviewHumanLegacyIssues(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "CREATE TABLE tasks(id TEXT,type TEXT,status TEXT,parent_id TEXT,principal TEXT,output TEXT)")
		sqlExec(t, db, "INSERT INTO tasks VALUES ('child','agent_run','done','missing','owner','output')")
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.LegacyTasks) != 1 || len(report.LegacyTasks[0].Issues) != 1 {
		t.Fatal("missing legacy issue")
	}
	var text bytes.Buffer
	if err := RenderText(&text, report); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"missing_parent", `"missing"`, `"tasks:child"`} {
		if !strings.Contains(text.String(), expected) {
			t.Fatalf("human output omits %s: %s", expected, text.String())
		}
	}
}

func TestRunInspectReviewCaseAliasesUseJSONFolding(t *testing.T) {
	for _, data := range []string{`{"spec_hash":"a","Spec_Hash":"b"}`, `{"kind":"fixture","\u212aind":"completed_copy"}`, `{"status":"done","\u017ftatus":"failed"}`, `{"outer":{"Principal":"a","principal":"b"}}`} {
		if uniqueJSON([]byte(data)) == nil {
			t.Fatalf("accepted case aliases: %s", data)
		}
	}
	if err := uniqueJSON([]byte(`{"launch":{"Authority":{"Principal":"owner","UserID":"owner"}}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRunInspectReviewArchiveLifecycleBinding(t *testing.T) {
	for _, test := range []struct{ name, journal, state string }{
		{"rebound", "{\"sequence\":1,\"source\":\"claude_hook\",\"type\":\"session_ready\",\"provider_session_id\":\"native-thread\"}\n", "complete_bound_archive"},
		{"wrong_agent", "{\"sequence\":1,\"source\":\"codex_hook\",\"type\":\"session_ready\",\"provider_session_id\":\"native-thread\"}\n", "egg_binding_conflict"},
		{"wrong_event", "{\"sequence\":1,\"source\":\"claude_hook\",\"type\":\"turn_completed\",\"provider_session_id\":\"native-thread\"}\n", "egg_binding_conflict"},
		{"bad_sequence", "{\"sequence\":2,\"source\":\"claude_hook\",\"type\":\"session_ready\",\"provider_session_id\":\"native-thread\"}\n", "invalid_lifecycle_binding"},
		{"partial_append", "{\"sequence\":1", "partial_lifecycle_binding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace, path := fixture(t, func(db *sql.DB, path string) {
				addArchive(t, path, "execution", "native-thread")
				dir := filepath.Join(path, "eggs", "execution")
				if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\nprovider_session_id=launch-thread\ncwd=/fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), []byte(test.journal), 0600); err != nil {
					t.Fatal(err)
				}
			})
			before := fingerprints(t, path)
			report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
			if err != nil {
				t.Fatal(err)
			}
			if report.Archives[0].State != test.state {
				t.Fatalf("archive=%s, want %s", report.Archives[0].State, test.state)
			}
			if !reflect.DeepEqual(before, fingerprints(t, path)) {
				t.Fatal("binding inspection changed evidence")
			}
		})
	}
}

func TestRunInspectPartialEggPathPreventsResume(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "run", "execution", "", "terminal", "timeout", str("partial"))
		addEgg(t, path, "run", "execution", "timeout", str("partial"))
		if err := os.WriteFile(filepath.Join(path, outputPath("execution", "run")), []byte(`{"version":1,"result":`), 0600); err != nil {
			t.Fatal(err)
		}
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','intent',1,1)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		sqlExec(t, db, "INSERT INTO conversation_events VALUES (1,'logical','logical','execution')")
		addArchive(t, path, "execution", "native-thread")
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	run := report.Runs[0]
	if run.Diagnosis != "timeout" || run.EvidenceState != "incomplete" || hasAction(run, "manual_resume_eligible") || len(run.Issues) != 1 || run.Issues[0].Code != "partial_or_unsupported_egg_record" {
		t.Fatalf("partial artifact conclusion: %+v", run)
	}
	if run.Issues[0].Sources[0].Path != outputPath("execution", "run") {
		t.Fatal("lost deterministic artifact reference")
	}
}
