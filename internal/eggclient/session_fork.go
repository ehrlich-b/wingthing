package eggclient

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

const sessionLaunchConfigFile = "session.launch.json"

type sessionLaunchConfig struct {
	Config string `json:"config"`
	Model  string `json:"model,omitempty"`
}

// SaveSessionLaunchConfig retains the effective policy and selected model, not
// generated provider IDs, prompts, or another execution's MCP binding.
func SaveSessionLaunchConfig(dir string, cfg *egg.EggConfig, args []string) error {
	rendered, err := cfg.YAML()
	if err != nil {
		return err
	}
	launch := sessionLaunchConfig{Config: rendered}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "--model" && i+1 < len(args) {
			i++
			launch.Model = args[i]
		} else if value, ok := strings.CutPrefix(args[i], "--model="); ok {
			launch.Model = value
		}
	}
	data, err := json.Marshal(launch)
	if err != nil {
		return err
	}
	return daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, sessionLaunchConfigFile), data, 0600)
}

func readForkLaunchConfig(dir string) (*egg.EggConfig, string, error) {
	path := filepath.Join(dir, sessionLaunchConfigFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, "", errors.New("source launch settings were not captured; start a new session with an updated wing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var launch sessionLaunchConfig
	if err := json.Unmarshal(data, &launch); err != nil || launch.Config == "" {
		return nil, "", errors.New("source launch settings are invalid")
	}
	cfg, err := egg.LoadEggConfigFromYAML(launch.Config)
	return cfg, launch.Model, err
}

func forkProviderID(dir, agent, cwd string) (string, error) {
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
		return egg.ParseChatMeta(string(data))["agent_session_id"], nil
	}
	// Live sources need not have reached the periodic archive capture yet.
	// Use their pinned identity, never the newest provider file in a workspace.
	meta := ReadEggMetaValues(dir)
	id := meta["provider_session_id"]
	path := filepath.Join(dir, ProviderResumeMetadataFile)
	info, err := os.Lstat(path)
	if !ValidProviderSessionID(id) || err != nil || !info.Mode().IsRegular() {
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
	native := filepath.Join(meta["provider_home"], egg.Profile(agent).SessionDir, strings.ReplaceAll(cwd, "/", "-"), id+".jsonl")
	if info, err := os.Lstat(native); meta["provider_home"] == "" || err != nil || !info.Mode().IsRegular() {
		return "", errors.New("provider conversation was not captured")
	}
	return id, nil
}

func SessionForkStatus(dir, agent, cwd string) (bool, string) {
	if _, err := forkProviderID(dir, agent, cwd); err != nil {
		return false, err.Error()
	}
	if _, _, err := readForkLaunchConfig(dir); err != nil {
		return false, err.Error()
	}
	return true, ""
}

type SessionForkPlan struct {
	SessionID    string
	Source       LocalSession
	Config       *egg.EggConfig
	Options      SpawnEggOpts
	Conversation *store.Conversation
}

type SessionForkScope struct {
	Principal         string
	Identity          EggIdentity
	AllowedPaths      []string
	EnforcePathBounds bool
	Admit             func(func() error) error
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
	source, err := ResolveLifecycleSession(cfg, sourceRef)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", source.ID)
	principal := scope.Principal
	if principal == "" {
		principal = "default"
	}
	owns := source.Principal == principal || principal == "default" && source.Principal == ""
	cwd := wingpolicy.CanonicalSessionPath(source.CWD)
	paths := wingpolicy.CanonicalPaths(scope.AllowedPaths)
	if !owns || scope.Identity.UserID != "" && ReadEggOwner(dir) != scope.Identity.UserID || scope.EnforcePathBounds && (len(paths) == 0 || !wingpolicy.IsUnderPaths(cwd, paths)) {
		return nil, errors.New("session not found or not owned by caller")
	}
	if recordedHome := ReadEggMetaValues(dir)["provider_home"]; recordedHome != "" && wingpolicy.CanonicalSessionPath(recordedHome) != wingpolicy.CanonicalSessionPath(EffectiveSessionHome(cfg, scope.Identity)) {
		return nil, errors.New("source provider home does not match the caller's execution identity")
	}
	providerID, err := forkProviderID(dir, source.Agent, cwd)
	if err != nil {
		return nil, err
	}
	eggCfg, model, err := readForkLaunchConfig(dir)
	if err != nil {
		return nil, err
	}
	if source.CWD == "" {
		return nil, errors.New("source working directory is unavailable")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, errors.New("source working directory is unavailable")
	}
	source.CWD = cwd
	id := cmdutil.NewRuntimeID()
	if label == "" {
		label = "fork-" + id
	}
	if err := ValidateSessionName(label); err != nil {
		return nil, err
	}
	plan := &SessionForkPlan{SessionID: id, Source: source, Config: eggCfg,
		Options: SpawnEggOpts{ForkSession: true, Label: label, Kind: "agent", Principal: principal, ResumeSessionID: providerID, ResumeSourceSessionID: source.ID}}
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
		release, err := BrowserProviderResumes.ReserveFork(cfg, home, source.Agent, providerID, source.ID, id)
		if err != nil {
			return err
		}
		spawned := false
		defer func() { release(spawned) }()
		historyDir := dir
		if _, alive := ReadAliveEggPID(dir); alive {
			// Capture to disposable state: never rewrite the original egg or its
			// native provider conversation while it is still running.
			historyDir, err = os.MkdirTemp(filepath.Join(cfg.Dir, "eggs"), ".fork-history-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(historyDir) }()
			if err := egg.CaptureSessionHistory(source.Agent, cwd, historyDir, home, time.Time{}, providerID); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(historyDir, "chat.meta")); err != nil {
				return errors.New("provider conversation was not captured")
			}
		} else {
			restoredID, err := egg.RestoreSessionHistory(source.Agent, cwd, dir, home)
			if err != nil {
				return fmt.Errorf("restore provider conversation: %w", err)
			}
			if restoredID != providerID {
				return errors.New("source provider identity changed during fork")
			}
		}
		if nativeModel := forkTranscriptModel(historyDir); nativeModel != "" {
			model = nativeModel
		}
		if model != "" {
			if strings.ContainsAny(model, "\x00\r\n") || len(model) > 128 {
				return errors.New("source model is invalid")
			}
			plan.Options.AgentArgs = []string{"--model", model}
		}
		plan.Conversation, err = reserveForkConversation(cfg, source, id, label, principal)
		if err != nil {
			return err
		}
		if scope.Prepare != nil {
			err = scope.Prepare(plan)
		}
		if err == nil {
			if scope.Spawn != nil {
				err = scope.Spawn(plan)
			} else {
				var client *egg.Client
				client, err = SpawnEgg(cfg, id, source.Agent, plan.Config, 24, 80, cwd, false, false, false, scope.Identity, 0, plan.Options)
				if err == nil {
					cmdutil.CloseWithLog("forked session client", client)
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

func forkTranscriptModel(dir string) string {
	file, err := os.Open(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		return ""
	}
	defer cmdutil.CloseWithLog("fork transcript", file)
	reader, err := gzip.NewReader(file)
	if err != nil {
		return ""
	}
	defer cmdutil.CloseWithLog("fork transcript gzip", reader)
	scanner := bufio.NewScanner(io.LimitReader(reader, 16<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	model := ""
	for scanner.Scan() {
		var record struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Type == "assistant" && record.Message.Model != "" {
			model = record.Message.Model
		}
	}
	return model
}
