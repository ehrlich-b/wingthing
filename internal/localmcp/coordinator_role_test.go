package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestPublicCoordinatorRoleInjectedAndInspectableThroughBootstrap(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	c := fixtureConversation(t, db, cfg, "root-role", "", "owner", "idle")
	server := &Server{Version: "dev", Cfg: cfg, Principal: c.OwnerID, BoundConversation: c.ID, Logs: &bytes.Buffer{}}
	args, err := server.prepareBoundParentMCP(c, egg.DefaultEggConfig(), []string{"--model", "already-selected-model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 6 || args[0] != "--model" || args[1] != "already-selected-model" || args[2] != "--mcp-config" || args[4] != "--append-system-prompt" {
		t.Fatalf("coordinator invocation changed provider selection: %v", args)
	}
	request, _ := json.Marshal(map[string]any{"conversation_id": c.ID})
	bootstrap, err := server.toolConversationBootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap["coordinator_prompt"] != args[5] || bootstrap["coordinator_context"] != contextForConversation(c) {
		t.Fatal("inspectable public role differs from actual provider argument")
	}
	if bootstrap["automatic"] != false {
		t.Fatal("output-only bootstrap claimed provider setup")
	}
	for _, required := range []string{"persistent Wingthing coordinator", "unique request_id", "definitely_not_sent", "conversation_checkpoint", "explicit opt-in", "owner_epoch is unsupported", "Do not automatically\ncontact a Wingthing/vendor",
		"checkpoint a plan before any\nlaunch", "bring it to human attention", "never relaunch or resend under a new one", "Use only the\ntools this connection lists"} {
		if !strings.Contains(args[5], required) {
			t.Fatalf("public role missing operational bound %q", required)
		}
	}
}

func TestCoordinatorContextDoesNotInventHomeOwnershipOrGlobalIdentity(t *testing.T) {
	c := &store.Conversation{ID: "child", RootID: "dot", ParentID: "parent", WingID: "executor", SessionID: "execution", CWD: filepath.Join(t.TempDir(), "quotes\"\n{{RUNTIME_FACTS}}")}
	facts := contextForConversation(c)
	if facts.DotID != "dot" || facts.TaskID != "child" || facts.ParentTaskID != "parent" || facts.ExecutorID != "executor" || facts.SessionID != "execution" || facts.IdentityScope != "local_wing" {
		t.Fatalf("lost qualified local facts %#v", facts)
	}
	encoded, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["home_roost_id"] != nil || wire["owner_epoch"] != nil || wire["owner_epoch_state"] != "unsupported" || wire["cross_host_adoption_supported"] != false {
		t.Fatalf("fabricated ownership %#v", wire)
	}
	if !strings.Contains(publicCoordinatorPrompt(c), string(encoded)) {
		t.Fatal("workspace facts were interpreted as template source")
	}
	c.WingID = ""
	if contextForConversation(c).ExecutorIdentityState != "unknown" {
		t.Fatal("absent executor claimed known")
	}
}

func TestConversationReadReportsContextForActualLocalTasks(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	root := fixtureConversation(t, db, cfg, "role-root", "", "owner", "idle")
	child := fixtureConversation(t, db, cfg, "role-child", root.ID, "owner", "completed")
	server := &Server{Version: "dev", Cfg: cfg, Principal: "owner", BoundConversation: root.ID}
	request, _ := json.Marshal(map[string]any{"conversation_id": root.ID})
	read, err := server.ToolConversationRead(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if read["coordinator_context"] != contextForConversation(root) {
		t.Fatal("read context did not address selected logical root")
	}
	for _, task := range read["tasks"].([]map[string]any) {
		c := task["conversation"].(*store.Conversation)
		facts := task["coordinator_context"].(coordinatorContext)
		if facts.TaskID != c.ID || facts.DotID != root.ID || facts.SessionID != c.SessionID {
			t.Fatalf("task context borrowed another execution %#v", facts)
		}
		if c.ID == child.ID && facts.ParentTaskID != root.ID {
			t.Fatal("child lost parent linkage")
		}
	}
}

func TestResumedCoordinatorRoleUsesNewInvocationWithSameLogicalRoot(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	user := "role-personal-owner"
	c := fixtureConversation(t, db, cfg, "resume-role", "", roostSessionPrincipal(user), "idle")
	args, principal, err := PrepareConversationResumeMCP("dev", cfg, &config.WingConfig{}, ws.PTYStart{SessionID: "new-execution", ResumeSessionID: c.SessionID, UserID: user, CWD: c.CWD}, egg.DefaultEggConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if principal != c.OwnerID || len(args) != 4 {
		t.Fatalf("lost existing binding %q %v", principal, args)
	}
	invocation := *c
	invocation.SessionID = "new-execution"
	if args[3] != publicCoordinatorPrompt(&invocation) {
		t.Fatal("resume prompt routed facts to the old execution")
	}
	unchanged, err := db.GetConversation(c.OwnerID, c.ID)
	if err != nil || unchanged.SessionID != c.SessionID {
		t.Fatalf("role preparation committed a nonexistent execution %#v %v", unchanged, err)
	}
}
