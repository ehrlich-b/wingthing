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
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestSessionPinSchemaStrictOwnershipAndRolePreservation(t *testing.T) {
	cfg := &config.Config{Dir: testsocket.Dir(t)}
	dir := filepath.Join(cfg.Dir, "eggs", "ours")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+cfg.Dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	s := testWingServer(t, &Server{Version: "test", Cfg: cfg, Logs: &bytes.Buffer{}})
	tool, ok := control.Lookup("session_pin")
	if !ok || tool.Grant != "terminal.rename" || tool.InputSchema["additionalProperties"] != false {
		t.Fatalf("schema=%v", tool)
	}
	for _, raw := range []string{`{"session":"ours"}`, `{"session":"ours","pinned":null}`, `{"session":"ours","pinned":"true"}`, `{"session":"ours","pinned":true,"role":"other"}`} {
		if _, err := s.ToolSessionPin(t.Context(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := s.ToolSessionPin(t.Context(), json.RawMessage(`{"session":"ours","pinned":false}`)); err != nil {
		t.Fatal(err)
	}
	m, err := sessionrole.Read(dir)
	if err != nil || m.Pinned || m.Role != "coordinator" {
		t.Fatalf("unpin=%v %v", m, err)
	}
	if err := eggclient.WriteEggOwner(dir, "foreign", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToolSessionPin(t.Context(), json.RawMessage(`{"session":"ours","pinned":true}`)); err == nil {
		t.Fatal("foreign pin accepted")
	}
	if m, err := sessionrole.Read(dir); err != nil || m.Pinned {
		t.Fatalf("denied mutation changed metadata: %v %v", m, err)
	}
}

func TestRoleAgentStartRequiresScopedClaudeParent(t *testing.T) {
	s := testWingServer(t, &Server{Version: "test", Cfg: &config.Config{Dir: testsocket.Dir(t)}, Logs: &bytes.Buffer{}})
	for _, raw := range []string{
		`{"agent":"claude","role":"coordinator"}`,
		`{"agent":"codex","role":"coordinator","scoped_mcp":true,"conversation_role":"parent"}`,
		`{"agent":"claude","role":"coordinator","scoped_mcp":true,"conversation_role":"child"}`,
		`{"agent":"claude","role":"../secret","scoped_mcp":true,"conversation_role":"parent"}`,
		`{"agent":"claude","role":"scout","scoped_mcp":true,"conversation_role":"parent"}`,
	} {
		if _, err := s.toolAgentStart(json.RawMessage(raw)); err == nil {
			t.Errorf("role launch accepted %s", raw)
		}
	}
	tool, _ := control.Lookup("agent_start")
	if _, ok := tool.InputSchema["properties"].(map[string]any)["role"]; !ok {
		t.Fatal("role absent from model-facing schema")
	}
}
