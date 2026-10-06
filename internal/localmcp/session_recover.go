package localmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

const recoveryPrompt = "The host or Wingthing restarted and interrupted this coordinator. Resume your existing task using the same conversation and DotID. Read the conversation and reconcile completed child work before continuing."

func (s *Server) ToolSessionRecover(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		List    bool   `json:"list"`
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.List && args.Session != "" {
		return nil, errors.New("list and session are mutually exclusive")
	}
	if args.Session != "" {
		return s.recoverSession(ctx, args.Session, "")
	}
	sessions, err := eggclient.DiscoverRecoverableSessions(s.Cfg)
	if err != nil {
		return nil, err
	}
	owned := []eggclient.RecoverySession{}
	for _, session := range sessions {
		if _, err := s.resolveOwnedLifecycleSession(session.ID); err == nil {
			if s.BoundConversation != "" {
				db, err := s.openMessageStore()
				if err != nil {
					return nil, err
				}
				_, err = s.ownedConversation(db, session.ConversationID)
				_ = db.Close()
				if err != nil {
					continue
				}
			}
			owned = append(owned, session)
		}
	}
	return map[string]any{"sessions": owned}, ctx.Err()
}

func (s *Server) recoverSession(ctx context.Context, id, boot string) (result map[string]any, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := eggclient.AcquireRecoveryLock(s.Cfg, id)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	source := eggclient.ClassifyEgg(s.Cfg, id)
	if source.Class != eggclient.RecoveryEligible {
		return nil, errors.New("session is not recoverable")
	}
	if _, err := s.resolveOwnedLifecycleSession(id); err != nil {
		return nil, err
	}
	wc, err := config.LoadWingConfig(s.Cfg.Dir)
	if err != nil {
		return nil, err
	}
	if wc.Locked {
		return nil, errors.New("recovery requires an unlocked wing")
	}
	dir := filepath.Join(s.Cfg.Dir, "eggs", id)
	var conversation *store.Conversation
	var db *store.Store
	if source.ConversationID != "" {
		db, err = s.openMessageStore()
		if err != nil {
			return nil, err
		}
		defer db.Close()
		conversation, err = s.ownedConversation(db, source.ConversationID)
		if err != nil {
			return nil, err
		}
		if conversation.SessionID != id {
			return nil, errors.New("recover the current conversation execution")
		}
	}
	if boot != "" {
		if wc.Org != "" || s.identity.SharedHost || conversation == nil || conversation.ParentID != "" || conversation.ID != conversation.RootID || conversation.Agent != "claude" {
			return nil, errors.New("automatic recovery requires a root coordinator")
		}
		executions, err := db.ConversationExecutions(conversation.ID)
		if err != nil {
			return nil, err
		}
		for _, execution := range executions {
			intent, err := egg.ReadLaunchIntent(filepath.Join(s.Cfg.Dir, "eggs", execution))
			if err == nil && (intent.AutoBoot == boot || intent.RetryAfter > time.Now().Unix()) {
				return map[string]any{"skipped": true}, nil
			}
		}
		claimed, err := eggclient.ClaimAutoRecovery(dir, boot, time.Now())
		if err != nil {
			return nil, err
		}
		if !claimed {
			return map[string]any{"skipped": true}, nil
		}
	}
	defer func() {
		if resultErr != nil {
			if err := eggclient.RecordRecoveryFailure(dir, resultErr, boot != "", time.Now()); err != nil {
				resultErr = fmt.Errorf("%w; persist recovery failure: %v", resultErr, err)
			}
		}
	}()
	if info, err := os.Stat(source.CWD); err != nil || !info.IsDir() {
		return nil, errors.New("recovery workspace is unavailable")
	}
	paths := wingpolicy.CanonicalPaths(wc.Paths.Strings())
	if len(paths) > 0 && !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(source.CWD), paths) {
		return nil, errors.New("recovery workspace is outside current wing path policy")
	}
	definition, _ := agentpkg.LookupDefinition(source.Agent)
	if _, err := exec.LookPath(definition.Command); err != nil && s.startContinuation == nil {
		return nil, fmt.Errorf("recovery provider unavailable: %w", err)
	}
	// The source boot claim is also inherited by the replacement. If that
	// execution is interrupted in this boot it cannot auto-launch a second time.
	if updated, err := egg.ReadLaunchIntent(dir); err == nil {
		source.Intent = updated
	}
	home := eggclient.EffectiveSessionHome(s.Cfg, s.identity)
	meta := eggclient.ReadEggMetaValues(dir)
	if recorded, err := eggclient.LifecycleProviderHome(s.Cfg, meta["provider_home"]); err != nil || wingpolicy.CanonicalSessionPath(recorded) != wingpolicy.CanonicalSessionPath(home) {
		return nil, errors.New("recovery provider home does not match the caller")
	}
	// Refresh exact Claude history after reboot, including work newer than the
	// last periodic capture. Other providers resume their recorded native ID.
	if source.Agent == "claude" {
		if err := egg.CaptureSessionHistory(source.Agent, source.CWD, dir, home, time.Time{}, source.Intent.ProviderSessionID); err != nil {
			return nil, err
		}
	}
	if conversation != nil && conversation.ParentID == "" && source.Agent == "claude" {
		model := source.Intent.Model
		if model == "" {
			model = archivedCoordinatorModel(dir)
		}
		target := cmdutil.NewRuntimeID()
		inputHash := sha256.Sum256([]byte(recoveryPrompt))
		turn, _, err := db.ReserveConversationContinuation(store.ConversationContinuation{OwnerID: s.clientPrincipal(), RequestID: "recovery-" + target, ConversationID: conversation.ID, SourceSession: id, SessionID: target, ProviderSessionID: source.Intent.ProviderSessionID, InputSHA256: hex.EncodeToString(inputHash[:]), SpecDigest: "recovery-" + id})
		if err != nil {
			return nil, err
		}
		invocation := *conversation
		invocation.SessionID = turn.SessionID
		launchErr := s.launchHeadlessContinuation(&invocation, turn, model, recoveryPrompt, &source)
		state, detail := "started", ""
		if launchErr != nil {
			state, detail = "failed", launchErr.Error()
		}
		if err := db.SetConversationContinuationLaunch(turn, state, detail); err != nil {
			return nil, err
		}
		if launchErr != nil {
			return nil, launchErr
		}
		turn.LaunchState = state
		result = continuationLaunchResult(conversation, turn, false)
	} else {
		configPath := source.Intent.EggConfig
		if configPath == "" {
			configPath = wc.EggConfig
		}
		eggCfg, err := eggclient.LoadSpawnEggConfig(configPath, source.CWD, false)
		if err != nil {
			return nil, err
		}
		target := cmdutil.NewRuntimeID()
		opts := recoverySpawnOpts(source)
		err = s.admitSpawn(func() error {
			if err := s.preflightBrokerChild(eggCfg, source.Agent, source.CWD, target); err != nil {
				return err
			}
			if s.broker != nil {
				opts = s.broker.launchOpts(s.Cfg, opts)
			}
			if _, err := os.Stat(filepath.Join(dir, "chat.meta")); err == nil && source.Agent == "claude" {
				provider, err := egg.RestoreSessionHistory(source.Agent, source.CWD, dir, home)
				if err != nil {
					return err
				}
				if provider != source.Intent.ProviderSessionID {
					return errors.New("restored provider does not match recovery intent")
				}
			}
			client, err := eggclient.SpawnEgg(s.Cfg, target, source.Agent, eggCfg, 24, 80, source.CWD, false, false, false, s.identity, 0, opts)
			if err != nil {
				return err
			}
			defer client.Close()
			if err := InheritConversationExecution(s.Cfg, id, target); err != nil {
				_ = client.Kill(ctx, target)
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		result = map[string]any{"session": target, "source_session": id, "conversation_id": source.ConversationID, "root_conversation_id": source.RootConversationID}
	}
	target := result["session"].(string)
	if err := egg.UpdateLaunchIntent(dir, func(i *egg.LaunchIntent) { i.RecoveredSession = target; i.RecoveryError = "" }); err != nil {
		return nil, err
	}
	result["recovered_from"] = id
	result["lifecycle"] = eggclient.SessionLifecycleSummary(ctx, s.Cfg, target)
	return result, nil
}

func recoverySpawnOpts(source eggclient.RecoverySession) eggclient.SpawnEggOpts {
	args := []string{}
	if source.Intent.Model != "" {
		args = append(args, "--model", source.Intent.Model)
	}
	return eggclient.SpawnEggOpts{Label: source.Name, Kind: "agent", AgentArgs: args, Principal: source.Principal,
		ResumeSessionID: source.Intent.ProviderSessionID, ResumeSourceSessionID: source.ID,
		RecoveredFrom: source.ID, RecoveryBoot: source.Intent.AutoBoot, RecoveryConversation: source.ConversationLink}
}

// RunSessionRecovery is a single startup pass, independent of relay connection.
// Source locks and persisted boot claims also cover wing daemon restarts.
func RunSessionRecovery(version string, ctx context.Context, cfg *config.Config, wc *config.WingConfig, shared bool) {
	if wc.Recover != "coordinators" || wc.Locked || wc.Org != "" || shared {
		return
	}
	boot, err := eggclient.RecoveryBootID()
	if err != nil || boot == "" {
		log.Printf("session recovery boot identity: %v", err)
		return
	}
	runSessionRecovery(ctx, cfg, wc, shared, boot, func(source eggclient.RecoverySession) *Server {
		return &Server{Version: version, Cfg: cfg, Principal: source.Principal, Logs: os.Stderr,
			allowedPaths: wingpolicy.CanonicalPaths(wc.Paths.Strings()), enforcePathBounds: len(wc.Paths) > 0,
			identity: eggclient.EggIdentity{UserID: eggclient.ReadEggOwner(filepath.Join(cfg.Dir, "eggs", source.ID)), Email: eggclient.ReadEggOwnerEmail(filepath.Join(cfg.Dir, "eggs", source.ID))}}
	})
}

func runSessionRecovery(ctx context.Context, cfg *config.Config, wc *config.WingConfig, shared bool, boot string, server func(eggclient.RecoverySession) *Server) {
	if wc.Recover != "coordinators" || wc.Locked || wc.Org != "" || shared {
		return
	}
	sessions, err := eggclient.DiscoverRecoverableSessions(cfg)
	if err != nil {
		log.Printf("session recovery inventory: %v", err)
		return
	}
	for _, source := range sessions {
		if ctx.Err() != nil {
			return
		}
		if source.ConversationID == "" || source.ParentConversationID != "" || source.ConversationID != source.RootConversationID {
			continue
		}
		s := server(source)
		if err := ConfigureRecoveryClient(s, source.ID); err != nil {
			log.Printf("session recovery client %s: %v", source.ID, err)
			continue
		}
		if _, err := s.recoverSession(ctx, source.ID, boot); err != nil {
			log.Printf("session recovery %s: %v", source.ID, err)
		}
	}
}

// Recovering a parent must bind its MCP client to the current clients.yaml
// entry for the original owner, including current grants and spawn bounds.
func ConfigureRecoveryClient(s *Server, session string) error {
	dir := filepath.Join(s.Cfg.Dir, "eggs", session)
	owner := eggclient.ReadEggOwner(dir)
	if owner != "" && s.clientPrincipal() == roostSessionPrincipal(owner) {
		wc, err := config.LoadWingConfig(s.Cfg.Dir)
		if err != nil {
			return err
		}
		// Browser/remote parents were admitted through the native authenticated
		// surface, independently of unrelated local clients.yaml entries.
		s.identity.UserID, s.identity.Email, s.identity.OrgWing = owner, eggclient.ReadEggOwnerEmail(dir), wc.Org != ""
		s.Surface, s.Actor, s.Grants = control.SurfaceHTTPMCP, "recovery", GrantSet(defaultDirectMCPGrants)
		s.MaxSessions, s.MaxSpawnsPerHour = defaultDirectMCPMaxSessions, defaultDirectMCPMaxSpawnsPerHour
		return nil
	}
	clients, err := LoadLocalMCPClientsConfig(s.Cfg)
	if err != nil {
		return err
	}
	principal := s.clientPrincipal()
	name := ""
	var entry localMCPClientConfig
	for candidate, configured := range clients.Clients {
		owner := configured.Owner
		if owner == "" {
			owner = candidate
		}
		if owner != principal {
			continue
		}
		if name != "" {
			return errors.New("recovery owner has multiple MCP clients; recover through the intended client")
		}
		name, entry = candidate, configured
	}
	if name == "" {
		if clients.RequireClient || len(clients.Clients) > 0 {
			return errors.New("recovery owner no longer has an MCP client")
		}
		s.Actor = principal
		return nil
	}
	s.Actor, s.Grants = name, GrantSet(entry.Grants)
	s.MaxSessions, s.MaxSpawnsPerHour = entry.Bounds.MaxSessions, entry.Bounds.MaxSpawnsPerHour
	return nil
}
