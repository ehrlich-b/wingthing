package main

import (
	"bytes"
	"context"
	"encoding/json"

	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func fixtureConversation(t *testing.T, db *store.Store, cfg *config.Config, id, parent, owner string, states ...string) *store.Conversation {
	t.Helper()
	c, _, err := db.ReserveConversation(store.Conversation{ID: id, ParentID: parent, OwnerID: owner, SessionID: "execution-" + id, LaunchKey: "request-" + id, SpecDigest: id, Agent: "claude", CWD: cfg.Dir, WingID: "wing-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.Dir, "eggs", c.SessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+cfg.Dir+"\nprovider_session_id=provider-"+id+"\nprovider_home="+cfg.Dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(dir, owner); err != nil {
		t.Fatal(err)
	}
	var journal bytes.Buffer
	for i, state := range states {
		data, _ := json.Marshal(egg.SessionEvent{Sequence: int64(i + 1), Type: "native-fixture", Source: "claude_hook", State: state, ProviderSessionID: "provider-" + id})
		journal.Write(data)
		journal.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), journal.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConversationReadKeepsIntermediateNativeStatesAcrossReconnectAndResume(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	root := fixtureConversation(t, db, cfg, "root", "", "owner", "idle")
	child := fixtureConversation(t, db, cfg, "child", root.ID, "owner", "working", "needs_input", "completed", "working")
	next := fixtureConversation(t, db, cfg, "unlinked", "", "owner", "idle")
	// Reuse the independent fixture execution as an exact resumed instance.
	if _, err := db.DB().Exec(`DELETE FROM conversation_executions WHERE session_id = ?`, next.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`DELETE FROM conversations WHERE id = ?`, next.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ResumeConversationExecution(child.SessionID, next.SessionID); err != nil {
		t.Fatal(err)
	}
	server := &localMCPServer{cfg: cfg, principal: "owner", boundConversation: root.ID, logs: &bytes.Buffer{}}
	args, _ := json.Marshal(map[string]any{"conversation_id": root.ID})
	result, err := server.toolConversationRead(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	events := result["events"].([]store.ConversationEvent)
	var childStates []string
	for _, event := range events {
		if event.SessionID == child.SessionID {
			childStates = append(childStates, event.State)
		}
	}
	if strings.Join(childStates, ",") != "working,needs_input,completed,working" {
		t.Fatalf("lost native transitions %v", childStates)
	}
	server = &localMCPServer{cfg: cfg, principal: "owner", boundConversation: root.ID, logs: &bytes.Buffer{}}
	again, err := server.toolConversationRead(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if len(again["events"].([]store.ConversationEvent)) != len(events) {
		t.Fatal("reconnect duplicated deliveries")
	}
	input, _ := json.Marshal(map[string]any{"conversation_id": root.ID, "expected_revision": 0, "after_cursor": result["next_cursor"], "checkpoint": "read both child states"})
	if _, err := server.toolConversationCheckpoint(input); err != nil {
		t.Fatal(err)
	}
}

func TestBoundConversationBootstrapPreservesBrowserOwnerAndRejectsOtherTrees(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	user := "personal-fixture-user"
	owner := roostSessionPrincipal(user)
	root := fixtureConversation(t, db, cfg, "root", "", owner, "idle")
	unrelated := fixtureConversation(t, db, cfg, "other", "", owner, "idle")
	dir := filepath.Join(cfg.Dir, "eggs", root.SessionID)
	if err := eggclient.WriteEggOwner(dir, user, "fixture@example.invalid"); err != nil {
		t.Fatal(err)
	}
	server := &localMCPServer{cfg: cfg, principal: owner, boundConversation: root.ID, logs: &bytes.Buffer{}}
	if err := validateBoundConversation(server); err != nil {
		t.Fatal(err)
	}
	if server.identity.UserID != user {
		t.Fatalf("native child identity %#v", server.identity)
	}
	for _, operation := range []string{"conversation_read", "conversation_bootstrap"} {
		input, _ := json.Marshal(map[string]any{"conversation_id": unrelated.ID})
		_, isError, _ := server.callTool(context.Background(), operation, input)
		if !isError {
			t.Fatalf("bound %s exposed unrelated tree", operation)
		}
	}
	input, _ := json.Marshal(map[string]any{"conversation_id": root.ID})
	result, err := server.toolConversationBootstrap(input)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if !bytes.Contains(data, []byte(owner)) || !bytes.Contains(data, []byte(root.ID)) || !bytes.Contains(data, []byte(cfg.Dir)) {
		t.Fatalf("bootstrap identity missing %s", data)
	}
	child, created, err := server.reserveAgentConversation("claude", cfg.Dir, "child", "child", "", "child-request", "new-child", map[string]any{"label": "child"})
	if err != nil || !created || child.ParentID != root.ID {
		t.Fatalf("bound launch %#v %v %v", child, created, err)
	}
	if _, _, err := server.reserveAgentConversation("claude", cfg.Dir, "bad", "child", unrelated.ID, "bad-request", "bad-child", nil); err == nil {
		t.Fatal("explicit other parent accepted")
	}
	listing, err := server.toolConversationList(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(listing["conversations"].([]*store.Conversation)) != 2 {
		t.Fatalf("bound listing %v", listing)
	}
	browser, err := browserSessionControl(context.Background(), cfg, &config.WingConfig{}, ws.TunnelRequest{SenderUserID: user, SenderOrgRole: "owner"}, "conversation_list", json.RawMessage(`{}`), cfg.Dir, false)
	if err != nil || len(browser["conversations"].([]*store.Conversation)) != 3 {
		t.Fatalf("shared browser inventory %v %v", browser, err)
	}
	foreign, err := browserSessionControl(context.Background(), cfg, &config.WingConfig{}, ws.TunnelRequest{SenderUserID: "different-user", SenderOrgRole: "owner"}, "conversation_list", json.RawMessage(`{}`), cfg.Dir, false)
	if err != nil || len(foreign["conversations"].([]*store.Conversation)) != 0 {
		t.Fatalf("foreign inventory %v %v", foreign, err)
	}
}

func TestAutomaticParentMCPUsesExistingSandboxAndRejectsConfigCollision(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Dir: filepath.Join(workspace, "preview-state")}
	c := &store.Conversation{ID: "parent", CWD: workspace, OwnerID: "owner"}
	server := &localMCPServer{cfg: cfg, principal: "owner"}
	args, err := server.prepareBoundParentMCP(c, egg.DefaultEggConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 4 || args[0] != "--mcp-config" {
		t.Fatalf("parent args %v", args)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"--conversation"`)) || !bytes.Contains(data, []byte(`"WINGTHING_DIR"`)) {
		t.Fatalf("unbound config %s", data)
	}
	resumed, err := server.prepareBoundParentMCP(c, egg.DefaultEggConfig(), nil)
	if err != nil || strings.Join(args, "\x00") != strings.Join(resumed, "\x00") {
		t.Fatalf("immutable resume configuration %v %v", resumed, err)
	}
	if err := os.WriteFile(args[1], []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.prepareBoundParentMCP(c, egg.DefaultEggConfig(), nil); err == nil {
		t.Fatal("changed resume configuration accepted")
	}
	if _, err := server.prepareBoundParentMCP(c, egg.DefaultEggConfig(), []string{"--mcp-config", "existing.json"}); err == nil {
		t.Fatal("config collision accepted")
	}
	server.cfg = &config.Config{Dir: t.TempDir()}
	if _, err := server.prepareBoundParentMCP(&store.Conversation{ID: "outside", CWD: workspace}, egg.DefaultEggConfig(), nil); err == nil {
		t.Fatal("inaccessible state silently mounted")
	}
}

func TestConversationDirectMCPPreservesConfiguredClientAndOwner(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{Dir: filepath.Join(workspace, "state")}
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	root := fixtureConversation(t, db, cfg, "root", "", "alice", "idle")
	root.CWD = workspace
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  coordinator:\n    owner: alice\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := &localMCPServer{cfg: cfg, principal: "alice", actor: "coordinator", surface: control.SurfaceLocalMCP}
	args, err := launcher.prepareBoundParentMCP(root, egg.DefaultEggConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := launcher.toolConversationBootstrap(json.RawMessage(`{"conversation_id":"root"}`))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapData, _ := json.Marshal(bootstrap["configuration"])
	for name, data := range map[string][]byte{"automatic": data, "bootstrap": bootstrapData} {
		t.Run(name, func(t *testing.T) {
			var configuration struct {
				Servers map[string]struct {
					Args []string          `json:"args"`
					Env  map[string]string `json:"env"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &configuration); err != nil {
				t.Fatal(err)
			}
			server := configuration.Servers["wingthing"]
			if !slices.Equal(server.Args, []string{"mcp", "stdio", "--client", "coordinator", "--conversation", root.ID}) || server.Env["WINGTHING_DIR"] != cfg.Dir {
				t.Fatalf("launcher client replaced in config: %s", data)
			}
			clients, err := loadLocalMCPClientsConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			client, ok := clients.Clients[server.Args[3]]
			if !ok || client.Owner != "alice" {
				t.Fatalf("injected client lost configured owner mapping: %+v", client)
			}
			bound := &localMCPServer{cfg: cfg, principal: client.Owner, actor: server.Args[3], boundConversation: root.ID, grants: grantSet(client.Grants)}
			if err := validateBoundConversation(bound); err != nil || !bound.toolAllowed("session_read") || bound.toolAllowed("agent_start") {
				t.Fatalf("bound client lost configured owner or grants: %v", err)
			}
		})
	}
	if bootstrap["mcp_client"] != "coordinator" {
		t.Fatalf("bootstrap advertised the owner as its client: %v", bootstrap["mcp_client"])
	}
	// Browser audit actors do not name a clients.yaml client.
	launcher.surface, launcher.actor = control.SurfaceHTTPMCP, "browser"
	browser, err := launcher.toolConversationBootstrap(json.RawMessage(`{"conversation_id":"root"}`))
	if err != nil || browser["mcp_client"] != "alice" {
		t.Fatalf("browser bootstrap identity changed: %v %v", browser, err)
	}
}
