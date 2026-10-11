package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A long-lived completion carries every optional projection we can reconcile.
// All baseline evidence is committed with the real admission/result writers.
func projectionInventoryFixture(t *testing.T, s *Store, owner, user, cwd string) {
	t.Helper()
	now := time.Now().UTC()
	created := now.Add(-60 * 24 * time.Hour)
	started := created.Add(time.Minute)
	parent := "parent"
	if err := s.CreateTask(&Task{ID: parent, Type: "prompt", RunAt: now}); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{
		"run_id": "durable", "session_id": "session-durable", "idempotency_key": "original-key",
		"agent": "codex", "model": "model", "label": "label", "cwd": cwd, "isolation": "sandbox",
		"phase": "terminal", "parent_id": parent, "timeout_seconds": 60, "created_at": created,
		"launch": map[string]any{"Authority": map[string]any{"Principal": owner, "UserID": user}},
		"result": map[string]any{"status": "done", "text": "durable output", "started_at": started, "ended_at": now,
			"failure_kind": "unknown_outcome", "containment_error": "cleanup evidence",
			"surviving_descendants": []map[string]any{{"pid": 42}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: "durable", Agent: "codex", Model: "model", CWD: cwd, Isolation: "sandbox", Principal: owner,
		Status: "done", ParentID: &parent, TimeoutSeconds: 60, CreatedAt: created, RunAt: created, StartedAt: &started, FinishedAt: &now}
	row, _, err := s.AdmitAgentRun(&AgentRun{ID: task.ID, SessionID: "session-durable", Principal: owner,
		RequestKey: "original-key", SpecHash: "fixture", Record: record}, task)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAgentRun(row, task, "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRunInventoryEveryProjectionFieldReportsIncompleteEvidence(t *testing.T) {
	mutations := map[string]string{
		"task-principal":          "UPDATE tasks SET principal='' WHERE id='durable'",
		"task-type":               "UPDATE tasks SET type='prompt' WHERE id='durable'",
		"task-status-missing":     "UPDATE tasks SET status='' WHERE id='durable'",
		"task-status-mismatch":    "UPDATE tasks SET status='pending' WHERE id='durable'",
		"task-agent":              "UPDATE tasks SET agent='' WHERE id='durable'",
		"task-model":              "UPDATE tasks SET model=x'01' WHERE id='durable'",
		"task-cwd":                "UPDATE tasks SET cwd='' WHERE id='durable'",
		"task-isolation":          "UPDATE tasks SET isolation='' WHERE id='durable'",
		"task-parent":             "UPDATE tasks SET parent_id=NULL WHERE id='durable'",
		"task-created-missing":    "UPDATE tasks SET created_at=NULL WHERE id='durable'",
		"task-created-relative":   "UPDATE tasks SET created_at='now' WHERE id='durable'",
		"task-started-missing":    "UPDATE tasks SET started_at=NULL WHERE id='durable'",
		"task-started-invalid":    "UPDATE tasks SET started_at='' WHERE id='durable'",
		"task-finished-missing":   "UPDATE tasks SET finished_at=NULL WHERE id='durable'",
		"task-finished-invalid":   "UPDATE tasks SET finished_at='' WHERE id='durable'",
		"task-finished-date-only": "UPDATE tasks SET finished_at='2026-10-11' WHERE id='durable'",
		"task-finished-numeric":   "UPDATE tasks SET finished_at=0 WHERE id='durable'",
		"task-timeout-missing":    "UPDATE tasks SET timeout_seconds='' WHERE id='durable'",
		"task-timeout-negative":   "UPDATE tasks SET timeout_seconds=-1 WHERE id='durable'",
		"task-timeout-fraction":   "UPDATE tasks SET timeout_seconds=0.5 WHERE id='durable'",
		"admission-id":            "UPDATE agent_runs SET id=NULL WHERE id='durable'",
		"admission-session":       "UPDATE agent_runs SET session_id='' WHERE id='durable'",
		"admission-principal":     "UPDATE agent_runs SET principal='' WHERE id='durable'",
		"admission-key-missing":   "UPDATE agent_runs SET request_key=NULL WHERE id='durable'",
		"admission-key-empty":     "UPDATE agent_runs SET request_key='' WHERE id='durable'",
		"admission-key-mismatch":  "UPDATE agent_runs SET request_key='replacement-key' WHERE id='durable'",
		"record-invalid":          "UPDATE agent_runs SET record='invalid JSON' WHERE id='durable'",
		"record-parent-missing":   "UPDATE agent_runs SET record=json_remove(record,'$.parent_id') WHERE id='durable'",
		"record-key-missing":      "UPDATE agent_runs SET record=json_remove(record,'$.idempotency_key') WHERE id='durable'",
		"record-user-empty":       "UPDATE agent_runs SET record=json_set(record,'$.launch.Authority.UserID','') WHERE id='durable'",
	}
	for _, field := range []string{"run_id", "session_id", "agent", "model", "cwd", "isolation", "phase", "timeout_seconds", "created_at",
		"launch.Authority.Principal", "launch.Authority.UserID", "result", "result.status", "result.text", "result.started_at", "result.ended_at"} {
		mutations["record-missing-"+field] = "UPDATE agent_runs SET record=json_remove(record,'$." + field + "') WHERE id='durable'"
		mutations["record-unusable-"+field] = "UPDATE agent_runs SET record=json_set(record,'$." + field + "',json('null')) WHERE id='durable'"
	}
	for _, field := range []string{"label", "parent_id", "idempotency_key", "result.failure_kind", "result.containment_error", "result.surviving_descendants"} {
		mutations["record-unusable-"+field] = "UPDATE agent_runs SET record=json_set(record,'$." + field + "',json('null')) WHERE id='durable'"
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			s := inventoryTestStore(t)
			projectionInventoryFixture(t, s, "reader", "user", "/allowed")
			q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", UserID: "user", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}, Limit: 1, TerminalSince: time.Now().Add(-time.Hour)}
			baseline, err := s.ListRunInventory(t.Context(), q)
			if err != nil || len(baseline.Runs) != 1 || baseline.Runs[0].ID != "durable" || baseline.Runs[0].Cleanup != "attention" {
				t.Fatalf("valid committed baseline: %+v %v", baseline, err)
			}
			if _, err = s.DB().Exec(mutation); err != nil {
				t.Fatal(err)
			}
			// Never return a full page, a partial page, or established absence when
			// any required projection is unavailable. Exact keys include history.
			for _, key := range []string{"", "original-key", "unseen-key"} {
				q.RequestKey = key
				page, err := s.ListRunInventory(t.Context(), q)
				if err == nil || !strings.Contains(err.Error(), "discovery is incomplete") || len(page.Runs) != 0 || page.More {
					t.Fatalf("unavailable evidence with key %q must be explicitly incomplete: %+v %v", key, page, err)
				}
			}
		})
	}
}

func TestRunInventoryProjectionDamageCannotExposeKnownDeniedAdmission(t *testing.T) {
	for _, boundary := range []string{"principal", "user", "cwd"} {
		for _, damage := range []string{"key", "finish", "metadata"} {
			t.Run(boundary+"/"+damage, func(t *testing.T) {
				s := inventoryTestStore(t)
				owner, user, cwd := "reader", "user", "/allowed"
				switch boundary {
				case "principal":
					owner = "other"
				case "user":
					user = "other"
				case "cwd":
					cwd = "/denied"
				}
				projectionInventoryFixture(t, s, owner, user, cwd)
				inventoryFixture(t, s, "mine", "reader", "user", "/allowed", "done", "my-key", time.Now().UTC())
				mutation := "UPDATE agent_runs SET record=json_remove(record,'$.phase') WHERE id='durable'"
				if damage == "key" {
					mutation = "UPDATE agent_runs SET request_key=NULL WHERE id='durable'"
				}
				if damage == "finish" {
					mutation = "UPDATE tasks SET finished_at=NULL WHERE id='durable'"
				}
				if _, err := s.DB().Exec(mutation); err != nil {
					t.Fatal(err)
				}
				q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", UserID: "user", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}, Limit: 1, TerminalSince: time.Now().Add(-time.Hour)}
				for _, key := range []string{"", "my-key", "original-key"} {
					q.RequestKey = key
					page, err := s.ListRunInventory(t.Context(), q)
					if err != nil || page.More {
						t.Fatalf("denied evidence affected discovery: %+v %v", page, err)
					}
					if key == "original-key" {
						if len(page.Runs) != 0 {
							t.Fatalf("denied admission leaked: %+v", page)
						}
					} else if len(page.Runs) != 1 || page.Runs[0].ID != "mine" {
						t.Fatalf("own admission lost: %+v", page)
					}
				}
			})
		}
	}
}

func TestRunInventoryLegacyUnavailableMetadataIsExplicit(t *testing.T) {
	for field, value := range map[string]string{
		"id": "NULL", "agent": "''", "model": "x'01'", "cwd": "x'01'", "isolation": "''", "status": "''",
		"created_at": "NULL", "started_at": "'now'", "finished_at": "0", "timeout_seconds": "''", "output": "x'01'",
	} {
		t.Run(field, func(t *testing.T) {
			s := inventoryTestStore(t)
			now := time.Now().UTC()
			if err := s.CreateTask(&Task{ID: "legacy", Type: "agent_run", Agent: "claude", Principal: "reader", CWD: "/allowed", Status: "done", RunAt: now}); err != nil {
				t.Fatal(err)
			}
			q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", EnforcePaths: true, AllowedPaths: []string{"/allowed"}}, Limit: 1, TerminalSince: now.Add(-time.Hour)}
			baseline, err := s.ListRunInventory(t.Context(), q)
			if err != nil || len(baseline.Runs) != 1 || !baseline.Runs[0].Legacy || baseline.Runs[0].OutputAvailability != "unavailable" {
				t.Fatalf("legacy zero values and creation-time fallback: %+v %v", baseline, err)
			}
			if _, err = s.DB().Exec("UPDATE tasks SET " + field + "=" + value + " WHERE id='legacy'"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ListRunInventory(t.Context(), q); err == nil || !strings.Contains(err.Error(), "discovery is incomplete") {
				t.Fatalf("unusable legacy %s: %v", field, err)
			}
		})
	}
}

func TestRunInventoryExactKeyReconciliationUsesOwnerIndexes(t *testing.T) {
	s := inventoryTestStore(t)
	q := RunInventoryQuery{Scope: RunInventoryScope{Principal: "reader", UserID: "user"}, RequestKey: "original-key", Limit: 1, TerminalSince: time.Now().Add(-time.Hour)}
	prefix, from, args := inventorySelection(q)
	scope, scopeArgs := inventoryScopeSQL(q.Scope)
	rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+prefix+"SELECT t.id"+from+scope, append(args, scopeArgs...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details.WriteString(detail + "\n")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := details.String()
	if strings.Contains(plan, "SCAN t") || strings.Contains(plan, "SCAN a") || strings.Contains(plan, "SCAN agent_runs") {
		t.Fatalf("key reconciliation scanned global history: %s", plan)
	}
	for _, indexed := range []string{"SEARCH agent_runs USING INDEX", "idx_tasks_run_inventory_active (principal=?)", "idx_tasks_run_inventory_recent (principal=?)"} {
		if !strings.Contains(plan, indexed) {
			t.Fatalf("missing owner index %q: %s", indexed, plan)
		}
	}
}
