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
	if err := os.Remove(filepath.Join(dir, "egg.owner")); err != nil {
		t.Fatal(err)
	}
	writeRecoveryAuthority(t, dir, egg.UnsandboxedEggConfig())
	return s, db, root, dir
}

func writeRecoveryAuthority(t *testing.T, dir string, policy *egg.EggConfig) {
	t.Helper()
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := egg.NewRecoveryRecord(intent, policy, eggclient.ReadEggMetaValues(dir)["provider_home"])
	if err != nil {
		t.Fatal(err)
	}
	record.Principal, record.OwnerID, record.OwnerEmail = eggclient.ReadSessionPrincipal(dir), eggclient.ReadEggOwner(dir), eggclient.ReadEggOwnerEmail(dir)
	record.ProviderHome = eggclient.ReadEggMetaValues(dir)["provider_home"]
	if err := egg.WriteRecoveryRecord(dir, record); err != nil {
		t.Fatal(err)
	}
	if err := egg.WriteLaunchIntent(dir, record.Intent); err != nil {
		t.Fatal(err)
	}
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
	policy, err := egg.ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryAuthority(t, dir, policy)
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
	if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: cfg.Dir}); err != nil {
		t.Fatal(err)
	}
	writeRecoveryAuthority(t, dir, egg.UnsandboxedEggConfig())
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  local-agent:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{Version: "test", Cfg: cfg, Principal: roostSessionPrincipal("browser-owner")}
	if err := ConfigureRecoveryClient(s, "browser-session"); err != nil {
		t.Fatal(err)
	}
	if s.identity.UserID != "browser-owner" || s.identity.Email != "owner@example.com" || s.Surface != control.SurfaceHTTPMCP || !s.Grants["terminal.start"] || s.conversationMCPClient() != s.Principal {
		t.Fatalf("lost original browser admission: %+v", s)
	}
	local := &Server{Version: "test", Cfg: cfg, Principal: "local-agent"}
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
	writeRecoveryAuthority(t, childDir, egg.UnsandboxedEggConfig())
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
		if err := os.WriteFile(filepath.Join(target, "egg.meta"), []byte("agent=claude\ncwd="+c.CWD+"\nprovider_home="+eggclient.EffectiveSessionHome(s.Cfg, s.identity)+"\n"), 0600); err != nil {
			return err
		}
		if err := eggclient.WriteSessionPrincipal(target, root.OwnerID); err != nil {
			return err
		}
		if err := egg.WriteLaunchIntent(target, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: c.CWD, Started: true, ProviderSessionID: "provider", ConversationID: c.ID, RootConversationID: c.RootID, AutoBoot: opts.RecoveryBoot}); err != nil {
			return err
		}
		writeRecoveryAuthority(t, target, egg.UnsandboxedEggConfig())
		return nil
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
	record, err := egg.ReadRecoveryRecord(dir)
	intent := record.Intent
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

func TestRecoveryRefusesForgedEggDirectoryAuthority(t *testing.T) {
	for _, field := range []string{"config", "provider", "owner", "cwd", "principal", "agent", "missing authority"} {
		t.Run(field, func(t *testing.T) {
			s, _, _, dir := recoverCoordinatorFixture(t)
			policyPath := filepath.Join(s.Cfg.Dir, "original.yaml")
			if err := os.WriteFile(policyPath, []byte("fs: [rw:"+s.Cfg.Dir+"]\nnetwork: none\n"), 0600); err != nil {
				t.Fatal(err)
			}
			policy, err := egg.ResolveEggConfig(policyPath)
			if err != nil {
				t.Fatal(err)
			}
			writeRecoveryAuthority(t, dir, policy)
			forged, err := egg.ReadLaunchIntent(dir)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "config":
				wide := filepath.Join(s.Cfg.Dir, "forged.yaml")
				if err := os.WriteFile(wide, []byte("base: none\nfs: [rw:/]\nnetwork: ['*']\nenv: ['*']\n"), 0600); err != nil {
					t.Fatal(err)
				}
				forged.EggConfig = wide
			case "provider":
				forged.ProviderSessionID = "forged-provider"
			case "owner":
				if err := eggclient.WriteEggOwner(dir, "forged-owner", "forged@example.com"); err != nil {
					t.Fatal(err)
				}
			case "cwd":
				forged.CWD = t.TempDir()
			case "principal":
				if err := eggclient.WriteSessionPrincipal(dir, "forged-principal"); err != nil {
					t.Fatal(err)
				}
			case "agent":
				forged.Agent = "codex"
			case "missing authority":
				if err := os.Remove(filepath.Join(egg.RecoveryDir(dir), "source.json")); err != nil {
					t.Fatal(err)
				}
			}
			// This write models provider-controlled metadata, bypassing the
			// trusted host's UpdateLaunchIntent helper.
			if err := egg.WriteLaunchIntent(dir, forged); err != nil {
				t.Fatal(err)
			}
			s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
				t.Fatal("forged authority reached provider launch")
				return nil
			}
			if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
				t.Fatal("forged authority admitted")
			}
			if got := eggclient.ClassifyEgg(s.Cfg, "source"); got.Class != eggclient.RecoveryArchived {
				t.Fatalf("forged authority recoverable: %+v", got)
			}
		})
	}
}

func TestRecoveryRefusesChangedPolicyAndInheritedBase(t *testing.T) {
	for _, changed := range []string{"config", "base"} {
		t.Run(changed, func(t *testing.T) {
			s, _, _, dir := recoverCoordinatorFixture(t)
			base := filepath.Join(s.Cfg.Dir, "base.yaml")
			path := filepath.Join(s.Cfg.Dir, "original.yaml")
			if err := os.WriteFile(base, []byte("fs: [rw:"+s.Cfg.Dir+"]\nnetwork: none\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("base: ./base.yaml\n"), 0600); err != nil {
				t.Fatal(err)
			}
			policy, err := egg.ResolveEggConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			writeRecoveryAuthority(t, dir, policy)
			if changed == "base" {
				path = base
			}
			if err := os.WriteFile(path, []byte("base: none\nfs: [rw:/]\nnetwork: ['*']\nenv: ['*']\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
				t.Fatal("changed policy reached provider launch")
				return nil
			}
			if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
				t.Fatal("changed policy admitted")
			}
		})
	}
}

func TestRecoveryRefusesChangedProviderRouting(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "https://original.example.com")
	s, _, _, _ := recoverCoordinatorFixture(t)
	t.Setenv("WT_PROVIDER_BASE_URL", "https://wider.example.com")
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
		t.Fatal("changed provider routing reached launch")
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
		t.Fatal("provider routing ceiling changed")
	}
}

func TestRecoveryRefusesChangedFilesystemSymlink(t *testing.T) {
	s, _, _, dir := recoverCoordinatorFixture(t)
	alias := filepath.Join(s.Cfg.Dir, "allowed")
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Cfg.Dir, "sandbox.yaml")
	if err := os.WriteFile(path, []byte("fs: [rw:"+s.Cfg.Dir+", rw:"+alias+"]\nnetwork: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := egg.ResolveEggConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryAuthority(t, dir, policy)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
		t.Fatal("filesystem symlink widened the recorded ceiling")
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
		t.Fatal("changed filesystem symlink admitted")
	}
}

func TestRecoveryRefusesChangedConversationWorkspace(t *testing.T) {
	s, db, root, _ := recoverCoordinatorFixture(t)
	if _, err := db.DB().Exec(`UPDATE conversations SET cwd = ? WHERE id = ?`, t.TempDir(), root.ID); err != nil {
		t.Fatal(err)
	}
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error {
		t.Fatal("changed conversation workspace reached launch")
		return nil
	}
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err == nil {
		t.Fatal("conversation widened recorded workspace")
	}
}

func TestRecoveryRestoresProtectedExecutionMode(t *testing.T) {
	for _, unsandboxed := range []bool{true, false} {
		t.Run(map[bool]string{true: "unsandboxed", false: "sandboxed"}[unsandboxed], func(t *testing.T) {
			s, _, _, dir := recoverCoordinatorFixture(t)
			s.Unsandboxed = !unsandboxed // the recovering caller has a different default
			if !unsandboxed {
				path := filepath.Join(s.Cfg.Dir, "sandbox.yaml")
				if err := os.WriteFile(path, []byte("fs: [rw:"+s.Cfg.Dir+"]\nnetwork: none\n"), 0600); err != nil {
					t.Fatal(err)
				}
				policy, err := egg.ResolveEggConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				writeRecoveryAuthority(t, dir, policy)
			}
			starts := 0
			s.startContinuation = func(_ *store.Conversation, policy *egg.EggConfig, _ eggclient.SpawnEggOpts) error {
				starts++
				if egg.RequiresSandbox(policy, "claude") == unsandboxed {
					t.Fatalf("lost original execution mode: %+v", policy)
				}
				return nil
			}
			if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err != nil {
				t.Fatal(err)
			}
			if starts != 1 {
				t.Fatalf("starts: %d", starts)
			}
		})
	}
}

func TestRecoveryExpandsCurrentHomeRelativePaths(t *testing.T) {
	s, _, root, _ := recoverCoordinatorFixture(t)
	t.Setenv("HOME", filepath.Dir(root.CWD))
	wc := &config.WingConfig{Paths: config.PathList{{Path: "~/" + filepath.Base(root.CWD)}}}
	if err := config.SaveWingConfig(s.Cfg.Dir, wc); err != nil {
		t.Fatal(err)
	}
	s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error { return nil }
	if _, err := s.ToolSessionRecover(context.Background(), json.RawMessage(`{"session":"source"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAutoRechecksCurrentOwnerACLAndArchivesRevocation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "revoked"}[revoked], func(t *testing.T) {
			s, db, root, dir := recoverCoordinatorFixture(t)
			owner, email := "browser-owner", "owner@example.com"
			principal := roostSessionPrincipal(owner)
			s.Principal = principal
			if _, err := db.DB().Exec(`UPDATE conversations SET owner_id = ? WHERE id = ?`, principal, root.ID); err != nil {
				t.Fatal(err)
			}
			if err := eggclient.WriteSessionPrincipal(dir, principal); err != nil {
				t.Fatal(err)
			}
			if err := eggclient.WriteEggOwner(dir, owner, email); err != nil {
				t.Fatal(err)
			}
			writeRecoveryAuthority(t, dir, egg.UnsandboxedEggConfig())
			wc := &config.WingConfig{Recover: "coordinators", Paths: config.PathList{{Path: root.CWD, Members: []string{email}}}}
			if err := config.SaveWingConfig(s.Cfg.Dir, wc); err != nil {
				t.Fatal(err)
			}
			if err := ConfigureRecoveryClient(s, root.SessionID); err != nil {
				t.Fatal(err)
			}
			if revoked {
				wc.Paths[0].Members = []string{"other@example.com"}
				if err := config.SaveWingConfig(s.Cfg.Dir, wc); err != nil {
					t.Fatal(err)
				}
			}
			starts := 0
			s.startContinuation = func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error { starts++; return nil }
			runSessionRecovery(context.Background(), s.Cfg, wc, false, "boot-acl", func(eggclient.RecoverySession) *Server { return s })
			want := 1
			if revoked {
				want = 0
				record, err := egg.ReadRecoveryRecord(dir)
				if err != nil || !record.Archived || eggclient.ClassifyEgg(s.Cfg, root.SessionID).Class != eggclient.RecoveryArchived {
					t.Fatalf("revocation not archived: %+v %v", record, err)
				}
			}
			if starts != want {
				t.Fatalf("starts: %d, want %d", starts, want)
			}
		})
	}
}
