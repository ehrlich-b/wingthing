package eggclient

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func forkProviderID(cfg *config.Config, dir, agent, cwd string) (string, error) {
	if agent != "claude" {
		return "", fmt.Errorf("agent %q does not support session fork; only Claude supports --fork-session", agent)
	}
	if _, alive := ReadAliveEggPID(dir); !alive {
		if ok, reason := SessionResumeStatus(dir, agent, cwd); !ok {
			return "", errors.New(reason)
		}
		data, err := os.ReadFile(filepath.Join(dir, "chat.meta"))
		if err != nil {
			return "", err
		}
		id := egg.ParseChatMeta(string(data))["agent_session_id"]
		if !validForkProviderID(id) {
			return "", errors.New("provider conversation metadata is invalid")
		}
		return id, nil
	}
	// Live sources need not have reached the periodic archive capture yet.
	// Follow only their own recorded SessionStart bindings, never the newest
	// provider file in a workspace.
	meta := ReadEggMetaValues(dir)
	id := meta["provider_session_id"]
	path := filepath.Join(dir, ProviderResumeMetadataFile)
	info, err := os.Lstat(path)
	if !validForkProviderID(id) || err != nil || !info.Mode().IsRegular() {
		return "", errors.New("provider conversation identity was not verified")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	reservation := egg.ParseChatMeta(string(data))
	if reservation["agent"] != agent || reservation["provider_session_id"] != id {
		return "", errors.New("provider conversation identity was not verified")
	}
	home, err := LifecycleProviderHome(cfg, meta["provider_home"])
	if err != nil {
		return "", err
	}
	id, err = egg.ResolveRecordedProviderSessionID(dir, agent, home, id)
	if err != nil {
		return "", err
	}
	if !validForkProviderID(id) {
		return "", errors.New("provider conversation metadata is invalid")
	}
	return id, nil
}

func validForkProviderID(id string) bool {
	if !ValidProviderSessionID(id) || id[0] == '-' {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func SessionForkStatus(cfg *config.Config, dir, agent, cwd string) (bool, string) {
	if _, err := forkProviderID(cfg, dir, agent, cwd); err != nil {
		return false, err.Error()
	}
	return true, ""
}

type SessionForkPlan struct {
	SessionID    string
	Source       LocalSession
	Config       *egg.EggConfig
	Identity     EggIdentity
	Options      SpawnEggOpts
	Conversation *store.Conversation
}

type SessionForkScope struct {
	Principal         string
	Identity          EggIdentity
	AllowedPaths      []string
	EnforcePathBounds bool
	TraceFromConfig   bool
	IdleTimeout       time.Duration
	Admit             func(func() error) error
	CheckSpawn        func() error // recheck capacity under the session-name lock
	Tools             []*config.ToolConfig
	LoadConfig        func(string) (*egg.EggConfig, error)
	Prepare           func(*SessionForkPlan) error
	Spawn             func(*SessionForkPlan) error
}

type SessionForkResult struct {
	ConversationLink
	Session       string `json:"session"`
	SourceSession string `json:"source_session"`
	Label         string `json:"label"`
	Agent         string `json:"agent"`
	CWD           string `json:"cwd"`
}

// ForkSession creates a distinct execution and logical sibling. All adapters
// use this owner/path check and the existing name, provider, and spawn locks.
func ForkSession(ctx context.Context, cfg *config.Config, sourceRef, label string, scope SessionForkScope) (*SessionForkResult, error) {
	principal := scope.Principal
	if principal == "" {
		principal = "default"
	}
	owns := func(session LocalSession) bool {
		if scope.Identity.UserID != "" {
			return ReadEggOwner(filepath.Join(cfg.Dir, "eggs", session.ID)) == scope.Identity.UserID
		}
		return session.Principal == principal || principal == "default" && session.Principal == ""
	}
	source, err := ResolveOwnedLifecycleSession(cfg, sourceRef, owns)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", source.ID)
	cwd := wingpolicy.CanonicalSessionPath(source.CWD)
	paths := wingpolicy.CanonicalPaths(scope.AllowedPaths)
	if scope.EnforcePathBounds && (len(paths) == 0 || !wingpolicy.IsUnderPaths(cwd, paths)) {
		return nil, errors.New("session not found or not owned by caller")
	}
	if source.CWD == "" {
		return nil, errors.New("source working directory is unavailable")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, errors.New("source working directory is unavailable")
	}
	source.CWD = cwd
	// Policy and execution identity belong to a fresh launch by this caller.
	// Source artifacts select history only; none can supply launch authority.
	loadConfig := scope.LoadConfig
	if loadConfig == nil {
		loadConfig = func(cwd string) (*egg.EggConfig, error) { return LoadSpawnEggConfig("", cwd, false) }
	}
	eggCfg, err := loadConfig(cwd)
	if err != nil {
		return nil, err
	}
	id := cmdutil.NewRuntimeID()
	if label == "" {
		label = "fork-" + id
	}
	if err := ValidateSessionName(label); err != nil {
		return nil, err
	}
	plan := &SessionForkPlan{SessionID: id, Source: source, Config: eggCfg, Identity: scope.Identity,
		Options: SpawnEggOpts{ForkSession: true, Label: label, Kind: "agent", Principal: principal, ResumeSourceSessionID: source.ID}}
	launch := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err := AcquireSessionNameLock(cfg)
		if err != nil {
			return err
		}
		defer cmdutil.CloseWithLog("fork session name lock", lock)
		if err := EnsureSessionNameAvailable(cfg, label, id); err != nil {
			return err
		}
		plan.Options.nameLock = lock
		home := EffectiveSessionHome(cfg, scope.Identity)
		providerID, err := forkProviderID(cfg, dir, source.Agent, cwd)
		if err != nil {
			return err
		}
		plan.Options.ResumeSessionID = providerID
		release, err := BrowserProviderResumes.ReserveFork(cfg, home, source.Agent, providerID, source.ID, id)
		if err != nil {
			return err
		}
		spawned := false
		defer func() { release(spawned) }()
		if _, alive := ReadAliveEggPID(dir); alive {
			native, err := egg.OpenRecordedSessionHistory(source.Agent, cwd, dir, home, providerID)
			if err != nil {
				return err
			}
			defer cmdutil.CloseWithLog("fork source history", native)
		} else {
			restoredID, err := egg.RestoreSessionHistory(source.Agent, cwd, dir, home)
			if err != nil {
				return fmt.Errorf("restore provider conversation: %w", err)
			}
			if restoredID != providerID {
				return errors.New("source provider identity changed during fork")
			}
		}
		plan.Conversation, err = reserveForkConversation(cfg, source, id, label, principal)
		if err != nil {
			return err
		}
		if scope.Prepare != nil {
			err = scope.Prepare(plan)
		}
		var toolListener *egg.ToolListener
		if err == nil {
			toolListener, err = PrepareBrowserTools(cfg, id, scope.Tools, &plan.Options, plan.Identity)
		}
		defer func() {
			if toolListener != nil {
				cmdutil.CloseWithLog("forked session tool listener", toolListener)
			}
		}()
		if err == nil && scope.CheckSpawn != nil {
			err = scope.CheckSpawn()
		}
		if err == nil {
			if scope.Spawn != nil {
				err = scope.Spawn(plan)
			} else {
				var client *egg.Client
				client, err = SpawnEgg(cfg, id, source.Agent, plan.Config, 24, 80, cwd, false, false, scope.TraceFromConfig && plan.Config.Trace, plan.Identity, scope.IdleTimeout, plan.Options)
				if err == nil {
					if toolListener != nil {
						go serveBrowserSessionTools(client, toolListener, id)
						toolListener = nil
					} else {
						cmdutil.CloseWithLog("forked session client", client)
					}
				}
			}
		}
		spawned = err == nil
		if plan.Conversation != nil {
			db, openErr := store.Open(cfg.DBPath())
			if openErr != nil {
				return errors.Join(err, openErr)
			}
			defer cmdutil.CloseWithLog("fork conversation store", db)
			state, detail := "started", ""
			if err != nil {
				state, detail = "failed", err.Error()
			}
			err = errors.Join(err, db.SetConversationLaunch(plan.Conversation.ID, state, detail))
		}
		return err
	}
	if scope.Admit != nil {
		err = scope.Admit(launch)
	} else {
		err = launch()
	}
	if err != nil {
		return nil, err
	}
	return &SessionForkResult{ConversationLink: linkForConversation(plan.Conversation), Session: id, SourceSession: source.ID, Label: label, Agent: source.Agent, CWD: cwd}, nil
}

func reserveForkConversation(cfg *config.Config, source LocalSession, target, title, principal string) (*store.Conversation, error) {
	if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("fork conversation store", db)
	c, err := db.ConversationForSession(source.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.OwnerID != principal {
		return nil, errors.New("conversation not found or not owned by caller")
	}
	// Roots fork into independent roots; a child keeps the source's parent.
	sibling, _, err := db.ReserveConversation(store.Conversation{ID: cmdutil.NewRuntimeID(), OwnerID: principal, ParentID: c.ParentID, Title: title, Agent: source.Agent, CWD: source.CWD, WingID: c.WingID, SessionID: target, LaunchKey: "fork-" + target, SpecDigest: source.ID})
	return sibling, err
}
