package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func inventoryFixture(t *testing.T, s *Store, id, owner, user, cwd, status, key string, at time.Time) *AgentRun {
	t.Helper()
	phase := "observing"
	end := time.Time{}
	if status == "done" || status == "failed" || status == "timeout" || status == "stopped" {
		phase, end = "terminal", at
	}
	record, _ := json.Marshal(map[string]any{"run_id": id, "session_id": "s-" + id, "idempotency_key": key, "agent": "codex", "model": "model", "cwd": cwd, "isolation": "none", "timeout_seconds": 0, "created_at": at, "phase": phase, "prompt": "SECRET PROMPT", "launch": map[string]any{"Authority": map[string]any{"Principal": owner, "UserID": user}}, "result": map[string]any{"status": status, "text": "SECRET OUTPUT", "started_at": time.Time{}, "ended_at": end}})
	task := &Task{ID: id, Principal: owner, CWD: cwd, Agent: "codex", Model: "model", Status: status, RunAt: at, CreatedAt: at}
	r, _, err := s.AdmitAgentRun(&AgentRun{ID: id, SessionID: "s-" + id, Principal: owner, RequestKey: key, SpecHash: id, Record: record}, task)
	if err != nil {
		t.Fatal(err)
	}
	if status == "done" || status == "failed" || status == "timeout" || status == "stopped" {
		task.FinishedAt = &at
		if err = s.SaveAgentRun(r, task, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestRunInventoryEqualTimestampsOwnersPathsAndReplacementPages(t *testing.T) {
	s := inventoryTestStore(t)
	at := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"c", "a", "b", "d"} {
		inventoryFixture(t, s, id, "owner", "user", "/allowed/project", "running", "key-"+id, at)
	}
	inventoryFixture(t, s, "0-foreign", "other", "user", "/allowed", "running", "", at)
	inventoryFixture(t, s, "0-user", "owner", "other-user", "/allowed", "running", "", at)
	inventoryFixture(t, s, "0-path", "owner", "user", "/forbidden", "running", "", at)
	inventoryFixture(t, s, "0-traversal", "owner", "user", "/allowed/../forbidden", "running", "", at)
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}, Limit: 2, TerminalSince: at.Add(-time.Hour)}
	seen := []string{}
	for {
		page, err := s.ListRunInventory(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Runs) != 2 {
			t.Fatalf("denied rows occupied page: %+v", page)
		}
		for _, r := range page.Runs {
			seen = append(seen, r.ID)
		}
		if !page.More {
			break
		}
		q.Continuing = true
		q.Position = page.Position
	}
	if !reflect.DeepEqual(seen, []string{"a", "b", "c", "d"}) {
		t.Fatalf("incomplete/duplicate set: %v", seen)
	}
	// Same-owner new executions remain separate IDs; an admission after the
	// initial watermark cannot sneak into later pages.
	q.Continuing = false
	q.Position = RunInventoryPosition{}
	first, err := s.ListRunInventory(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	inventoryFixture(t, s, "replacement", "owner", "user", "/allowed", "running", "replacement-key", at)
	q.Continuing = true
	q.Position = first.Position
	last, err := s.ListRunInventory(context.Background(), q)
	if err != nil || len(last.Runs) != 2 || last.More || last.Runs[1].ID != "d" {
		t.Fatalf("watermark: %+v %v", last, err)
	}
}

func TestRunInventoryRetentionLegacyAndExactKey(t *testing.T) {
	s := inventoryTestStore(t)
	now := time.Now().UTC()
	inventoryFixture(t, s, "old-active", "owner", "user", "/work", "running", "active", now.Add(-60*24*time.Hour))
	inventoryFixture(t, s, "old-done", "owner", "user", "/work", "done", "old-key", now.Add(-60*24*time.Hour))
	inventoryFixture(t, s, "recent", "owner", "user", "/work", "done", "recent-key", now)
	for _, id := range []string{"legacy-missing", "legacy-output"} {
		if err := s.CreateTask(&Task{ID: id, Type: "agent_run", Agent: "claude", Principal: "default", CWD: "/work", Status: "done", RunAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec("UPDATE tasks SET output='legacy result' WHERE id='legacy-output'"); err != nil {
		t.Fatal(err)
	}
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user", LegacyLocalDefault: true}, Limit: 200, TerminalSince: now.Add(-7 * 24 * time.Hour)}
	page, err := s.ListRunInventory(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]RunInventoryItem{}
	for _, r := range page.Runs {
		ids[r.ID] = r
	}
	if len(ids) != 4 || ids["old-active"].Terminal || ids["legacy-missing"].OutputAvailability != "unavailable" || ids["legacy-output"].OutputAvailability != "available" {
		t.Fatalf("retention/legacy: %+v", ids)
	}
	q.RequestKey = "old-key"
	page, err = s.ListRunInventory(context.Background(), q)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "old-done" {
		t.Fatalf("exact old admission: %+v %v", page, err)
	}
	q.RequestKey = "nonexistent"
	page, err = s.ListRunInventory(context.Background(), q)
	if err != nil || len(page.Runs) != 0 {
		t.Fatalf("missing key: %+v %v", page, err)
	}
	q.Limit = 201
	if _, err = s.ListRunInventory(context.Background(), q); err == nil {
		t.Fatal("unbounded page")
	}
}

func TestRunInventoryTransitionStaleAnchorAndReadFailure(t *testing.T) {
	s := inventoryTestStore(t)
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		inventoryFixture(t, s, id, "owner", "user", "/work", "running", "", now.Add(-60*24*time.Hour))
	}
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user"}, Limit: 1, TerminalSince: now.Add(-7 * 24 * time.Hour)}
	first, err := s.ListRunInventory(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.GetAgentRun("b")
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal(row.Record, &record); err != nil {
		t.Fatal(err)
	}
	record["phase"] = "terminal"
	result := record["result"].(map[string]any)
	result["status"], result["ended_at"] = "done", now
	row.Record, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.GetTask("b")
	if err != nil {
		t.Fatal(err)
	}
	task.Status, task.FinishedAt = "done", &now
	if err = s.SaveAgentRun(row, task, "completed", ""); err != nil {
		t.Fatal(err)
	}
	q.Continuing = true
	q.Position = first.Position
	second, err := s.ListRunInventory(context.Background(), q)
	if err != nil || len(second.Runs) != 1 || second.Runs[0].ID != "b" || !second.Runs[0].Terminal {
		t.Fatalf("completion disappeared: %+v %v", second, err)
	}
	if _, err = s.DB().Exec("DELETE FROM agent_runs WHERE id='c'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec("DELETE FROM task_log WHERE task_id='c'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec("DELETE FROM tasks WHERE id='c'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListRunInventory(context.Background(), q); !errors.Is(err, ErrRunInventoryCursor) {
		t.Fatalf("removed anchor: %v", err)
	}
	s.Close()
	q.Continuing = false
	if _, err = s.ListRunInventory(context.Background(), q); err == nil {
		t.Fatal("closed store returned empty success")
	}
}

func TestRunInventoryMetadataProjectionAndNonzeroFixture(t *testing.T) {
	s := inventoryTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < 250; i++ {
		inventoryFixture(t, s, fmt.Sprintf("run-%03d", i), "owner", "user", "/work", "done", "", now)
	}
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user"}, Limit: 100, TerminalSince: now.Add(-time.Hour)}
	count := 0
	pages := 0
	for {
		p, err := s.ListRunInventory(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(p.Runs)
		var fields []map[string]any
		if err = json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		for _, r := range fields {
			for _, key := range []string{"prompt", "output", "text", "launch", "error", "provider_session_id", "turn_id", "usage"} {
				if _, ok := r[key]; ok {
					t.Fatalf("content field %s", key)
				}
			}
		}
		count += len(p.Runs)
		pages++
		if !p.More {
			break
		}
		q.Continuing = true
		q.Position = p.Position
	}
	if count != 250 || pages != 3 {
		t.Fatalf("fixture counts %d/%d", count, pages)
	}
	t.Logf("independent store oracle: %d runs in %d pages", count, pages)
}

func inventoryTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRunInventoryQueryPlanUsesBoundedHistoryIndexes(t *testing.T) {
	s := inventoryTestStore(t)
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user"}, Limit: 100, TerminalSince: time.Now().Add(-time.Hour)}
	prefix, from, args := inventorySelection(q)
	scope, scopeArgs := inventoryScopeSQL(q.Scope)
	args = append(args, scopeArgs...)
	rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+prefix+"SELECT t.id"+from+scope+" ORDER BY t.created_at,t.id LIMIT 201", args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := ""
	durableOwnerIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details += detail + "\n"
		if strings.Contains(detail, "SEARCH a USING INDEX ") && strings.Contains(detail, "(principal=?)") {
			durableOwnerIndex = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"idx_tasks_run_inventory_active", "idx_tasks_run_inventory_recent"} {
		if !strings.Contains(details, "SEARCH tasks USING INDEX "+index) {
			t.Fatalf("missing bounded search %s: %s", index, details)
		}
	}
	if strings.Contains(details, "SCAN tasks") {
		t.Fatalf("unbounded task history: %s", details)
	}
	if !durableOwnerIndex || !strings.Contains(details, "idx_tasks_run_inventory_recent (principal=? AND <expr>=?)") {
		t.Fatalf("missing indexed durable-owner or unknown-time search: %s", details)
	}
	t.Log(details)
}

func TestRunInventoryCorruptAdmissionCannotBecomeEmptySuccess(t *testing.T) {
	s := inventoryTestStore(t)
	now := time.Now().UTC()
	inventoryFixture(t, s, "corrupt", "owner", "user", "/work", "running", "", now)
	if _, err := s.DB().Exec("UPDATE agent_runs SET record='{}' WHERE id='corrupt'"); err != nil {
		t.Fatal(err)
	}
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user"}, Limit: 100, TerminalSince: now.Add(-time.Hour)}
	if _, err := s.ListRunInventory(t.Context(), q); err == nil {
		t.Fatal("unavailable admitted ID reported as complete empty list")
	}
}

func TestRunInventoryBoundedHistorySkipsArchivedContentAndHandlesLegacyTimes(t *testing.T) {
	s := inventoryTestStore(t)
	now := time.Now().UTC()
	inventoryFixture(t, s, "archived", "owner", "user", "/work", "done", "old-key", now.Add(-60*24*time.Hour))
	// Archived malformed JSON is outside routine discovery's query, so a cold
	// history record cannot break or enter a current inventory scan.
	if _, err := s.DB().Exec("UPDATE agent_runs SET record='corrupt archived content' WHERE id='archived'"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&Task{ID: "sqlite-time", Type: "agent_run", Agent: "claude", Principal: "owner", Status: "done", CWD: "/work", RunAt: now}); err != nil {
		t.Fatal(err)
	}
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "owner", UserID: "user"}, Limit: 100, TerminalSince: now.Add(-time.Hour)}
	start := time.Now()
	page, err := s.ListRunInventory(t.Context(), q)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "sqlite-time" {
		t.Fatalf("history bound/SQLite time: %+v %v", page, err)
	}
	t.Logf("bounded active/recent scan returned 1 legacy run with archived content excluded in %s", time.Since(start))
	q.RequestKey = "old-key"
	if _, err = s.ListRunInventory(t.Context(), q); err == nil {
		t.Fatal("exact-key scan swallowed corrupt admission")
	}
}
