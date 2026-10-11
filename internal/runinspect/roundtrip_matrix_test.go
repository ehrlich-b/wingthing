//go:build runinspectfixture

package runinspect_test

import (
	"bytes"
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/store"
)

//go:embed testdata/migrations/*.sql
var migrationShapes embed.FS

func interruptedRoundTrip(t *testing.T) (*fixture.Builder, *store.Conversation, int64) {
	t.Helper()
	b := newRoundTrip(t)
	b.Wing("run", "execution", "", "terminal", "timeout", fixture.PrivateOutput)
	b.Egg("run", "execution", "timeout", fixture.PrivateOutput)
	c := b.Conversation("logical", "execution", "")
	seq := b.Event(c, 1)
	b.Checkpoint(c.ID, 0, seq)
	b.Archive("execution", "claude", "native-execution")
	return b, c, seq
}
func mutateEgg(t *testing.T, b *fixture.Builder, id, session string, modify func(map[string]json.RawMessage)) {
	t.Helper()
	path := fixture.EggPath(session, id)
	data, err := os.ReadFile(filepath.Join(b.Path, path))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	modify(record)
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	b.Write(path, append(data, '\n'))
}
func dropTerminalText(t *testing.T, b *fixture.Builder, id, session string) {
	t.Helper()
	b.SQL("UPDATE agent_runs SET record=json_remove(record,'$.result.text') WHERE id=?", id)
	mutateEgg(t, b, id, session, func(record map[string]json.RawMessage) {
		var result map[string]json.RawMessage
		if err := json.Unmarshal(record["result"], &result); err != nil {
			t.Fatal(err)
		}
		delete(result, "text")
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		record["result"] = data
	})
}

func TestRunInspectRoundTripTwoReplacementsAndStaleCheckpoint(t *testing.T) {
	b, c, seq := interruptedRoundTrip(t)
	if err := b.Store.ResumeConversationExecution("execution", "replacement-one"); err != nil {
		t.Fatal(err)
	}
	if err := b.Store.ResumeConversationExecution("replacement-one", "replacement-two"); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"replacement-one", "replacement-two"} {
		b.Wing("run-"+session, session, "", "terminal", "timeout", fixture.PrivateOutput)
		b.Egg("run-"+session, session, "timeout", fixture.PrivateOutput)
		b.Archive(session, "claude", "native-"+session)
	}
	// Superseded writer intent was accepted before replacement; a stale DB CAS
	// cannot overwrite the retained checkpoint, as in the recovery oracle.
	if err := b.Store.CheckpointConversation("owner", c.ID, 0, seq, "stale overwrite"); err == nil {
		t.Fatal("durable writer accepted stale checkpoint")
	}
	b.Journal("execution", "superseded-request", c.ID, 0, seq)
	current, err := b.Store.GetConversation("owner", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.SessionID != "replacement-two" || current.Revision != 1 || current.DeliveredCursor != seq || current.Checkpoint != fixture.PrivateCheckpoint {
		t.Fatalf("writer state: %+v", current)
	}
	b.Seal()
	r := inspectRoundTrip(t, b, 0)
	if len(r.Conversations) != 1 || !reflect.DeepEqual(r.Conversations[0].Executions, []string{"execution", "replacement-one", "replacement-two"}) || r.Conversations[0].SessionID != "replacement-two" {
		t.Fatalf("execution history: %+v", r.Conversations)
	}
	for _, run := range r.Runs {
		if run.CurrentSessionID != "replacement-two" || run.ConversationID != "logical" || roundTripAction(run, "manual_resume_eligible") != (run.SessionID == "replacement-two") {
			t.Fatalf("stale execution promoted: %+v", run)
		}
	}
	if len(r.CheckpointAttempts) != 1 || r.CheckpointAttempts[0].SessionID != "execution" || r.CheckpointAttempts[0].FenceState != "stale_writer" || r.CheckpointAttempts[0].Acknowledgement != "cursor_covered_by_durable_checkpoint" {
		t.Fatalf("checkpoint: %+v", r.CheckpointAttempts)
	}
}

func TestRunInspectRoundTripCheckpointAcknowledgement(t *testing.T) {
	// The oracle is the actual CheckpointConversation CAS contract plus the
	// existing TestConversationDeliveryReplayAndCheckpointCAS recovery fixture.
	for _, tc := range []struct {
		name, state string
		delta       int64
		resume      bool
	}{
		{"behind", "unacknowledged_events", -1, false},
		{"equal", "current_at_snapshot", 0, true},
		{"ahead_corrupt_copy", "inconsistent_cursor", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, c, seq := interruptedRoundTrip(t)
			if err := b.Store.CheckpointConversation("owner", c.ID, 1, seq+1, "unseen"); err == nil {
				t.Fatal("writer acknowledged unseen event")
			}
			if tc.delta < 0 {
				// A healthy behind-ACK snapshot adds a durable event after checkpoint.
				b.Event(c, 2)
			} else if tc.delta > 0 {
				// Healthy writers refuse this. Explicit, finite corruption models a
				// supplier whose acknowledgement contradicts its copied event inventory.
				b.SQL("UPDATE conversations SET delivered_cursor=? WHERE id=?", seq+1, c.ID)
			}
			b.Seal()
			r := inspectRoundTrip(t, b, 0)
			cp := r.Conversations[0].Checkpoint
			if cp.State != tc.state || roundTripAction(roundTripRun(t, r, "run"), "manual_resume_eligible") != tc.resume {
				t.Fatalf("checkpoint/resume: %+v %+v", cp, r.Runs)
			}
			if tc.delta < 0 && (*cp.PendingEvents != 1 || *cp.EventHead != seq+1 || *cp.AcknowledgedCursor != seq) {
				t.Fatalf("event accounting: %+v", cp)
			}
			if tc.delta == 0 && (*cp.PendingEvents != 0 || *cp.EventHead != seq || *cp.Revision != 1) {
				t.Fatalf("current checkpoint: %+v", cp)
			}
		})
	}
}

func TestRunInspectRoundTripTerminalOutputAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, diagnosis, text string
		drop, archive         bool
	}{
		{"present", "completed_with_output", fixture.PrivateOutput, false, false},
		{"empty_present", "completed_with_output", "", false, false},
		{"absent_old_record", "completed_output_unavailable", "", true, false},
		{"archived_only", "completed_output_unavailable", "", true, true},
		{"present_and_archived", "completed_with_output", fixture.PrivateOutput, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newRoundTrip(t)
			b.Wing("run", "execution", "", "terminal", "done", tc.text)
			b.Egg("run", "execution", "done", tc.text)
			if tc.drop {
				dropTerminalText(t, b, "run", "execution")
			}
			if tc.archive {
				b.Archive("execution", "claude", "native-execution")
			}
			b.Seal()
			r := inspectRoundTrip(t, b, 0)
			run := roundTripRun(t, r, "run")
			if run.Diagnosis != tc.diagnosis || run.WingStatus != "done" || run.EggStatus != "done" || roundTripAction(run, "manual_resume_eligible") {
				t.Fatalf("terminal outcome lost: %+v", run)
			}
			if len(run.Output) != 2 {
				t.Fatalf("output source handles: %+v", run.Output)
			}
			for _, out := range run.Output {
				if out.Available == tc.drop || out.Bytes != int64(len(tc.text)) {
					t.Fatalf("output availability: %+v", out)
				}
			}
			if tc.archive && r.Archives[0].State != "complete_bound_archive" {
				t.Fatalf("archive: %+v", r.Archives)
			}
		})
	}
}

func TestRunInspectRoundTripPartialSchemasAndVersions(t *testing.T) {
	for _, name := range []string{"partial_fences", "future_egg", "partial_egg", "partial_wing"} {
		t.Run(name, func(t *testing.T) {
			b, _, _ := interruptedRoundTrip(t)
			b.Wing("valid", "valid-execution", "", "terminal", "done", fixture.PrivateOutput)
			b.Egg("valid", "valid-execution", "done", fixture.PrivateOutput)
			switch name {
			case "partial_fences":
				shape, err := migrationShapes.ReadFile("testdata/migrations/partial_fences.sql")
				if err != nil {
					t.Fatal(err)
				}
				b.SQL(string(shape))
			case "future_egg":
				mutateEgg(t, b, "run", "execution", func(record map[string]json.RawMessage) { record["version"] = json.RawMessage("99") })
			case "partial_egg":
				b.Write(fixture.EggPath("execution", "run"), []byte(`{"version":1,"result":`))
			case "partial_wing":
				b.SQL("UPDATE agent_runs SET record=? WHERE id='run'", `{"run_id":"run"`)
			}
			b.Seal()
			r := inspectRoundTrip(t, b, 0)
			if roundTripRun(t, r, "valid").Diagnosis != "completed_with_output" {
				t.Fatal("unrelated completion discarded")
			}
			run := roundTripRun(t, r, "run")
			if roundTripAction(run, "manual_resume_eligible") || len(r.Issues) == 0 {
				t.Fatalf("unsupported evidence promoted: %+v", r)
			}
			if name == "partial_fences" && (r.Conversations[0].Checkpoint.State != "unknown" || r.Conversations[0].Checkpoint.Revision != nil || r.Conversations[0].Checkpoint.AcknowledgedCursor != nil || run.Diagnosis != "timeout") {
				t.Fatalf("partial schema upgraded: %+v", r)
			}
		})
	}
}

func TestRunInspectRoundTripLegacyMigrationShape(t *testing.T) {
	b := newRoundTrip(t)
	shape, err := migrationShapes.ReadFile("testdata/migrations/legacy_tasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	b.SQL(string(shape))
	// Migration 001 predates owner/execution fences. No modern serializer can
	// write its later columns; retain only the actual historical projection.
	b.SQL("INSERT INTO tasks(id,type,what,run_at,status,output,parent_id) VALUES ('legacy','agent_run','private prompt',?,'done','legacy completed output','missing-parent')", fixture.CapturedAt)
	b.Seal()
	r := inspectRoundTrip(t, b, 0)
	if len(r.Runs) != 0 || len(r.LegacyTasks) != 1 || r.LegacyTasks[0].Diagnosis != "completed_with_output" || !r.LegacyTasks[0].Output.Available || len(r.LegacyTasks[0].Issues) != 1 || r.LegacyTasks[0].Issues[0].Code != "missing_parent" {
		t.Fatalf("legacy result: %+v", r)
	}
	if len(r.Issues) == 0 {
		t.Fatal("missing schema fields silently upgraded")
	}
}

func TestRunInspectRoundTripRotationAndParentMismatch(t *testing.T) {
	for _, name := range []string{"temporary_capture", "missing_archive_meta", "truncated_archive", "parent_owner_conflict"} {
		t.Run(name, func(t *testing.T) {
			b, c, seq := interruptedRoundTrip(t)
			b.Wing("valid", "valid-execution", "", "terminal", "done", fixture.PrivateOutput)
			b.Egg("valid", "valid-execution", "done", fixture.PrivateOutput)
			switch name {
			case "temporary_capture":
				b.Write("eggs/execution/.chat-jsonl-interrupted.tmp", []byte("partial bounded capture"))
			case "missing_archive_meta":
				if err := os.Remove(filepath.Join(b.Path, "eggs/execution/chat.meta")); err != nil {
					t.Fatal(err)
				}
			case "truncated_archive":
				data, err := os.ReadFile(filepath.Join(b.Path, "eggs/execution/chat.jsonl.gz"))
				if err != nil {
					t.Fatal(err)
				}
				b.Write("eggs/execution/chat.jsonl.gz", data[:len(data)-6])
			case "parent_owner_conflict":
				b.Wing("parent", "parent-execution", "", "terminal", "done", fixture.PrivateOutput)
				b.SQL("UPDATE agent_runs SET record=json_set(record,'$.parent_id','parent') WHERE id='run'")
				b.SQL("UPDATE agent_runs SET principal='foreign',record=json_set(record,'$.launch.Authority.Principal','foreign','$.launch.Authority.UserID','foreign') WHERE id='parent'")
			}
			b.Journal("execution", "ambiguous", c.ID, 1, seq)
			b.Seal()
			r := inspectRoundTrip(t, b, 0)
			if roundTripRun(t, r, "valid").Diagnosis != "completed_with_output" || roundTripAction(roundTripRun(t, r, "run"), "manual_resume_eligible") {
				t.Fatalf("corruption affected actions/unrelated run: %+v", r)
			}
			run := roundTripRun(t, r, "run")
			if name == "parent_owner_conflict" && (run.Diagnosis != "inconsistent_evidence" || !hasRoundTripIssue(run.Issues, "parent_owner_conflict")) {
				t.Fatalf("parent identity mismatch omitted: %+v", run)
			}
			for _, action := range run.Actions {
				if len(action.Requires) == 0 || len(action.Basis) == 0 || strings.Contains(action.Kind, "execute") {
					t.Fatalf("action lacks manual evidence: %+v", action)
				}
			}
		})
	}
}
func hasRoundTripIssue(issues []runinspect.Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestRunInspectRoundTripArchiveBindingRestoreOracle(t *testing.T) {
	b, _, _ := interruptedRoundTrip(t)
	b.Write("eggs/execution/egg.meta", []byte("agent=claude\nprovider_session_id=foreign-thread\ncwd=/fixture\n"))
	b.Seal()
	copyPath := filepath.Join(b.Workspace, "restore-probe")
	if err := os.CopyFS(copyPath, os.DirFS(filepath.Join(b.Path, "eggs/execution"))); err != nil {
		t.Fatal(err)
	}
	_, err := egg.RestoreSessionHistory("claude", "/fixture", copyPath, filepath.Join(b.Workspace, "restore-home"))
	if err == nil || !strings.Contains(err.Error(), "captured provider session ID does not match egg metadata") {
		t.Fatalf("independent restore validator: %v", err)
	}
	r := inspectRoundTrip(t, b, 0)
	if r.Archives[0].State != "egg_binding_conflict" || roundTripAction(roundTripRun(t, r, "run"), "manual_resume_eligible") {
		t.Fatalf("restore contradiction accepted: %+v", r)
	}
	var human bytes.Buffer
	if err := runinspect.RenderText(&human, r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(human.Bytes(), []byte("egg_binding_conflict")) {
		t.Fatal("human archive contradiction omitted")
	}
}

func TestRunInspectRoundTripArchiveRebindingRestoreOracle(t *testing.T) {
	b, _, _ := interruptedRoundTrip(t)
	b.RebindArchive("execution", "launch-before-clear", "native-execution")
	b.Seal()
	before := fixture.Inventory(t, b.Path)
	copyPath := filepath.Join(b.Workspace, "restore-rebinding")
	if err := os.CopyFS(copyPath, os.DirFS(filepath.Join(b.Path, "eggs/execution"))); err != nil {
		t.Fatal(err)
	}
	native, err := egg.RestoreSessionHistory("claude", "/fixture", copyPath, filepath.Join(b.Workspace, "restored-provider-home"))
	if err != nil || native != "native-execution" {
		t.Fatalf("valid persisted rebinding rejected by restore: %s %v", native, err)
	}
	r := inspectRoundTrip(t, b, 0)
	if r.Archives[0].State != "complete_bound_archive" || !roundTripAction(roundTripRun(t, r, "run"), "manual_resume_eligible") {
		t.Fatalf("valid persisted rebinding rejected by inspector: %+v", r)
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Path)) {
		t.Fatal("restore oracle changed inspector's input")
	}
}
