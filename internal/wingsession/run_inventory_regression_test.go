package wingsession

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// The alias is an enclosing directory, not a final-component cwd alias. This
// models /var -> /private/var while keeping all fixture files in private scratch.
func regressionInventoryManager(t *testing.T) (*Runs, string) {
	t.Helper()
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	alias := filepath.Join(parent, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: filepath.Join(alias, "state"), WingID: "review-wing"}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Runs{db: db, service: &Service{Config: cfg}, records: map[string]*Run{}}, filepath.Join(real, "state")
}

func regressionInventorySeed(t *testing.T, m *Runs, id, cwd string, a Authority, status string, at time.Time) *Run {
	t.Helper()
	r := &Run{ID: id, SessionID: "session-" + id, Agent: "codex", CWD: cwd, CreatedAt: at, Phase: "terminal", Launch: RunLaunch{Authority: a}, Result: egg.RunTurnResult{RunID: id, SessionID: "session-" + id, Status: status, Text: "durable-output", EndedAt: at}}
	if !r.Result.Terminal() {
		r.Phase = "observing"
		r.Result.EndedAt = time.Time{}
	}
	wire, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := m.db.AdmitAgentRun(&store.AgentRun{ID: id, SessionID: r.SessionID, Principal: a.Principal, SpecHash: id, Record: wire}, r.task())
	if err != nil {
		t.Fatal(err)
	}
	if err = m.db.SaveAgentRun(row, r.task(), "", ""); err != nil {
		t.Fatal(err)
	}
	r.revision = row.Revision
	m.records[id] = r
	return r
}

func TestRunInventoryRegressionForeignPartialRecordMustNotAffectOwnedInventory(t *testing.T) {
	for _, boundary := range []string{"path", "user"} {
		t.Run(boundary, func(t *testing.T) {
			m, _ := regressionInventoryManager(t)
			now := time.Now().UTC()
			a := Authority{Principal: "reader", UserID: "user-a", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}
			regressionInventorySeed(t, m, "mine", "/allowed", a, "done", now)
			otherAuth := a
			otherCwd := "/allowed"
			if boundary == "path" {
				otherCwd = "/forbidden"
			} else {
				otherAuth.UserID = "user-b"
			}
			foreign := regressionInventorySeed(t, m, "foreign", otherCwd, otherAuth, "done", now)
			if m.owned(a, foreign) {
				t.Fatal("independent ownership oracle accepted foreign record")
			}
			first, err := m.List(t.Context(), a, RunInventoryRequest{})
			if err != nil || first.Count != 1 {
				t.Fatalf("valid baseline: %+v %v", first, err)
			}
			// Preserve cwd and Authority so authorization still has complete evidence.
			if _, err = m.db.DB().Exec("UPDATE agent_runs SET record=json_remove(record,'$.run_id') WHERE id='foreign'"); err != nil {
				t.Fatal(err)
			}
			page, err := m.List(t.Context(), a, RunInventoryRequest{})
			if err != nil || page.Count != 1 || page.Runs[0].ID != "mine" {
				t.Fatalf("foreign partial record blocked own run; want [mine], got count=%d error=%v", page.Count, err)
			}
		})
	}
}

func TestRunInventoryRegressionRestartCursorAcceptsSameStoreThroughParentAlias(t *testing.T) {
	m, real := regressionInventoryManager(t)
	a := Authority{Principal: "reader", UserID: "user-a"}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b"} {
		regressionInventorySeed(t, m, id, "/work", a, "done", now)
	}
	first, err := m.List(t.Context(), a, RunInventoryRequest{Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	if err = m.db.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: real, WingID: "review-wing"}
	reopened, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next := &Runs{db: reopened, service: &Service{Config: cfg}}
	page, err := next.List(t.Context(), a, RunInventoryRequest{Limit: 1, Cursor: first.NextCursor})
	if err != nil || page.Count != 1 || page.Runs[0].ID != "b" {
		t.Fatalf("same committed database/key rejected after alias reopen; want b, got count=%d error=%v invalid=%v", page.Count, err, errors.Is(err, fs.ErrInvalid))
	}
	if page.SnapshotRevision != first.SnapshotRevision {
		t.Fatal("alias changed the snapshot identity")
	}
	// A copied database with the same wing ID, key and rows is still a
	// different physical state directory and must reject the original cursor.
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(t.TempDir(), "copied-state")
	if err = os.CopyFS(copyDir, os.DirFS(real)); err != nil {
		t.Fatal(err)
	}
	copyCfg := &config.Config{Dir: copyDir, WingID: cfg.WingID}
	copyDB, err := store.Open(copyCfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	copyManager := &Runs{db: copyDB, service: &Service{Config: copyCfg}}
	if _, err = copyManager.List(t.Context(), a, RunInventoryRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("different physical store accepted the alias cursor: %v", err)
	}
}

func TestRunInventoryRegressionPartialProjectionOwnerCannotBecomeEmptySuccess(t *testing.T) {
	m, _ := regressionInventoryManager(t)
	a := Authority{Principal: "reader", UserID: "user-a"}
	now := time.Now().UTC()
	regressionInventorySeed(t, m, "durable", "/work", a, "done", now)
	if _, err := m.db.DB().Exec("UPDATE tasks SET principal='' WHERE id='durable'"); err != nil {
		t.Fatal(err)
	}
	known, err := m.Get(a, "durable")
	if err != nil || known.ID != "durable" {
		t.Fatalf("independent known-ID oracle: %+v %v", known, err)
	}
	page, err := m.List(t.Context(), a, RunInventoryRequest{})
	if err != nil {
		if !strings.Contains(err.Error(), "discovery is incomplete") {
			t.Fatalf("expected an explicit incomplete-state error: %v", err)
		}
	} else if page.Count != 1 || page.Runs[0].ID != known.ID {
		t.Fatal("durable owner-authorized run was silently omitted after projection principal lost; want run or incomplete-state error")
	}
}
