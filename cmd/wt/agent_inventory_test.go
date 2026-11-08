package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestAgentListCLIUsesToolSeamAndHelp(t *testing.T) {
	t.Setenv("WINGTHING_DIR", testsocket.Dir(t))
	t.Setenv("WT_MCP_CLIENT", "read-client")
	previous := callRunInventoryTool
	t.Cleanup(func() { callRunInventoryTool = previous })
	calls := 0
	callRunInventoryTool = func(_ context.Context, dir, client, name string, args json.RawMessage) (map[string]any, error) {
		calls++
		if client != "read-client" || name != "agent_list" {
			t.Fatalf("CLI bypassed tool seam: %s %s", client, name)
		}
		var request map[string]any
		_ = json.Unmarshal(args, &request)
		if request["idempotency_key"] != "original-key" || request["limit"] != float64(2) {
			t.Fatalf("CLI changed filter: %s", args)
		}
		return map[string]any{"runs": []any{}, "count": 0, "wing_id": "fixture-wing", "snapshot_revision": "fixture"}, nil
	}
	out := new(bytes.Buffer)
	cmd := agentCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"runs", "--json", "--limit", "2", "--idempotency-key", "original-key"})
	if err := cmd.Execute(); err != nil || calls != 1 || !strings.Contains(out.String(), `"runs":[]`) {
		t.Fatalf("CLI JSON: %s %v", out, err)
	}
	help := new(bytes.Buffer)
	cmd = agentCmd()
	cmd.SetOut(help)
	cmd.SetArgs([]string{"runs", "--help"})
	if err := cmd.Execute(); err != nil || !strings.Contains(help.String(), "--cursor") || !strings.Contains(help.String(), "wt agent runs --json") || calls != 1 {
		t.Fatalf("CLI help: %s %v", help, err)
	}
}

func TestRunInventoryCLIRecoverIsFiniteAndOldPeerErrors(t *testing.T) {
	t.Setenv("WINGTHING_DIR", testsocket.Dir(t))
	previous := callRunInventoryTool
	t.Cleanup(func() { callRunInventoryTool = previous })
	calls := 0
	callRunInventoryTool = func(_ context.Context, dir, client, name string, args json.RawMessage) (map[string]any, error) {
		calls++
		if name == "agent_list" {
			return map[string]any{"wing_id": "wing", "snapshot_revision": "snapshot", "count": 1, "runs": []any{map[string]any{"wing_id": "wing", "run_id": "recovered", "terminal": true, "cleanup": "unknown", "output_availability": "available"}}}, nil
		}
		if name == "agent_status" {
			return map[string]any{"run_id": "recovered", "status": "done"}, nil
		}
		if name == "agent_result" {
			return map[string]any{"run_id": "recovered", "status": "done", "ready": true, "output": "real result"}, nil
		}
		t.Fatalf("unrequested operation %s", name)
		return nil, nil
	}
	out := new(bytes.Buffer)
	cmd := agentCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"recover", "--json"})
	if err := cmd.Execute(); err != nil || calls != 3 || !strings.Contains(out.String(), `"output":"real result"`) {
		t.Fatalf("finite recover: %s %v", out, err)
	}
	callRunInventoryTool = func(context.Context, string, string, string, json.RawMessage) (map[string]any, error) {
		return nil, errors.ErrUnsupported
	}
	cmd = agentCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"recover", "--json"})
	if err := cmd.Execute(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("old peer became empty inventory: %v", err)
	}
}
