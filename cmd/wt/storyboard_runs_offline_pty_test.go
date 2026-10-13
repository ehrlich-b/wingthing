//go:build runinspectfixture

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestStoryboardRunsOfflinePTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	b := fixture.New(t, filepath.Join(f.work, "completed-copy"))
	b.Wing("completed-run", "completed-execution", "", "terminal", "done", fixture.PrivateOutput)
	b.Egg("completed-run", "completed-execution", "done", fixture.PrivateOutput)
	b.Seal()
	before := fixture.Inventory(t, f.work)
	report, err := runinspect.Inspect(t.Context(), b.Path, runinspect.Options{WorkspaceRoot: f.work})
	if err != nil || len(report.Runs) != 1 || report.Runs[0].Diagnosis != "completed_with_output" {
		t.Fatalf("snapshot diagnosis: %+v %v", report, err)
	}
	h := northstarStartPTY(t, f, binary, 120, 36)
	h.expect("offline inspector navigator", 0, "wt |")
	h.expect("snapshot form", h.send("d3"), "Completed snapshot inside this workspace")
	h.expect("metadata-only snapshot", h.send(b.Path+"\r"), "Offline run inspection", "completed_with_output")
	h.mu.Lock()
	text := h.transcript.String()
	h.mu.Unlock()
	if strings.Contains(text, fixture.PrivateOutput) || strings.Contains(text, fixture.PrivatePrompt) {
		t.Fatal("offline view exposed content")
	}
	storyboardCapture(t, h, "06-5-completed-copy")
	h.send("\x1b")
	h.expect("report return", 0, "Runs / diagnostics")
	h.send("\x1b")
	h.expect("navigator return", 0, "Enter attach")
	h.exit("q")
	after := fixture.Inventory(t, f.work)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("inspection changed snapshot or workspace")
	}

	// The authenticated control seam returns typed recovery evidence. No egg,
	// prompt or process is replayed to fill an unavailable output handle.
	server, err := controlsocket.Listen(t.Context(), f.local, "local-fixture", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{Principal: "fixture-owner"}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
			r := control.DirectResponse{Version: control.ContractVersion, ID: req.ID}
			var args map[string]any
			json.Unmarshal(req.Arguments, &args)
			switch req.Tool {
			case "wing_list":
				r.Result = map[string]any{"count": 1, "wings": []any{map[string]any{"name": "local", "wing_id": "local-fixture", "mcp_control": true}}}
			case "agent_list":
				r.Result = map[string]any{"wing_id": "local-fixture", "count": 2, "snapshot_revision": "fixture-revision", "observed_at": time.Now().UTC().Format(time.RFC3339), "runs": []any{map[string]any{"wing_id": "local-fixture", "run_id": "unknown-admission", "session_id": "orphan-execution", "idempotency_key": "original-admission-key", "status": "failed", "failure_kind": "unknown_outcome", "phase": "reconciling", "agent": "codex", "cwd": f.work, "isolation": "standard", "output_availability": "unavailable", "cleanup": "unknown", "terminal": true}, map[string]any{"wing_id": "local-fixture", "run_id": "expired-output", "session_id": "completed-execution", "idempotency_key": "completed-key", "status": "done", "phase": "terminal", "agent": "codex", "cwd": f.work, "output_availability": "unavailable", "cleanup": "unknown", "terminal": true}}}
			case "terminal_list":
				r.Result = map[string]any{"wing_id": "local-fixture", "sessions": []any{}}
			default:
				r.Error = "unexpected mutation or result access"
				t.Error(req.Tool)
			}
			return r
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	h = northstarStartPTY(t, f, binary, 120, 36)
	h.expect("recovery navigator", 0, "wt |")
	h.expect("unknown inventory", h.send("h"), "unknown-admission")
	h.expect("original recovery receipt", h.send("/unknown-admission\ri"), "Original admission key: original-admission-key", "Status: unknown", "reconcile the original key", "does not prove cleanup")
	storyboardCapture(t, h, "06-7-unknown-output-orphan")
	h.send("\x1b")
	h.expect("history return", 0, "Recent runs")
	h.expect("expired success outcome", h.send("/\x15expired-output\ri"), "Status: done", "Output: unavailable", "Terminal outcome retained", "does not prove cleanup")
	storyboardCapture(t, h, "06-7-expired-output")
	h.send("\x1b")
	h.expect("back to history", 0, "Recent runs")
	h.exit("q")
}

func TestStoryboardTerminalTaskPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.bin, "claude"), `#!/bin/sh
if [ "$1" = --version ]; then printf 'fixture-claude 1\n'; exit 0; fi
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Fixture tests: 0 failures; no model request."}]}}' '{"type":"result","input_tokens":0,"output_tokens":0}'
`, 0700)
	policy := filepath.Join(f.work, "egg.yaml")
	northstarWrite(t, policy, "base: none\nfs: ['ro:/', 'rw:"+f.work+"']\nnetwork: {agent_domains: none}\n", 0600)
	h := northstarStartPTY(t, f, binary, 120, 36, "run", "Report fixture test failures", "--agent", "claude", "--config", policy)
	h.expect("legacy task was submitted", 0, "submitted:")
	// Required native enforcement must pass on this host; no --unsandboxed
	// retry or automatic fallback is permitted to make this journey pass.
	h.expect("sandboxed fixture task result", 0, "Fixture tests: 0 failures; no model request.")
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not finish")
	}
	storyboardCapture(t, h, "06-1-terminal-task")
	s, err := store.Open(filepath.Join(f.local, "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tasks, err := s.ListRecent(2)
	if err != nil || len(tasks) != 1 || tasks[0].Status != "done" || tasks[0].Isolation == "privileged" {
		t.Fatalf("task receipt: %+v %v", tasks, err)
	}
}
