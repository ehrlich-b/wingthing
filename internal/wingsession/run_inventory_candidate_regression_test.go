// Regressions adapted from the independent round-3 candidate probes.
package wingsession

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestRunInventoryRegressionLostFinishProjectionCannotExpireRecentDurableCompletion(t *testing.T) {
	m, _ := regressionInventoryManager(t)
	a := Authority{Principal: "reader", UserID: "user-a"}
	now := time.Now().UTC()
	r := regressionInventorySeed(t, m, "long-run", "/work", a, "running", now.Add(-60*24*time.Hour))
	r.Result.Status = "done"
	r.Result.EndedAt = now
	r.Phase = "terminal"
	wire, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	row := &store.AgentRun{ID: r.ID, Record: wire, Revision: r.revision}
	if e = m.db.SaveAgentRun(row, r.task(), "completed", ""); e != nil {
		t.Fatal(e)
	}
	first, e := m.List(t.Context(), a, RunInventoryRequest{TerminalWindowSeconds: 3600})
	if e != nil || first.Count != 1 || first.Runs[0].FinishedAt == "" {
		t.Fatalf("valid baseline: %+v %v", first, e)
	}
	if _, e = m.db.DB().Exec("UPDATE tasks SET finished_at=NULL WHERE id='long-run'"); e != nil {
		t.Fatal(e)
	}
	if e = m.db.Close(); e != nil {
		t.Fatal(e)
	}
	if e = m.service.StartRuns(t.Context()); e != nil {
		t.Fatal(e)
	}
	defer m.service.RunManager.Close()
	known, e := m.service.RunManager.Get(a, r.ID)
	if e != nil || known.Result.Status != "done" || !known.Result.EndedAt.Equal(now) {
		t.Fatalf("durable known-ID oracle: %+v %v", known, e)
	}
	page, e := m.service.RunManager.List(t.Context(), a, RunInventoryRequest{TerminalWindowSeconds: 3600})
	if e != nil && !strings.Contains(e.Error(), "discovery is incomplete") {
		t.Fatalf("expected explicit incomplete retention evidence: %v", e)
	}
	if e == nil && (page.Count != 1 || page.Runs[0].ID != known.ID) {
		t.Fatalf("recent terminal receipt at %s silently expired by losing nullable task finished_at; created=%s; complete empty page=%+v", known.Result.EndedAt, known.CreatedAt, page)
	}
}

func TestRunInventoryRegressionLostRequestKeyProjectionCannotBecomeAbsentAdmission(t *testing.T) {
	m, _ := regressionInventoryManager(t)
	a := Authority{Principal: "reader", UserID: "user-a"}
	now := time.Now().UTC()
	r := &Run{ID: "accepted", SessionID: "session-accepted", Agent: "codex", CWD: "/work", CreatedAt: now, Phase: "terminal", Launch: RunLaunch{Authority: a}, RequestKey: "original-key", Result: egg.RunTurnResult{RunID: "accepted", SessionID: "session-accepted", Status: "done", Text: "durable output", EndedAt: now}}
	wire, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	row, _, e := m.db.AdmitAgentRun(&store.AgentRun{ID: r.ID, SessionID: r.SessionID, Principal: a.Principal, RequestKey: r.RequestKey, SpecHash: r.ID, Record: wire}, r.task())
	if e != nil {
		t.Fatal(e)
	}
	if e = m.db.SaveAgentRun(row, r.task(), "key-fixture", ""); e != nil {
		t.Fatal(e)
	}
	first, e := m.List(t.Context(), a, RunInventoryRequest{RequestKey: "original-key"})
	if e != nil || first.Count != 1 {
		t.Fatalf("valid key baseline: %+v %v", first, e)
	}
	if _, e = m.db.DB().Exec("UPDATE agent_runs SET request_key=NULL WHERE id='accepted'"); e != nil {
		t.Fatal(e)
	}
	if e = m.db.Close(); e != nil {
		t.Fatal(e)
	}
	if e = m.service.StartRuns(t.Context()); e != nil {
		t.Fatal(e)
	}
	defer m.service.RunManager.Close()
	known, e := m.service.RunManager.Get(a, r.ID)
	if e != nil || known.RequestKey != "original-key" {
		t.Fatalf("durable known-ID key oracle: %+v %v", known, e)
	}
	all, e := m.service.RunManager.List(t.Context(), a, RunInventoryRequest{})
	if e != nil && !strings.Contains(e.Error(), "discovery is incomplete") || e == nil && (all.Count != 1 || all.Runs[0].ID != known.ID) {
		t.Fatalf("ordinary inventory must retain admission or report incomplete evidence: %+v %v", all, e)
	}
	page, e := m.service.RunManager.List(t.Context(), a, RunInventoryRequest{RequestKey: "original-key"})
	if e != nil && !strings.Contains(e.Error(), "discovery is incomplete") {
		t.Fatalf("expected explicit incomplete admission evidence: %v", e)
	}
	if e == nil && (page.Count != 1 || page.Runs[0].ID != known.ID) {
		t.Fatalf("durable accepted request with key %q treated as absent after nullable key projection lost: %+v", known.RequestKey, page)
	}
}
