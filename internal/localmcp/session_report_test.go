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
