package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/runinventory"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestRunInventoryReopenRetentionRemovedSessionsAndFiniteConsumer(t *testing.T) {
	s, _ := committedInventoryServer(t, "retention-wing", func(db *store.Store, cfg *config.Config) {
		principal := wingsession.UserPrincipal("wing-user")
		now := time.Now().UTC()
		zero := int64(0)
		commitInventoryRun(t, db, "recent-output", principal, "wing-user", cfg.Dir, "done", "durable result after archive", now.Add(-time.Minute), nil)
		commitInventoryRun(t, db, "expired-task", principal, "wing-user", cfg.Dir, "done", "task retention does not delete run", now.Add(-time.Minute), &wingsession.MCPTaskRecord{RetentionMillis: &zero})
		commitInventoryRun(t, db, "interrupted", principal, "wing-user", cfg.Dir, "failed", "", now.Add(-time.Minute), nil)
		commitInventoryRun(t, db, "outside-retention", principal, "wing-user", cfg.Dir, "done", "cold history", now.Add(-60*24*time.Hour), nil)
		if err := db.CreateTask(&store.Task{ID: "legacy-output-unavailable", Type: "agent_run", Principal: principal, CWD: cfg.Dir, RunAt: now, Status: "done"}); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(cfg.Dir, "eggs", "session-recent-output")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "run.result.json"), []byte("archived output must not be recreated"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(cfg.Dir, "eggs")); err != nil {
			t.Fatal(err)
		}
	})
	authority := s.sessionAuthority()
	if tasks := s.Sessions.RunManager.Tasks(authority); len(tasks) != 0 {
		t.Fatalf("expired task retained: %+v", tasks)
	}
	if _, err := s.Sessions.RunManager.Task(authority, "expired-task"); err == nil {
		t.Fatal("expired native task became visible")
	}
	old := s.Sessions
	if err := old.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	restored := &wingsession.Service{Config: old.Config, Home: old.Home, Policy: old.Policy, Spawn: old.Spawn}
	if err := restored.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.RunManager.Close() })
	s.Sessions = restored
	db, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logsBefore int
	if err = db.DB().QueryRow("SELECT count(*) FROM task_log").Scan(&logsBefore); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	results := map[string]int{}
	call := func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		calls[name]++
		var target struct {
			ID string `json:"run_id"`
		}
		_ = json.Unmarshal(args, &target)
		if target.ID == "outside-retention" {
			t.Fatal("consumer polled archived terminal ID outside discovery")
		}
		if name == "agent_result" {
			results[target.ID]++
			if results[target.ID] > 1 {
				t.Fatal("consumer polled terminal result")
			}
		}
		data, denied, err := serializedInventoryCall(t, s, name, args)
		if denied && err == nil {
			err = fmt.Errorf("tool denied: %v", data["error"])
		}
		return data, err
	}
	recovered, err := runinventory.Recover(t.Context(), call, runinventory.Options{Limit: 2, TerminalWindowSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	c := recovered.Counts
	if !recovered.Complete || c.Total != 4 || c.Completed != 2 || c.Interrupted != 1 || c.UnavailableOutput != 1 || c.UnknownCleanup != 4 || calls["agent_list"] != 2 || calls["agent_status"] != 4 || calls["agent_result"] != 4 || len(calls) != 3 {
		t.Fatalf("reopen/retention oracle: %+v %v", recovered, calls)
	}
	var logsAfter int
	if err = db.DB().QueryRow("SELECT count(*) FROM task_log").Scan(&logsAfter); err != nil {
		t.Fatal(err)
	}
	if logsAfter != logsBefore {
		t.Fatalf("read mutated run lifecycle: %d -> %d", logsBefore, logsAfter)
	}
	if _, err = os.Stat(filepath.Join(s.Cfg.Dir, "eggs")); !os.IsNotExist(err) {
		t.Fatalf("consumer recreated session/output archive: %v", err)
	}
	exact, err := s.toolAgentList(t.Context(), json.RawMessage(`{"terminal_window_seconds":3600}`))
	if err != nil || exact["count"] != float64(4) {
		t.Fatalf("post-consumer inventory changed: %v %v", exact, err)
	}
	t.Log("reopen oracle: 5 retained records, 4 discovered; expired MCP task remains a run; 2 pages/4 statuses/4 one-shot terminal results; no archived session/output recreation or run lifecycle writes")
}
