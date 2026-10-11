package runinspect

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const fixtureSchema = `
CREATE TABLE agent_runs (id TEXT,session_id TEXT,principal TEXT,spec_hash TEXT,revision INTEGER,record TEXT);
CREATE TABLE conversations (id TEXT,owner_id TEXT,root_id TEXT,parent_id TEXT,session_id TEXT,checkpoint TEXT,revision INTEGER,delivered_cursor INTEGER);
CREATE TABLE conversation_executions (session_id TEXT,conversation_id TEXT);
CREATE TABLE conversation_events (sequence INTEGER,root_id TEXT,conversation_id TEXT,session_id TEXT);
CREATE TABLE conversation_wake_policy (root_id TEXT,delivery_cursor INTEGER);
`

func fixture(t *testing.T, setup func(*sql.DB, string)) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "snapshot")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(path, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(fixtureSchema); err != nil {
		t.Fatal(err)
	}
	setup(db, path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	seal(t, path)
	return workspace, path
}

func seal(t *testing.T, path string) {
	t.Helper()
	m := Manifest{Version: 1, Kind: "fixture", Completed: true, CapturedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), Files: []File{}}
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == ManifestName {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, File{Path: filepath.ToSlash(rel), Bytes: int64(len(data)), SHA256: digest(data)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ManifestName), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func sqlExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func addWing(t *testing.T, db *sql.DB, id, session, parent, phase, status string, text *string) {
	t.Helper()
	// This JSON is hand-authored independently of the inspector's projections.
	data, err := json.Marshal(map[string]any{"run_id": id, "session_id": session, "spec_hash": "wing-spec", "parent_id": parent, "phase": phase, "launch": map[string]any{"Authority": map[string]any{"Principal": "owner", "UserID": "owner"}}, "result": map[string]any{"run_id": id, "session_id": session, "status": status, "text": text, "provider_session_id": "native-thread", "turn_id": "native-turn"}})
	if err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, "INSERT INTO agent_runs VALUES (?,?,?,'wing-spec',1,?)", id, session, "owner", string(data))
}

func addEgg(t *testing.T, path, id, session, status string, text *string) {
	t.Helper()
	result := map[string]any{"run_id": id, "session_id": session, "status": status, "provider_session_id": "native-thread", "turn_id": "native-turn", "started_at": "2026-10-10T11:59:00Z", "deadline": "2026-10-10T12:01:00Z", "ended_at": "2026-10-10T12:00:00Z"}
	if text != nil {
		result["text"] = *text
	}
	data, err := json.Marshal(map[string]any{"version": 1, "spec_hash": "egg-spec", "result": result})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, outputPath(session, id))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func str(s string) *string { return &s }

func TestRunInspectIdentityAndLostAcknowledgement(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "orphan", "execution", "missing-parent", "submitting", "pending", nil)
		addEgg(t, path, "orphan", "execution", "done", str("exact output Ω"))
		addWing(t, db, "clean", "clean-exec", "", "terminal", "done", str("clean output"))
		addEgg(t, path, "clean", "clean-exec", "done", str("clean output"))
	})
	before := treeBytes(t, path)
	report, err := Inspect(context.Background(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 2 || report.Runs[0].ID != "clean" || report.Runs[0].Diagnosis != "completed_with_output" {
		t.Fatalf("clean report: %+v", report.Runs)
	}
	orphan := report.Runs[1]
	if orphan.Acknowledgement != "terminal_receipt_not_in_wing" || orphan.Diagnosis != "completed_with_output" || orphan.Issues[0].Code != "missing_parent" {
		t.Fatalf("orphan: %+v", orphan)
	}
	if !reflect.DeepEqual(before, treeBytes(t, path)) {
		t.Fatal("inspection changed snapshot bytes")
	}
}

func treeBytes(t *testing.T, path string) map[string]string {
	t.Helper()
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		out[p] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRunInspectSymlinkedWorkspaceParent(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "clean", "execution", "", "terminal", "done", str("output"))
	})
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	parentAlias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(filepath.Dir(workspace), parentAlias); err != nil {
		t.Fatal(err)
	}
	aliasedWorkspace := filepath.Join(parentAlias, filepath.Base(workspace))
	aliasedPath := filepath.Join(aliasedWorkspace, "snapshot")
	before := treeBytes(t, path)
	want, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, workspace, path string
	}{
		{"both_aliased", aliasedWorkspace, aliasedPath},
		{"snapshot_aliased", workspace, aliasedPath},
		{"workspace_aliased", aliasedWorkspace, path},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, err := ValidateDirectory(tc.workspace, tc.path)
			if err != nil || gotPath != path {
				t.Fatalf("canonical directory = %q, %v; want %q", gotPath, err, path)
			}
			got, err := Inspect(t.Context(), tc.path, Options{WorkspaceRoot: tc.workspace})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("aliased parent changed inspection: %v, %+v", err, got)
			}
			if !reflect.DeepEqual(before, treeBytes(t, path)) {
				t.Fatal("inspection changed snapshot bytes")
			}
		})
	}
}

func TestRunInspectRejectsWorkspaceSymlinks(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {})
	_, outside := fixture(t, func(db *sql.DB, path string) {})
	for _, tc := range []struct {
		name, target, suffix string
	}{
		{"inside", path, ""},
		{"escaping", outside, ""},
		{"workspace_prefix", workspace, "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alias := filepath.Join(workspace, tc.name)
			if err := os.Symlink(tc.target, alias); err != nil {
				t.Fatal(err)
			}
			before := treeBytes(t, path)
			outsideBefore := treeBytes(t, outside)
			_, err := Inspect(t.Context(), filepath.Join(alias, tc.suffix), Options{WorkspaceRoot: workspace})
			if err == nil || err.Error() != "directory must be an unaliased path inside the workspace" {
				t.Fatalf("workspace symlink rejection = %v", err)
			}
			if !reflect.DeepEqual(before, treeBytes(t, path)) || !reflect.DeepEqual(outsideBefore, treeBytes(t, outside)) {
				t.Fatal("refusal changed snapshot bytes")
			}
		})
	}
}

func TestRunInspectManifestTrustBoundary(t *testing.T) {
	for _, name := range []string{"missing", "incomplete", "mismatch", "outside", "symlink", "wal", "unlisted"} {
		t.Run(name, func(t *testing.T) {
			workspace, path := fixture(t, func(db *sql.DB, path string) {})
			switch name {
			case "missing":
				if err := os.Remove(filepath.Join(path, ManifestName)); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				data, _ := os.ReadFile(filepath.Join(path, ManifestName))
				var m Manifest
				_ = json.Unmarshal(data, &m)
				m.Completed = false
				data, _ = json.Marshal(m)
				if err := os.WriteFile(filepath.Join(path, ManifestName), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "mismatch":
				if err := os.WriteFile(filepath.Join(path, "wt.db"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "outside":
				workspace = filepath.Join(workspace, "other")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(workspace, "alias")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				path = alias
			case "wal":
				if err := os.WriteFile(filepath.Join(path, "wt.db-wal"), []byte("not consolidated"), 0600); err != nil {
					t.Fatal(err)
				}
				seal(t, path)
			case "unlisted":
				if err := os.WriteFile(filepath.Join(path, "extra"), []byte("partial copy"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := treeBytes(t, path)
			if _, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace}); err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
			if !reflect.DeepEqual(before, treeBytes(t, path)) {
				t.Fatal("refusal changed snapshot bytes")
			}
		})
	}
}

func TestRunInspectConflictingOwnershipAndReplacement(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		addWing(t, db, "a", "shared", "", "observing", "running", nil)
		addWing(t, db, "b", "shared", "", "observing", "running", nil)
		addEgg(t, path, "a", "shared", "done", str("complete a"))
		addWing(t, db, "replaced", "previous", "", "terminal", "done", str("old output"))
		sqlExec(t, db, "INSERT INTO conversations VALUES ('logical','owner','logical','','current','intent',2,0)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('previous','logical'),('current','logical')")
		addWing(t, db, "missing-ancestor", "child", "", "admitted", "pending", nil)
		sqlExec(t, db, "INSERT INTO conversations VALUES ('child-logical','owner','missing-root','missing-parent','child','intent',1,0)")
		sqlExec(t, db, "INSERT INTO conversation_executions VALUES ('child','child-logical')")
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range report.Runs {
		switch run.ID {
		case "a", "b":
			if run.Diagnosis != "inconsistent_evidence" || run.EvidenceState != "inconsistent" {
				t.Fatalf("ownership guess: %+v", run)
			}
		case "replaced":
			if run.ConversationID != "logical" || run.CurrentSessionID != "current" || run.SessionID != "previous" || run.Diagnosis != "completed_with_output" {
				t.Fatalf("replacement confused history: %+v", run)
			}
		}
	}
	if len(report.Conversations) != 2 || len(report.Conversations[0].Issues) != 2 {
		t.Fatalf("missing ancestry: %+v", report.Conversations)
	}
	second, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil || !reflect.DeepEqual(report, second) {
		t.Fatal("inspection is nondeterministic")
	}
}

func TestRunInspectPartialRecordKeepsUnrelatedRuns(t *testing.T) {
	workspace, path := fixture(t, func(db *sql.DB, path string) {
		sqlExec(t, db, "INSERT INTO agent_runs VALUES ('partial','partial-execution','owner','spec',1,'{')")
		addWing(t, db, "valid", "valid-execution", "", "terminal", "done", str("kept output"))
		addEgg(t, path, "future", "future-execution", "done", str("future text"))
		file := filepath.Join(path, outputPath("future-execution", "future"))
		data, _ := os.ReadFile(file)
		var raw map[string]any
		_ = json.Unmarshal(data, &raw)
		raw["version"] = 99
		data, _ = json.Marshal(raw)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	})
	report, err := Inspect(t.Context(), path, Options{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Runs) != 3 || report.Runs[2].Diagnosis != "completed_with_output" || len(report.Issues) != 2 {
		t.Fatalf("partial/future record discarded unrelated evidence: %+v", report)
	}
	if report.Runs[0].Diagnosis == "completed_with_output" || len(report.Runs[0].Output) != 0 {
		t.Fatal("future serializer trusted")
	}
}
