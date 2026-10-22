package localmcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestSessionReportStrictSchemaOwnershipAndPathBounds(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	dir := filepath.Join(cfg.Dir, "eggs", "ours")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd="+cfg.Dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testWingServer(t, &Server{Version: "test", Cfg: cfg, Logs: &bytes.Buffer{}})
	tool, ok := control.Lookup("session_report")
	if !ok || tool.Grant != "terminal.send" || tool.InputSchema["additionalProperties"] != false {
		t.Fatalf("schema=%v", tool)
	}
	for _, raw := range []string{`{"session":"ours"}`, `{"session":"ours","report":null}`, `{"session":"ours","report":{"goal":"a","update":"b","next_action":"c","approve":true}}`, `{"session":"ours","report":{"goal":"a","update":"b","next_action":"c"},"grant":"all"}`} {
		if _, err := s.ToolSessionReport(t.Context(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	args := json.RawMessage(`{"session":"ours","report":{"goal":"Goal","update":"Tests passed","next_action":"Read the results","presence":"asleep"}}`)
	result, err := s.ToolSessionReport(t.Context(), args)
	if err != nil || result["session"] != "ours" {
		t.Fatalf("result=%v %v", result, err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s.enforcePathBounds = true
	s.allowedPaths = []string{t.TempDir()}
	if _, err := s.ToolSessionReport(t.Context(), args); err == nil {
		t.Fatal("out-of-path report accepted")
	}
	s.enforcePathBounds = false
	if err := eggclient.WriteEggOwner(dir, "foreign", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToolSessionReport(t.Context(), args); err == nil {
		t.Fatal("foreign report accepted")
	}
	after, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("denied report changed journal")
	}
}

func TestShellReportMCPRejectsMutableAndForeignSession(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	for _, id := range []string{"shell-ours", "shell-other"} {
		dir := filepath.Join(cfg.Dir, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := testWingServer(t, &Server{Version: "test", Cfg: cfg, Logs: &bytes.Buffer{}})
	report := func(ref, id, state string, revision, after int64) error {
		args, _ := json.Marshal(map[string]any{"session": ref, "report": map[string]any{"goal": "g", "update": "u", "next_action": "n", "shell": map[string]any{"session_id": id, "state": state, "revision": revision, "after": after}}})
		_, err := s.ToolSessionReport(t.Context(), args)
		return err
	}
	if err := report("shell-ours", "shell-ours", "working", 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := report("shell-ours", "shell-ours", "waiting", 2, 1); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"shell-", "shell-other"} {
		if err := report(ref, "shell-ours", "continue", 3, 2); err == nil {
			t.Fatal("cross-session report accepted", ref)
		}
	}
	if err := report("shell-ours", "shell-ours", "continue", 3, 1); err == nil {
		t.Fatal("stale acknowledgement accepted")
	}
	if err := report("shell-ours", "shell-ours", "continue", 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteEggOwner(filepath.Join(cfg.Dir, "eggs", "shell-other"), "foreign", ""); err != nil {
		t.Fatal(err)
	}
	if err := report("shell-other", "shell-other", "working", 1, 0); err == nil {
		t.Fatal("foreign report accepted")
	}
}
