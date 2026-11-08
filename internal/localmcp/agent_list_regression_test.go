package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/runinventory"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestRunInventoryRegressionRecoverDoesNotReportCancelledMCPResultAsCompleted(t *testing.T) {
	parent := testsocket.Dir(t)
	real := filepath.Join(parent, "real")
	alias := filepath.Join(parent, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: filepath.Join(alias, "state"), WingID: "cancel-wing", DefaultAgent: "codex"}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.EnsureKeyPair(cfg.Dir); err != nil {
		t.Fatal(err)
	}
	f := newRunWingFixture(t, &Server{Version: "fixture", Cfg: cfg, Principal: "owner", Logs: io.Discard})
	admitted, err := f.server.toolAgentRunTask(json.RawMessage(`{"agent":"codex","prompt":"complete while stop is in flight"}`), &control.MCPTaskParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := admitted["run_id"].(string)
	<-f.submitted
	// Reopen lets us inject only the egg Stop transport. The real CancelTask,
	// stop-intent persistence and final-result acceptance remain unchanged.
	if err = f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	stop := f.service.RunBackend.Stop
	f.service.RunBackend.Stop = func(ctx context.Context, cfg *config.Config, sess eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
		f.finish(t, id, "actual completion before stop acknowledgement", "")
		return stop(ctx, cfg, sess, id)
	}
	if err = f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	task, err := f.service.RunManager.CancelTask(f.server.sessionAuthority(), id)
	if err != nil || task.Status != "cancelled" {
		t.Fatalf("cancel receipt: %+v %v", task, err)
	}
	f.wait(t, id)
	args, _ := json.Marshal(map[string]any{"run_id": id})
	status, err := f.server.toolAgentStatus(args)
	if err != nil || status["status"] != "done" {
		t.Fatalf("underlying completion: %v %v", status, err)
	}
	outcome, err := f.server.toolAgentResult(args)
	if err != nil || outcome["status"] != "stopped" || fmt.Sprint(outcome["failure_kind"]) != "stopped" {
		t.Fatalf("frozen MCP result: %v %v", outcome, err)
	}
	call := func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		data, denied, err := serializedInventoryCall(t, f.server, name, args)
		if denied && err == nil {
			err = fmt.Errorf("denied: %v", data["error"])
		}
		return data, err
	}
	recovered, err := runinventory.Recover(t.Context(), call, runinventory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Counts.Completed != 0 || recovered.Counts.Interrupted != 1 {
		t.Fatalf("MCP result is stopped, but recovery claims completion: %+v (status=%v result=%v)", recovered, status["status"], outcome["status"])
	}
	if !recovered.Complete || len(recovered.Runs) != 1 || recovered.Runs[0].Status != "stopped" || recovered.Runs[0].State != "interrupted" || recovered.Runs[0].Output != "" || recovered.Runs[0].ReadError != "" {
		t.Fatalf("cancelled receipt was not preserved: %+v", recovered)
	}
}
