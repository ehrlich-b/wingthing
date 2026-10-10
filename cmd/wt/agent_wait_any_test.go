package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestAgentWaitAnyCLI(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := "s"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", dir)
	t.Setenv("WT_MCP_CLIENT", "")
	cfg := &config.Config{Dir: dir}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []*store.Task{
		{ID: "done", Type: "agent_run", Status: "done"},
		{ID: "running", Type: "agent_run", Status: "running"},
		{ID: "owner-run", Type: "agent_run", Principal: "alice", Status: "done"},
	} {
		principal := task.Principal
		if principal == "" {
			principal = wingsession.UserPrincipal("cli-owner")
		}
		task.Principal = principal
		run := &wingsession.Run{ID: task.ID, SessionID: "session-" + task.ID, Phase: "observing", Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: principal, UserID: "cli-owner"}}, Result: egg.RunTurnResult{RunID: task.ID, SessionID: "session-" + task.ID, Status: task.Status}}
		if run.Result.Terminal() {
			run.Phase = "terminal"
		}
		wire, _ := json.Marshal(run)
		if _, _, err := db.AdmitAgentRun(&store.AgentRun{ID: task.ID, SessionID: run.SessionID, Principal: principal, SpecHash: task.ID, Record: wire}, task); err != nil {
			t.Fatal(err)
		}
	}
	closeForTest(t, "CLI fake run store", db)
	wc := &config.WingConfig{WingID: "fixture-wing"}
	service := &wingsession.Service{Config: cfg, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }, RunBackend: &wingsession.RunBackend{Wait: func(ctx context.Context, _ *config.Config, _ eggclient.LocalSession, _ string) (egg.RunTurnResult, error) {
		<-ctx.Done()
		return egg.RunTurnResult{}, ctx.Err()
	}}}
	if err := service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer service.RunManager.Close()
	listener, err := localmcp.ListenLocalWingControl(t.Context(), "test", service, "cli-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	clients := "clients:\n  reader:\n    owner: alice\n    grants: [agent.read]\n  observer:\n    owner: alice\n    grants: [terminal.read]\n"
	for _, test := range []struct {
		name       string
		args       []string
		clients    string
		wantError  string
		finishedID string
		pendingID  string
		exitCode   int
	}{
		{name: "completion", args: []string{"wait-any", "done", "running"}, finishedID: "done", pendingID: "running"},
		{name: "timeout", args: []string{"wait-any", "running", "--timeout", "0.1"}, pendingID: "running", exitCode: 2},
		{name: "all unknown", args: []string{"wait-any", "unknown"}},
		{name: "client owner", args: []string{"wait-any", "owner-run", "--client", "reader"}, clients: clients, finishedID: "owner-run"},
		{name: "grant denied", args: []string{"wait-any", "owner-run", "--client", "observer"}, clients: clients, wantError: `lacks grant "agent.read"`},
		{name: "unknown client", args: []string{"wait-any", "owner-run", "--client", "unknown"}, clients: clients, wantError: `MCP client "unknown" is not configured`},
		{name: "implicit client", args: []string{"wait-any", "owner-run"}, clients: clients, wantError: `MCP client "default" is not configured`},
		{name: "missing IDs", args: []string{"wait-any"}, wantError: "accepts between 1 and 64 arg(s)"},
		{name: "invalid timeout", args: []string{"wait-any", "done", "--timeout", "600.1"}, wantError: "timeout_seconds must be between"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.clients != "" {
				if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), []byte(test.clients), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "clients.yaml")) })
			}
			cmd := agentCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("CLI error = %v, want %q", err, test.wantError)
				}
				return
			}
			var exitErr *cmdutil.CommandExitError
			if test.exitCode != 0 {
				if !errors.As(err, &exitErr) || exitErr.Code != test.exitCode {
					t.Fatalf("CLI error = %#v, want exit %d", err, test.exitCode)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if test.exitCode == 2 && output.String() != "{\"finished\":[],\"pending\":[\"running\"],\"wing_id\":\"fixture-wing\"}\n" {
				t.Fatalf("timeout JSON changed: %q", output.String())
			}
			var data struct {
				Finished []struct {
					RunID  string `json:"run_id"`
					Status string `json:"status"`
				} `json:"finished"`
				Pending []string `json:"pending"`
			}
			if err := json.Unmarshal(output.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if test.finishedID == "" {
				if len(data.Finished) != 0 {
					t.Fatalf("finished = %#v", data.Finished)
				}
			} else if len(data.Finished) != 1 || data.Finished[0].RunID != test.finishedID || data.Finished[0].Status != "done" {
				t.Fatalf("finished = %#v", data.Finished)
			}
			if test.pendingID == "" {
				if len(data.Pending) != 0 {
					t.Fatalf("pending = %#v", data.Pending)
				}
			} else if len(data.Pending) != 1 || data.Pending[0] != test.pendingID {
				t.Fatalf("pending = %#v", data.Pending)
			}
		})
	}
}
