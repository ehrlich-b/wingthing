package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/runinventory"
	"github.com/ehrlich-b/wingthing/internal/store"
	"testing"
	"time"
)

func TestRunInventoryConsumerDiscoversRealFixtureIDsAndResults(t *testing.T) {
	f := prepareListFixture(t)
	completed := f.admit(t, "completed request")
	<-f.submitted
	running := f.admit(t, "still running")
	<-f.submitted
	interrupted := f.admit(t, "interrupted request")
	<-f.submitted
	f.finish(t, completed, "completed result Ω", "")
	f.wait(t, completed)
	f.finish(t, interrupted, "", agent.UnknownOutcome)
	f.wait(t, interrupted)
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	err = db.CreateTask(&store.Task{ID: "legacy-unavailable", Type: "agent_run", Principal: "owner", Status: "done", CWD: f.server.Cfg.Dir, RunAt: time.Now()})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	calls := map[string]int{}
	readIDs := map[string]bool{}
	call := func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		calls[name]++
		var target struct {
			ID string `json:"run_id"`
		}
		_ = json.Unmarshal(args, &target)
		if target.ID != "" {
			readIDs[target.ID] = true
		}
		data, denied, err := serializedInventoryCall(t, f.server, name, args)
		if denied && err == nil {
			err = fmt.Errorf("tool denied: %v", data["error"])
		}
		return data, err
	}
	recovered, err := runinventory.Recover(t.Context(), call, runinventory.Options{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	counts := recovered.Counts
	if !recovered.Complete || recovered.Pages != 2 || counts.Total != 4 || counts.Completed != 1 || counts.Running != 1 || counts.Interrupted != 1 || counts.UnavailableOutput != 1 || counts.UnknownCleanup != 4 {
		t.Fatalf("consumer reconciliation: %+v", recovered)
	}
	found := map[string]runinventory.RecoveredRun{}
	for _, r := range recovered.Runs {
		found[r.ID] = r
	}
	if found[completed].Output != "completed result Ω" || found[running].State != "running" || found[interrupted].State != "interrupted" || found["legacy-unavailable"].State != "unavailable_output" {
		t.Fatalf("real-ID outcomes: %+v", found)
	}
	if len(readIDs) != 4 || calls["agent_list"] != 2 || calls["agent_status"] != 4 || calls["agent_result"] != 3 || len(calls) != 3 {
		t.Fatalf("consumer launched/polled/skipped: %v %v", calls, readIDs)
	}
	t.Logf("consumer oracle: 4 real IDs, 2 pages, 4 status + 3 result calls; completed=1 running=1 interrupted=1 unavailable_output=1 unknown_cleanup=4")
}
