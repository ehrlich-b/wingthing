package runinspect

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

type fingerprint struct {
	Hash     string
	Bytes    string
	Mode     os.FileMode
	Size     int64
	Modified time.Time
}

func fingerprints(t *testing.T, path string) map[string]fingerprint {
	t.Helper()
	out := map[string]fingerprint{}
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := fingerprint{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime()}
		if !d.IsDir() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			value.Hash = hex.EncodeToString(hash[:])
			value.Bytes = string(data)
		}
		out[p] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunInspectPinnedSerializersAndHashes(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "snapshot")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	// Use the actual pinned serializers and schema, only in the disposable test
	// copy. Inspection itself must not use this migrating/writable Store API.
	db, err := store.Open(filepath.Join(path, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	captured := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	run := wingsession.Run{ID: "serialized-run", SessionID: "serialized-execution", Phase: "terminal", SpecHash: "spec", Agent: "claude", CWD: "/fixture", Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: "owner", UserID: "owner"}}, Result: egg.RunTurnResult{RunID: "serialized-run", SessionID: "serialized-execution", Status: "done", Text: "exact serialized Ω", ProviderSessionID: "native", TurnID: "turn", EndedAt: captured, Deadline: captured.Add(time.Minute)}}
	wire, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{ID: run.ID, Type: "agent_run", What: "private prompt", Agent: "claude", Principal: "owner", RunAt: captured, CreatedAt: captured, Status: "done", Output: &run.Result.Text, FinishedAt: &captured}
	row, _, err := db.AdmitAgentRun(&store.AgentRun{ID: run.ID, SessionID: run.SessionID, Principal: "owner", SpecHash: "spec", Record: wire}, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveAgentRun(row, task, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	seal(t, path)
	before := fingerprints(t, path)
	for _, preview := range []int{0, 17, 4096} {
		report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace, PreviewBytes: preview})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Runs) != 1 || report.Runs[0].Diagnosis != "completed_with_output" || report.Runs[0].EvidenceState != "consistent" || report.Runs[0].Receipts[0].ProviderSessionID != "native" {
			t.Fatalf("pinned serialization misinterpreted: %+v", report)
		}
		if !reflect.DeepEqual(before, fingerprints(t, path)) {
			t.Fatal("inspection changed bytes, SHA256, mode, timestamps or inventory")
		}
	}
}

func TestRunInspectLegacyParentDoesNotInventExecution(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "CREATE TABLE tasks (id TEXT,type TEXT,status TEXT,parent_id TEXT,principal TEXT,output TEXT)")
		sqlExec(t, db, "INSERT INTO tasks VALUES ('legacy','agent_run','done',NULL,'owner','legacy result'),('unrelated','prompt','done',NULL,'owner','ignored')")
		addWing(t, db, "child", "execution", "legacy", "queued", "pending", nil)
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.LegacyTasks) != 1 || report.LegacyTasks[0].ID != "legacy" || report.LegacyTasks[0].Diagnosis != "completed_with_output" {
		t.Fatalf("legacy ancestor absent: %+v", report.LegacyTasks)
	}
	if report.Runs[0].ParentID != "legacy" || len(report.Runs[0].Issues) != 0 {
		t.Fatalf("observed legacy parent marked missing: %+v", report.Runs)
	}
	if report.LegacyTasks[0].Output.Preview != "" {
		t.Fatal("legacy output escaped into default summary")
	}
}

func TestRunInspectAmbiguousKeysRemainUnknown(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "valid", "valid-execution", "", "terminal", "done", str("safe result"))
		sqlExec(t, db, "INSERT INTO agent_runs VALUES ('ambiguous','execution','owner','wing-spec',1,?)", `{"run_id":"ambiguous","run_id":"other","session_id":"execution","phase":"terminal","result":{"status":"done","text":"PRIVATE-AMBIGUOUS-CONTENT"}}`)
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 2 || report.Runs[0].Diagnosis != "missing_evidence" || len(report.Runs[0].Output) != 0 || report.Runs[1].Diagnosis != "completed_with_output" {
		t.Fatalf("ambiguous keys won: %+v", report.Runs)
	}
	data, err := os.ReadFile(filepath.Join(path, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte(`{"completed":false,`), data[1:]...)
	if err := os.WriteFile(filepath.Join(path, ManifestName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace}); err == nil {
		t.Fatal("ambiguous completion assertion accepted")
	}
}

func TestRunInspectEventOwnershipMismatchPreventsResume(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "run", "execution", "", "terminal", "timeout", str("partial"))
		addEgg(t, path, "run", "execution", "timeout", str("partial"))
		addArchive(t, path, "execution", "native-thread")
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','intent',1,5)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		sqlExec(t, db, "INSERT INTO conversation_events VALUES (5,'logical','foreign','execution')")
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conversations[0].Issues) == 0 || hasAction(report.Runs[0], "manual_resume_eligible") {
		t.Fatalf("foreign event made checkpoint resumable: %+v", report)
	}
}

func TestRunInspectConflictingExecutionsNameEveryIdentity(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "same-run", "execution-a", "", "terminal", "done", str("first result"))
		addEgg(t, path, "same-run", "execution-a", "done", str("first result"))
		addEgg(t, path, "same-run", "execution-b", "done", str("other result"))
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	run := report.Runs[0]
	if run.Diagnosis != "inconsistent_evidence" || len(run.Output) != 0 || len(run.Issues) != 1 || len(run.Issues[0].Sources) != 3 {
		t.Fatalf("selected a conflicting winner: %+v", run)
	}
	seen := map[string]bool{}
	for _, id := range run.Issues[0].IDs {
		seen[id] = true
	}
	if !seen["same-run"] || !seen["execution-a"] || !seen["execution-b"] {
		t.Fatalf("affected execution IDs absent: %+v", run.Issues)
	}
}

func TestRunInspectPendingTextCannotSupplyCompletedOutput(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		// The pinned serializer always writes text:"" on a pending wing result.
		// That field is not the completed artifact of a status-only egg receipt.
		addWing(t, db, "run", "execution", "", "submitting", "pending", str(""))
		addEgg(t, path, "run", "execution", "done", nil)
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if report.Runs[0].Diagnosis != "completed_output_unavailable" || report.Runs[0].Acknowledgement != "terminal_receipt_not_in_wing" || !hasAction(report.Runs[0], "missing_evidence") {
		t.Fatalf("pending field supplied final artifact: %+v", report.Runs)
	}
}

func TestRunInspectInitialZeroCursorIsNotAcknowledgement(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','execution','',0,0)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('execution','logical')")
		payload := json.RawMessage(`{"method":"tools/call","params":{"name":"conversation_checkpoint","arguments":{"conversation_id":"logical","expected_revision":0,"after_cursor":0,"checkpoint":"new intent"}}}`)
		entry, _ := json.Marshal(map[string]any{"version": 1, "request_id": "request", "tool": "conversation_checkpoint", "payload": payload, "payload_sha256": digest(payload), "phase": "dispatching"})
		dir := filepath.Join(path, "conversation-brokers", "execution", "journal")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "request.json"), entry, 0600); err != nil {
			t.Fatal(err)
		}
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conversations[0].Checkpoint.State != "absent" || len(report.CheckpointAttempts) != 1 || report.CheckpointAttempts[0].Acknowledgement != "request_outcome_unconfirmed" {
		t.Fatalf("initial zero cursor became an ACK: %+v", report)
	}
}
