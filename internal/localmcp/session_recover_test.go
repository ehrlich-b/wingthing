package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func recoverCoordinatorFixture(t *testing.T) (*Server, *store.Store, *store.Conversation, string) {
	t.Helper()
	s, db, root, dir := continuationFixture(t)
	intent := egg.LaunchIntent{Version: 1, Agent: "claude", CWD: root.CWD, Label: root.Title,
		ConversationID: root.ID, RootConversationID: root.RootID, ProviderSessionID: "provider", Model: continuationModel, Started: true}
	if err := egg.WriteLaunchIntent(dir, intent); err != nil {
		t.Fatal(err)
	}
	return s, db, root, dir
}

func TestRecoveryCoordinatorLaunchKeepsProviderConversationAndDotID(t *testing.T) {
	s, db, root, dir := recoverCoordinatorFixture(t)
	starts := 0
	s.startContinuation = func(c *store.Conversation, policy *egg.EggConfig, opts eggclient.SpawnEggOpts) error {
		starts++
		if opts.ResumeSessionID != "provider" || opts.ResumeSourceSessionID != root.SessionID || opts.RecoveredFrom != root.SessionID || !opts.ProviderReserved {
			t.Fatalf("resume contract: %+v", opts)
		}
		if opts.RecoveryConversation.ConversationID != root.ID || opts.RecoveryConversation.RootConversationID != root.RootID || contextForConversation(c).DotID != root.RootID {
			t.Fatalf("lost task identity: %+v", opts.RecoveryConversation)
		}
		if eggclient.LaunchModel(opts.AgentArgs) != continuationModel || !strings.Contains(strings.Join(opts.AgentArgs, " "), recoveryPrompt) {
			t.Fatalf("headless continuation args: %v", opts.AgentArgs)
		}
		return nil
	}
	result, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`))
	if err != nil {
		t.Fatal(err)
	}
	current, err := db.GetConversation(root.OwnerID, root.ID)
	if err != nil || current.SessionID != result["session"] || current.RootID != root.RootID {
		t.Fatalf("current root: %+v %v", current, err)
	}
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil || intent.RecoveredSession != current.SessionID {
		t.Fatalf("source receipt: %+v %v", intent, err)
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil || starts != 1 {
		t.Fatalf("source recovered twice: %v, starts %d", err, starts)
	}
}

func TestRecoveryReloadsOriginalEggConfigReference(t *testing.T) {
	s, _, _, dir := recoverCoordinatorFixture(t)
	s.Unsandboxed = false
	path := filepath.Join(s.Cfg.Dir, "recovery-egg.yaml")
	if err := os.WriteFile(path, []byte("fs:\n  - rw:"+s.Cfg.Dir+"\nnetwork: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := egg.UpdateLaunchIntent(dir, func(i *egg.LaunchIntent) { i.EggConfig = path }); err != nil {
		t.Fatal(err)
	}
	starts := 0
	s.startContinuation = func(_ *store.Conversation, policy *egg.EggConfig, _ eggclient.SpawnEggOpts) error {
		starts++
		if policy.SourcePath != path {
			t.Fatalf("lost config reference: %q", policy.SourcePath)
		}
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("provider starts: %d", starts)
	}
}

func TestRecoveryDoesNotUseConversationTitleAsTerminalLabel(t *testing.T) {
	s, db, root, dir := recoverCoordinatorFixture(t)
	if _, err := db.DB().Exec(`UPDATE conversations SET title = 'A coordinator conversation' WHERE id = ?`, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := egg.UpdateLaunchIntent(dir, func(i *egg.LaunchIntent) { i.Label = "" }); err != nil {
		t.Fatal(err)
	}
	s.startContinuation = func(_ *store.Conversation, _ *egg.EggConfig, opts eggclient.SpawnEggOpts) error {
		if opts.Label != "" {
			t.Fatalf("freeform title used as session label: %q", opts.Label)
		}
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRestoresBrowserOwnerWithoutLocalClientSubstitution(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	dir := filepath.Join(cfg.Dir, "eggs", "browser-session")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteEggOwner(dir, "browser-owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  local-agent:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: cfg, Principal: roostSessionPrincipal("browser-owner")}
	if err := ConfigureRecoveryClient(s, "browser-session"); err != nil {
		t.Fatal(err)
	}
	if s.identity.UserID != "browser-owner" || s.identity.Email != "owner@example.com" || s.Surface != control.SurfaceHTTPMCP || !s.Grants["terminal.start"] || s.conversationMCPClient() != s.Principal {
		t.Fatalf("lost original browser admission: %+v", s)
	}
	local := &Server{Cfg: cfg, Principal: "local-agent"}
	if err := ConfigureRecoveryClient(local, "browser-session"); err != nil {
		t.Fatal(err)
	}
	if local.identity.UserID != "" || local.Grants["terminal.start"] || !local.Grants["terminal.read"] {
		t.Fatalf("browser identity leaked to local client: %+v", local)
	}
}

func TestRecoveryAutoOptInOncePerRootBootAndNeverChildren(t *testing.T) {
	s, db, root, _ := recoverCoordinatorFixture(t)
	child, _, err := db.ReserveConversation(store.Conversation{ID: "child", OwnerID: root.OwnerID, ParentID: root.ID, Agent: "claude", CWD: root.CWD, SessionID: "child-source", LaunchKey: "child", SpecDigest: "child"})
	if err != nil {
		t.Fatal(err)
	}
	childDir := writeResumeSessionFixture(t, s.Cfg, child.SessionID, root.OwnerID, "claude", root.CWD, "child-provider", "{}\n")
	if err := eggclient.WriteSessionPrincipal(childDir, root.OwnerID); err != nil {
		t.Fatal(err)
	}
	if err := egg.WriteLaunchIntent(childDir, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: root.CWD, ConversationID: child.ID, RootConversationID: root.ID, ParentConversationID: root.ID, ProviderSessionID: "child-provider", Started: true}); err != nil {
		t.Fatal(err)
	}
	starts := 0
	s.startContinuation = func(c *store.Conversation, _ *egg.EggConfig, opts eggclient.SpawnEggOpts) error {
		starts++
		if c.ID != root.ID || opts.RecoveryBoot != "boot-a" {
			t.Fatalf("wrong auto target: %+v %+v", c, opts)
		}
		// Simulate an interrupted replacement to exercise the next startup.
		target := filepath.Join(s.Cfg.Dir, "eggs", c.SessionID)
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, "egg.meta"), []byte("agent=claude\ncwd="+c.CWD+"\n"), 0600); err != nil {
			return err
		}
		if err := eggclient.WriteSessionPrincipal(target, root.OwnerID); err != nil {
			return err
		}
		return egg.WriteLaunchIntent(target, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: c.CWD, Started: true, ProviderSessionID: "provider", ConversationID: c.ID, RootConversationID: c.RootID, AutoBoot: opts.RecoveryBoot})
	}
	factory := func(eggclient.RecoverySession) *Server { return s }
	wc := &config.WingConfig{}
	runSessionRecovery(context.Background(), s.Cfg, wc, false, "boot-a", factory)
	if starts != 0 {
		t.Fatal("auto recovery enabled by default")
	}
	wc.Recover = "coordinators"
	runSessionRecovery(context.Background(), s.Cfg, wc, false, "boot-a", factory)
	runSessionRecovery(context.Background(), s.Cfg, wc, false, "boot-a", factory)
	if starts != 1 {
		t.Fatalf("auto starts: %d", starts)
	}
	current, err := db.GetConversation(root.OwnerID, child.ID)
	if err != nil || current.SessionID != child.SessionID {
		t.Fatalf("child was recovered: %+v %v", current, err)
	}
	// An explicit later execution must not erase the root's boot claim.
	currentRoot, err := db.GetConversation(root.OwnerID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := egg.UpdateLaunchIntent(filepath.Join(s.Cfg.Dir, "eggs", currentRoot.SessionID), func(i *egg.LaunchIntent) { i.AutoBoot = "" }); err != nil {
		t.Fatal(err)
	}
	runSessionRecovery(context.Background(), s.Cfg, wc, false, "boot-a", factory)
	if starts != 1 {
		t.Fatal("root boot claim lost across executions")
	}
}

func TestRecoveryAutoFailureRetainsEligibilityAndBackoff(t *testing.T) {
	s, db, root, dir := recoverCoordinatorFixture(t)
	starts := 0
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
		starts++
		return errors.New("provider failed")
	}
	factory := func(eggclient.RecoverySession) *Server { return s }
	wc := &config.WingConfig{Recover: "coordinators"}
	for _, boot := range []string{"boot-a", "boot-a", "boot-b"} {
		runSessionRecovery(context.Background(), s.Cfg, wc, false, boot, factory)
	}
	if starts != 1 {
		t.Fatalf("failure retried without backoff: %d", starts)
	}
	if got := eggclient.ClassifyEgg(s.Cfg, root.SessionID); got.Class != eggclient.RecoveryEligible {
		t.Fatalf("failed entry disappeared: %+v", got)
	}
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil || intent.RecoveryError == "" || intent.RetryAfter == 0 {
		t.Fatalf("failure evidence: %+v %v", intent, err)
	}
	current, err := db.GetConversation(root.OwnerID, root.ID)
	if err != nil || current.SessionID != root.SessionID {
		t.Fatalf("failed recovery retired source: %+v %v", current, err)
	}
}

func TestRecoverySupersededConversationIsArchived(t *testing.T) {
	s, db, root, _ := recoverCoordinatorFixture(t)
	if err := db.ResumeConversationExecution(root.SessionID, "later-execution"); err != nil {
		t.Fatal(err)
	}
	if source := eggclient.ClassifyEgg(s.Cfg, root.SessionID); source.Class != eggclient.RecoveryArchived {
		t.Fatalf("retired execution eligible: %+v", source)
	}
}

func TestRecoveryMCPGrantSchemaOwnershipAndDeliberateStop(t *testing.T) {
	s, _, _, dir := recoverCoordinatorFixture(t)
	tool, ok := control.Lookup("session_recover")
	if !ok || tool.Grant != "session.recover" || tool.InputSchema["type"] != "object" || tool.InputSchema["anyOf"] != nil {
		t.Fatalf("recovery schema: %+v", tool)
	}
	s.Grants = GrantSet([]string{"terminal.read", "terminal.start"})
	result, failed, protocolErr := s.callTool(context.Background(), "session_recover", json.RawMessage(`{"list":true}`))
	if protocolErr != nil || !failed || !strings.Contains(result["error"].(string), "session.recover") {
		t.Fatalf("grant bypass: %+v %v %v", result, failed, protocolErr)
	}
	s.Grants["session.recover"] = true
	result, failed, protocolErr = s.callTool(context.Background(), "session_recover", json.RawMessage(`{"list":true}`))
	if protocolErr != nil || failed || len(result["sessions"].([]eggclient.RecoverySession)) != 1 {
		t.Fatalf("list: %+v %v %v", result, failed, protocolErr)
	}
	s.Principal = "other"
	result, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{}`))
	if err != nil || len(result["sessions"].([]eggclient.RecoverySession)) != 0 {
		t.Fatalf("foreign entries exposed: %+v %v", result, err)
	}
	s.Principal = "owner"
	if err := egg.MarkDeliberateStop(dir, "stop"); err != nil {
		t.Fatal(err)
	}
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
		t.Fatal("deliberate stop recovered")
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
		t.Fatal("deliberate stop admitted")
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"list":true,"session":"source"}`)); err == nil {
		t.Fatal("ambiguous action admitted")
	}
}
