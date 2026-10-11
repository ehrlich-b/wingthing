package localmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/runinventory"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestRunInventoryMissingFilterProjectionCannotCompleteRecovery(t *testing.T) {
	for _, field := range []string{"finished_at", "idempotency_key"} {
		t.Run(field, func(t *testing.T) {
			var s *Server
			var id string
			var options runinventory.Options
			if field == "finished_at" {
				id, options.TerminalWindowSeconds = "long-run", 3600
				s, _ = committedInventoryServer(t, "projection-wing", func(db *store.Store, cfg *config.Config) {
					now := time.Now().UTC()
					commitInventoryRun(t, db, id, wingsession.UserPrincipal("wing-user"), "wing-user", cfg.Dir, "done", "durable output", now, nil)
					row, err := db.GetAgentRun(id)
					if err != nil {
						t.Fatal(err)
					}
					var r wingsession.Run
					if err = json.Unmarshal(row.Record, &r); err != nil {
						t.Fatal(err)
					}
					// Admission age and finish time are independent. Match the
					// historical projection to a genuinely long-lived receipt.
					created := now.Add(-60 * 24 * time.Hour)
					r.CreatedAt = created
					row.Record, err = json.Marshal(r)
					if err != nil {
						t.Fatal(err)
					}
					projection, err := db.GetTask(id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.DB().Exec("UPDATE tasks SET created_at=? WHERE id=?", created.Format(time.RFC3339), id); err != nil {
						t.Fatal(err)
					}
					if err = db.SaveAgentRun(row, projection, "long-run-fixture", ""); err != nil {
						t.Fatal(err)
					}
				})
			} else {
				f := prepareListFixture(t)
				s = f.server
				options.RequestKey = "original-key"
				args, err := json.Marshal(map[string]any{"agent": "claude", "cwd": s.Cfg.Dir, "prompt": "accepted request", "idempotency_key": options.RequestKey})
				if err != nil {
					t.Fatal(err)
				}
				data, err := s.toolAgentRunTask(args, &control.MCPTaskParams{})
				if err != nil {
					t.Fatal(err)
				}
				id = data["run_id"].(string)
				<-f.submitted
				f.finish(t, id, "durable output", "")
				f.wait(t, id)
			}
			args, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			baseline, denied, err := serializedInventoryCall(t, s, "agent_list", args)
			if err != nil || denied || baseline["count"] != float64(1) {
				t.Fatalf("valid serialized baseline: %v %v", baseline, err)
			}
			db, err := store.Open(s.Cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			mutation := "UPDATE tasks SET finished_at=NULL WHERE id=?"
			if field == "idempotency_key" {
				mutation = "UPDATE agent_runs SET request_key=NULL WHERE id=?"
			}
			_, err = db.DB().Exec(mutation, id)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			previous := s.Sessions
			if err = previous.RunManager.Close(); err != nil {
				t.Fatal(err)
			}
			restored := &wingsession.Service{Config: previous.Config, Home: previous.Home, Policy: previous.Policy, RunBackend: previous.RunBackend, Spawn: previous.Spawn}
			if err = restored.StartRuns(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Existing cleanup owns this service; transfer it the reopened manager.
			previous.RunManager = restored.RunManager
			s.Sessions = restored
			known, err := restored.RunManager.Get(s.sessionAuthority(), id)
			if err != nil || known.Result.Status != "done" || known.Result.Text != "durable output" {
				t.Fatalf("independent known-ID oracle: %+v %v", known, err)
			}
			calls := 0
			call := func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
				calls++
				if name != "agent_list" {
					t.Fatalf("incomplete discovery polled a run: %s", name)
				}
				data, denied, err := serializedInventoryCall(t, s, name, args)
				if denied && err == nil {
					err = control.ToolError(data)
				}
				return data, err
			}
			recovered, err := runinventory.Recover(t.Context(), call, options)
			if err == nil || !strings.Contains(err.Error(), "discovery is incomplete") || recovered.Complete || recovered.Counts.Total != 0 || calls != 1 {
				t.Fatalf("unavailable %s must not become completed empty recovery: %+v calls=%d error=%v", field, recovered, calls, err)
			}
		})
	}
}
