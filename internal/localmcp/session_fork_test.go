package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func forkServerFixture(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Dir: t.TempDir()}
	dir := writeResumeSessionFixture(t, cfg, "source", "alice", "claude", cfg.Dir, "provider", "{}\n")
	if err := eggclient.WriteSessionPrincipal(dir, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.SaveSessionLaunchConfig(dir, egg.DefaultEggConfig(), []string{"--model", "sonnet"}); err != nil {
		t.Fatal(err)
	}
	return &Server{Version: "dev", Cfg: cfg, Principal: "owner", Logs: &bytes.Buffer{}, spawnFork: func(*eggclient.SessionForkPlan) error { return nil }}
}

func TestSessionForkMCPGrantAndSourceAuditTarget(t *testing.T) {
	s := forkServerFixture(t)
	s.Grants = GrantSet([]string{"terminal.read"})
	args := json.RawMessage(`{"session":"source","name":"branch"}`)
	result, isError, protocolErr := s.callTool(context.Background(), "session_fork", args)
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), "terminal.start") {
		t.Fatalf("grant refusal: %#v %t %v", result, isError, protocolErr)
	}
	s.Grants = GrantSet([]string{"terminal.start"})
	result, isError, protocolErr = s.callTool(context.Background(), "session_fork", args)
	if protocolErr != nil || isError || result["session"] == "source" || result["source_session"] != "source" {
		t.Fatalf("fork result: %#v %t %v", result, isError, protocolErr)
	}
	data, err := os.ReadFile(filepath.Join(s.Cfg.Dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["tool"] != "session_fork" || record["target"] != "source" {
			t.Fatalf("audit target: %s", line)
		}
	}
}

func TestSessionForkMCPUsesSpawnBoundsAndRefusesBoundConnections(t *testing.T) {
	for _, name := range []string{"sessions", "rate", "shared rate", "bound", "principal", "nested schema"} {
		t.Run(name, func(t *testing.T) {
			s := forkServerFixture(t)
			want := ""
			args := json.RawMessage(`{"session":"source","name":"branch"}`)
			switch name {
			case "sessions":
				seedRemoteListSession(t, s.Cfg, "active", "owner")
				s.MaxSessions = 1
				want = "max_sessions"
			case "rate":
				s.MaxSpawnsPerHour = 1
				s.spawnTimes = []time.Time{time.Now()}
				want = "max_spawns"
			case "shared rate":
				s.MaxSpawnsPerHour = 1
				s.admission = NewMCPAdmissionState()
				s.admission.spawnTimes["owner"] = []time.Time{time.Now()}
				want = "max_spawns"
			case "bound":
				s.BoundConversation = "root"
				want = "bound"
			case "principal":
				s.Principal = "other"
				want = "owned"
			case "nested schema":
				args = json.RawMessage(`{"session":"source","options":{"name":"branch"}}`)
				want = "unknown field"
			}
			s.spawnFork = func(*eggclient.SessionForkPlan) error { t.Fatal("refused fork spawned"); return nil }
			_, err := s.ToolSessionFork(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error: %v, want %s", err, want)
			}
		})
	}
}

func TestBrowserSessionForkSharedOwnerRules(t *testing.T) {
	s := forkServerFixture(t)
	dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd="+s.Cfg.Dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"session":"source","name":"branch"}`)
	for _, shared := range []bool{false, true} {
		for _, user := range []string{"alice", "bob"} {
			req := ws.TunnelRequest{SenderUserID: user, SenderOrgRole: "admin", SenderEmail: user + "@example.com"}
			_, err := BrowserSessionControl("dev", context.Background(), s.Cfg, &config.WingConfig{Org: "shared-org"}, req, "session_fork", args, s.Cfg.Dir, shared)
			want := "only Claude"
			if user == "bob" {
				want = "owned"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("shared=%t user=%s error=%v", shared, user, err)
			}
		}
	}
}

func TestSessionForkMCPBindsNewRootWithInheritedIsolation(t *testing.T) {
	s := forkServerFixture(t)
	dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
	if err := eggclient.SaveSessionLaunchConfig(dir, egg.UnsandboxedEggConfig(), []string{"--model", "sonnet"}); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	original, _, err := db.ReserveConversation(store.Conversation{ID: "original", OwnerID: "owner", Agent: "claude", CWD: s.Cfg.Dir, SessionID: "source", LaunchKey: "initial", SpecDigest: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		if egg.RequiresSandbox(plan.Config, "claude") || plan.Conversation.ID == original.ID || plan.Conversation.ParentID != "" {
			t.Fatalf("fork changed policy or reused the original tree: %#v", plan)
		}
		var binding string
		for i, arg := range plan.Options.AgentArgs {
			if arg == "--mcp-config" && i+1 < len(plan.Options.AgentArgs) {
				binding = plan.Options.AgentArgs[i+1]
			}
		}
		data, err := os.ReadFile(binding)
		if err != nil || !strings.Contains(string(data), plan.Conversation.ID) || strings.Contains(string(data), `"original"`) {
			t.Fatalf("fork MCP binding = %q: %v", data, err)
		}
		return nil
	}
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err != nil {
		t.Fatal(err)
	}
}
