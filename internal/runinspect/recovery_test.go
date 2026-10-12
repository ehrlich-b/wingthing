package runinspect

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func addArchive(t *testing.T, path, session, provider string) {
	t.Helper()
	dir := filepath.Join(path, "eggs", session)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.meta"), []byte("agent=claude\nformat=jsonl\nagent_session_id="+provider+"\ncwd=/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	data, _ := json.Marshal(map[string]any{"sessionId": provider, "type": "assistant", "message": map[string]any{"content": "ARCHIVE-PROVIDER-SENTINEL"}})
	if _, err := writer.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.jsonl.gz"), compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func hasAction(run Run, kind string) bool {
	for _, action := range run.Actions {
		if action.Kind == kind {
			return true
		}
	}
	return false
}

func TestRunInspectCheckpointArchiveMatrix(t *testing.T) {
	// Expectations are chosen from the pinned runtime's CAS and archive rules,
	// independently of inspector output. One finite case per operational gap.
	for _, test := range []struct {
		name, state, archive string
		delivered            int
		resume               bool
	}{
		{"fresh", "current_at_snapshot", "complete_bound_archive", 5, true},
		{"wake_does_not_ack", "unacknowledged_events", "complete_bound_archive", 0, false},
		{"past_evidence", "inconsistent_cursor", "complete_bound_archive", 6, false},
		{"missing_meta", "current_at_snapshot", "interrupted_rotation", 5, false},
		{"truncated_gzip", "current_at_snapshot", "incomplete_archive", 5, false},
		{"old_transcript_new_meta", "current_at_snapshot", "archive_identity_conflict", 5, false},
		{"pending_rotation", "current_at_snapshot", "complete_bound_archive", 5, false},
		{"missing_checkpoint", "absent", "complete_bound_archive", 5, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace, path := fixture(t, func(db *sql.DB, path string) {
				addWing(t, db, "run", "execution", "", "terminal", "timeout", str("safe partial result"))
				addEgg(t, path, "run", "execution", "timeout", str("safe partial result"))
				checkpoint := "retained intent"
				if test.name == "missing_checkpoint" {
					checkpoint = ""
				}
				sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution',?,2,?)", checkpoint, test.delivered)
				sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
				sqlExec(t, db, "INSERT INTO conversation_events VALUES (5,'logical','logical','execution')")
				sqlExec(t, db, "INSERT INTO conversation_wake_policy VALUES ('logical',5)")
				addArchive(t, path, "execution", "native-thread")
				dir := filepath.Join(path, "eggs", "execution")
				switch test.name {
				case "missing_meta":
					if err := os.Remove(filepath.Join(dir, "chat.meta")); err != nil {
						t.Fatal(err)
					}
				case "truncated_gzip":
					file := filepath.Join(dir, "chat.jsonl.gz")
					data, _ := os.ReadFile(file)
					if err := os.WriteFile(file, data[:len(data)-6], 0600); err != nil {
						t.Fatal(err)
					}
				case "old_transcript_new_meta":
					if err := os.WriteFile(filepath.Join(dir, "chat.meta"), []byte("agent=claude\nformat=jsonl\nagent_session_id=new-thread\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "pending_rotation":
					if err := os.WriteFile(filepath.Join(dir, ".chat-jsonl-interrupted.tmp"), []byte("partial new capture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
			report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Conversations) != 1 || report.Conversations[0].Checkpoint.State != test.state || report.Archives[0].State != test.archive || hasAction(report.Runs[0], "manual_resume_eligible") != test.resume {
				t.Fatalf("state/archive/resume: %+v %+v %+v", report.Conversations, report.Archives, report.Runs)
			}
			if *report.Conversations[0].Checkpoint.WakeDeliveryCursor != 5 {
				t.Fatal("wake cursor conflated with explicit ACK")
			}
			wire, _ := json.Marshal(report)
			if strings.Contains(string(wire), "ARCHIVE-PROVIDER-SENTINEL") || strings.Contains(string(wire), "retained intent") || strings.Contains(string(wire), "safe partial result") {
				t.Fatal("content escaped into metadata summary")
			}
		})
	}
}

func TestRunInspectStaleCheckpointWriterAndLostReply(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','retained private intent',1,5)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		sqlExec(t, db, "INSERT INTO conversation_events VALUES (5,'logical','logical','execution'),(8,'logical','logical','execution')")
		sqlExec(t, db, "INSERT INTO conversation_wake_policy VALUES ('logical',8)")
		payload := json.RawMessage(`{"method":"tools/call","params":{"name":"conversation_checkpoint","arguments":{"conversation_id":"logical","expected_revision":0,"after_cursor":5,"checkpoint":"retained private intent"}}}`)
		journal, _ := json.Marshal(map[string]any{"version": 1, "request_id": "request", "tool": "conversation_checkpoint", "payload": payload, "payload_sha256": digest(payload), "phase": "dispatching"})
		dir := filepath.Join(path, "conversation-brokers", "execution", "journal")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "request.json"), journal, 0600); err != nil {
			t.Fatal(err)
		}
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.CheckpointAttempts) != 1 || report.CheckpointAttempts[0].FenceState != "stale_writer" || report.CheckpointAttempts[0].Acknowledgement != "cursor_covered_by_durable_checkpoint" {
		t.Fatalf("lost checkpoint reply: %+v", report.CheckpointAttempts)
	}
	cp := report.Conversations[0].Checkpoint
	if cp.State != "unacknowledged_events" || *cp.PendingEvents != 1 || *cp.AcknowledgedCursor != 5 || *cp.WakeDeliveryCursor != 8 {
		t.Fatalf("CAS/replay cursors: %+v", cp)
	}
}

func TestRunInspectTerminalAndAdmissionMatrix(t *testing.T) {
	for _, test := range []struct {
		name, wing, egg, phase, diagnosis string
		text                              *string
	}{
		{"completion", "done", "done", "terminal", "completed_with_output", str("exact Ω")},
		{"lost_ack", "pending", "done", "submitting", "completed_with_output", str("exact Ω")},
		{"output_missing", "pending", "done", "submitting", "completed_output_unavailable", nil},
		{"failure", "failed", "failed", "terminal", "failed", str("safe partial")},
		{"running", "running", "running", "observing", "running_at_snapshot", nil},
		{"reservation_only", "pending", "pending", "submitting", "admitted_outcome_unknown", nil},
		{"stale_running_observation", "running", "pending", "observing", "inconsistent_evidence", nil},
		{"failed_wing_unknown_egg", "failed", "pending", "terminal", "failed", nil},
		{"spawn_intent_no_egg", "pending", "", "spawning", "admitted_outcome_unknown", nil},
		{"terminal_conflict", "failed", "done", "terminal", "inconsistent_evidence", str("safe partial")},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace, path := fixture(t, func(db *sql.DB, path string) {
				addWing(t, db, "run", "execution", "", test.phase, test.wing, test.text)
				if test.egg != "" {
					addEgg(t, path, "run", "execution", test.egg, test.text)
				}
			})
			report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
			if err != nil {
				t.Fatal(err)
			}
			if report.Runs[0].Diagnosis != test.diagnosis {
				t.Fatalf("diagnosis %s, want %s", report.Runs[0].Diagnosis, test.diagnosis)
			}
			if hasAction(report.Runs[0], "wait_for_known_completion") != (test.name == "running") {
				t.Fatal("ambiguous admission/terminal conflict recommended waiting")
			}
		})
	}
}

func TestRunInspectBoundedPreviewAndContentIsolation(t *testing.T) {
	const secret = "PRIVATE-PROVIDER-SENTINEL"
	text := "\x1b[31m\n\x00\u202eΩ" + secret
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "run", "execution", "", "terminal", "done", &text)
		addEgg(t, path, "run", "execution", "done", &text)
		sqlExec(t, db, "UPDATE agent_runs SET record=json_set(record,'$.prompt',?,'$.result.error',?,'$.label',?)", secret, secret, secret)
	})
	for _, limit := range []int{0, 1, 9, 4096} {
		report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace, PreviewBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		var human bytes.Buffer
		if err := RenderText(&human, report); err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(human.String(), "\x1b\x00\u202e") {
			t.Fatal("raw controls reached human renderer")
		}
		for _, out := range report.Runs[0].Output {
			n := limit
			if n > len(text) {
				n = len(text)
			}
			if out.Bytes != int64(len(text)) || out.OmittedBytes != int64(len(text)-n) {
				t.Fatalf("omitted byte accounting: %+v", out)
			}
			if limit == 0 && out.Preview != "" {
				t.Fatal("implicit content preview")
			}
		}
		wire, _ := json.Marshal(report)
		if limit == 0 && (strings.Contains(string(wire), secret) || strings.Contains(human.String(), secret)) {
			t.Fatal("provider content entered metadata summary")
		}
		if limit == 4096 && !strings.Contains(human.String(), "untrusted output preview: ") {
			t.Fatal("preview lost its data boundary")
		}
	}
	for _, limit := range []int{-1, 4097} {
		if _, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace, PreviewBytes: limit}); err == nil {
			t.Fatal("unbounded preview accepted")
		}
	}
}

func TestRunInspectOlderSchemaRetainsUnknowns(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "DROP TABLE conversations; CREATE TABLE conversations (id TEXT,owner_id TEXT,root_id TEXT,parent_id TEXT,session_id TEXT,checkpoint TEXT)")
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','old private intent')")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		addWing(t, db, "run", "execution", "", "terminal", "failed", str("old result"))
		addArchive(t, path, "execution", "native-thread")
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conversations) != 1 || report.Conversations[0].Checkpoint.State != "unknown" || report.Conversations[0].Checkpoint.Revision != nil || report.Conversations[0].Checkpoint.AcknowledgedCursor != nil {
		t.Fatalf("older schema guessed a fence: %+v", report.Conversations)
	}
	if len(report.Runs) != 1 || report.Runs[0].Diagnosis != "failed" || hasAction(report.Runs[0], "manual_resume_eligible") {
		t.Fatalf("older state discarded or promoted: %+v", report.Runs)
	}
}
