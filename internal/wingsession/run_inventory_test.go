package wingsession

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func inventoryManager(t *testing.T) *Runs {
	t.Helper()
	cfg := &config.Config{Dir: testsocket.Dir(t), WingID: "inventory-wing"}
	if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Runs{db: db, service: &Service{Config: cfg}}
}

func seedInventoryRun(t *testing.T, m *Runs, id string, a Authority, at time.Time, status, key string) {
	t.Helper()
	r := &Run{ID: id, SessionID: "session-" + id, Agent: "codex", Model: "model", CWD: "/work", CreatedAt: at, Phase: "admitted", Launch: RunLaunch{Authority: a}, RequestKey: key, SpecHash: id, Result: egg.RunTurnResult{RunID: id, Status: status, Text: "result"}}
	if r.Result.Terminal() {
		r.Phase = "terminal"
		r.Result.EndedAt = at
	}
	record, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := m.db.AdmitAgentRun(&store.AgentRun{ID: id, SessionID: r.SessionID, Principal: a.Principal, RequestKey: key, SpecHash: id, Record: record}, r.task())
	if err != nil {
		t.Fatal(err)
	}
	if r.Result.Terminal() {
		if err = m.db.SaveAgentRun(row, r.task(), "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunInventoryCursorSurvivesReopenAndRejectsForeignStale(t *testing.T) {
	m := inventoryManager(t)
	now := time.Now().UTC()
	a := Authority{Principal: "owner", UserID: "user", EnforcePaths: true, AllowedPaths: []string{"/work"}}
	for _, id := range []string{"a", "b", "c"} {
		seedInventoryRun(t, m, id, a, now, "done", "")
	}
	first, err := m.listInventoryAt(t.Context(), a, RunInventoryRequest{Limit: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Count != 1 || first.NextCursor == "" {
		t.Fatalf("no continuation: %+v", first)
	}
	if err = m.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(m.service.Config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next := &Runs{db: reopened, service: m.service}
	req := RunInventoryRequest{Limit: 2, Cursor: first.NextCursor}
	second, err := next.listInventoryAt(t.Context(), a, req, now.Add(time.Minute))
	if err != nil || second.Count != 2 || second.Runs[0].ID != "b" || second.SnapshotRevision != first.SnapshotRevision {
		t.Fatalf("restart changed scan: %+v %v", second, err)
	}
	for _, tc := range []struct {
		name string
		a    Authority
		req  RunInventoryRequest
		at   time.Time
	}{
		{"user", Authority{Principal: a.Principal, UserID: "other"}, req, now},
		{"principal", Authority{Principal: "other", UserID: a.UserID}, req, now},
		{"paths", Authority{Principal: a.Principal, UserID: a.UserID, EnforcePaths: true, AllowedPaths: []string{"/other"}}, req, now},
		{"filter", a, RunInventoryRequest{Cursor: first.NextCursor, RequestKey: "other-key"}, now},
		{"window", a, RunInventoryRequest{Cursor: first.NextCursor, TerminalWindowSeconds: 3600}, now},
		{"stale", a, req, now.Add(24 * time.Hour)},
		{"future", a, req, now.Add(-time.Minute)},
		{"malformed", a, RunInventoryRequest{Cursor: "not-a-cursor"}, now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := next.listInventoryAt(t.Context(), tc.a, tc.req, tc.at); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("foreign/stale accepted: %v", err)
			}
		})
	}
	other := inventoryManager(t)
	if _, err = other.List(t.Context(), a, req); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("other wing accepted: %v", err)
	}
	t.Log("3 durable IDs, same snapshot after reopen; 9 invalid/foreign cursor cases rejected")
}

func TestRunInventoryLostAdmissionIsReadOnlyWithoutManagerMemory(t *testing.T) {
	m := inventoryManager(t)
	now := time.Now().UTC()
	a := Authority{Principal: "owner", UserID: "user"}
	seedInventoryRun(t, m, "unacknowledged", a, now, "pending", "original-key")
	// There is deliberately no manager.records entry and no worker/backend.
	page, err := m.List(context.Background(), a, RunInventoryRequest{RequestKey: "original-key"})
	if err != nil || page.Count != 1 || page.Runs[0].ID != "unacknowledged" || page.Runs[0].Phase != "admitted" {
		t.Fatalf("lost receipt: %+v %v", page, err)
	}
	var count int
	if err = m.db.DB().QueryRow("SELECT count(*) FROM agent_runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("read changed admissions: %d %v", count, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = m.List(ctx, a, RunInventoryRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan became empty success: %v", err)
	}
	if len(m.records) != 0 {
		t.Fatal("inventory instantiated runtime state")
	}
}

func TestRunInventoryIdentityUnavailableIsError(t *testing.T) {
	m := inventoryManager(t)
	m.service.Config = &config.Config{Dir: filepath.Join(m.service.Config.Dir, "missing")}
	if _, err := m.List(t.Context(), Authority{Principal: "owner"}, RunInventoryRequest{}); err == nil {
		t.Fatal("missing identity returned completed empty list")
	}
}
