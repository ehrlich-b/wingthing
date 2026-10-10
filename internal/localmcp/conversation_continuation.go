package localmcp

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

const coordinatorModelFile = "coordinator.model"

type continuationArgs struct {
	SourceSession string `json:"resume_session"`
	Role          string `json:"conversation_role"`
	Input         string `json:"input"`
	RequestID     string `json:"request_id"`
}

func validContinuationModel(model string) bool {
	return strings.HasPrefix(model, "claude-") && len(model) <= 128 && strings.TrimSpace(model) == model && !strings.ContainsFunc(model, unicode.IsControl)
}

func saveCoordinatorModel(cfgDir, session, model string) error {
	if !validContinuationModel(model) {
		return nil
	}
	dir := filepath.Join(cfgDir, "eggs", session)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, coordinatorModelFile), []byte(model), 0600)
}

// Older executions have no launch-time model record. Native assistant records
// carry the exact model; never turn an alias or a default into invented evidence.
func archivedCoordinatorModel(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, coordinatorModelFile)); err == nil && validContinuationModel(string(data)) {
		return string(data)
	}
	file, err := os.Open(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		return ""
	}
	defer cmdutil.CloseWithLog("coordinator transcript", file)
	reader, err := gzip.NewReader(file)
	if err != nil {
		return ""
	}
	defer cmdutil.CloseWithLog("coordinator transcript gzip", reader)
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
		if json.Unmarshal(scanner.Bytes(), &record) == nil && record.Type == "assistant" && validContinuationModel(record.Message.Model) {
			model = record.Message.Model
		}
	}
	return model
}

func (s *Server) continuationAvailability(db *store.Store, c *store.Conversation, view egg.SessionView) map[string]any {
	if s.BoundConversation != "" || s.identity.OrgWing || s.identity.SharedHost || c == nil || c.OwnerID != s.clientPrincipal() || c.ParentID != "" || c.ID != c.RootID || c.Agent != "claude" || c.SessionID != view.SessionID || view.Agent != "claude" || view.ProcessAlive || !eggclient.ValidProviderSessionID(view.ProviderSessionID) {
		return nil
	}
	if wc, err := config.LoadWingConfig(s.Cfg.Dir); err != nil || wc.Org != "" {
		return nil
	}
	session, err := s.resolveOwnedLifecycleSession(c.SessionID)
	if err != nil || session.ID != c.SessionID || session.Agent != "claude" || wingpolicy.CanonicalSessionPath(session.CWD) != wingpolicy.CanonicalSessionPath(c.CWD) {
		return nil
	}
	dir := filepath.Join(s.Cfg.Dir, "eggs", c.SessionID)
	if s.identity.UserID != "" && eggclient.ReadEggOwner(dir) != s.identity.UserID {
		return nil
	}
	if ok, _ := eggclient.SessionResumeStatus(dir, "claude", c.CWD); !ok {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "chat.meta"))
	if err != nil || egg.ParseChatMeta(string(data))["agent_session_id"] != view.ProviderSessionID {
		return nil
	}
	var pending int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM conversation_continuations WHERE conversation_id = ? AND launch_state = 'starting'`, c.ID).Scan(&pending); err != nil || pending != 0 {
		return nil
	}
	home := eggclient.EffectiveSessionHome(s.Cfg, s.identity)
	key := eggclient.ProviderResumeKey(home, "claude", view.ProviderSessionID)
	eggclient.BrowserProviderResumes.Mu.Lock()
	_, reserved := eggclient.BrowserProviderResumes.Active[key]
	eggclient.BrowserProviderResumes.Mu.Unlock()
	if reserved || eggclient.ActiveProviderResumeConflict(s.Cfg, key, "", func(dir string) bool { _, alive := eggclient.ReadAliveEggPID(dir); return alive }) {
		return nil
	}
	model := archivedCoordinatorModel(dir)
	if !validContinuationModel(model) {
		return nil
	}
	return map[string]any{"available": true, "source_session": c.SessionID, "conversation_id": c.ID, "provider_session_id": view.ProviderSessionID, "model": model}
}

func (s *Server) addSessionContinuation(result map[string]any, view egg.SessionView) {
	if _, err := os.Stat(s.Cfg.DBPath()); err != nil {
		return
	}
	db, err := s.openConversationStore()
	if err != nil {
		return
	}
	defer cmdutil.CloseWithLog("continuation availability store", db)
	c, err := db.ConversationForSession(view.SessionID)
	if err != nil {
		return
	}
	if available := s.continuationAvailability(db, c, view); available != nil {
		result["headless_continuation"] = available
	}
}

func headlessCoordinatorArgs(model, input string) []string {
	tools := []string{"agent_start", "session_wait", "session_read", "session_status", "conversation_read", "conversation_checkpoint", "wingthing_capabilities"}
	for i := range tools {
		tools[i] = "mcp__wingthing__" + tools[i]
	}
	policy, _ := json.Marshal(map[string]any{"availableModels": []string{model}, "enforceAvailableModels": true})
	return []string{"--model", model, "-p", input, "--output-format", "stream-json", "--verbose", "--restricted", "--setting-sources=", "--permission-mode", "dontAsk", "--permission-prompts", "none", "--max-turns", "40", "--allowedTools", strings.Join(tools, ","), "--settings", string(policy)}
}

func continuationLaunchResult(c *store.Conversation, turn *store.ConversationContinuation, reused bool) map[string]any {
	copy := *c
	copy.SessionID, copy.LaunchState, copy.LaunchError = turn.SessionID, turn.LaunchState, turn.LaunchError
	out := conversationLaunchResult(&copy, reused)
	out["source_session"], out["request_id"] = turn.SourceSession, turn.RequestID
	out["provider_session_id"] = turn.ProviderSessionID
	out["continuation_state"] = turn.LaunchState
	out["new_turn"] = map[string]any{"input_sha256": turn.InputSHA256}
	return out
}

func (s *Server) toolAgentContinue(arguments json.RawMessage) (map[string]any, error) {
	var args continuationArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if s.BoundConversation != "" || s.identity.OrgWing || s.identity.SharedHost {
		return nil, errors.New("only an unbound personal caller can continue a root conversation")
	}
	if wc, err := config.LoadWingConfig(s.Cfg.Dir); err != nil || wc.Org != "" {
		return nil, errors.New("headless continuation requires a personal wing")
	}
	if err := eggclient.ValidateSessionID(args.SourceSession); err != nil {
		return nil, err
	}
	if args.Role != "parent" || strings.TrimSpace(args.RequestID) != args.RequestID || args.RequestID == "" || len(args.RequestID) > 128 || strings.ContainsFunc(args.RequestID, unicode.IsControl) {
		return nil, errors.New("continuation requires conversation_role parent and a bounded immutable request_id")
	}
	if strings.TrimSpace(args.Input) == "" || len(args.Input) > 64<<10 || strings.HasPrefix(args.Input, "-") || strings.ContainsRune(args.Input, 0) {
		return nil, errors.New("input must be a message of at most 64 KiB that does not begin with '-'")
	}
	encoded, _ := json.Marshal(struct{ Source, Role, Input string }{args.SourceSession, args.Role, args.Input})
	digest := sha256.Sum256(encoded)
	specDigest := hex.EncodeToString(digest[:])
	db, err := s.openConversationStore()
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("continuation launch store", db)
	// Replay precedes eligibility: the source may be retired and the root may
	// already have later turns. Its immutable request still names one execution.
	turn, err := db.GetConversationContinuation(s.clientPrincipal(), args.RequestID)
	if err == nil {
		if turn.SpecDigest != specDigest {
			return nil, errors.New("launch request_id was already used with different arguments")
		}
		c, err := s.ownedConversation(db, turn.ConversationID)
		if err != nil {
			return nil, err
		}
		return continuationLaunchResult(c, turn, true), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	c, err := db.ConversationForSession(args.SourceSession)
	if err != nil {
		return nil, errors.New("continuation source not found or not owned by caller")
	}
	c, err = s.ownedConversation(db, c.ID)
	if err != nil {
		return nil, err
	}
	source, err := s.resolveOwnedLifecycleSession(args.SourceSession)
	if err != nil || source.ID != args.SourceSession {
		return nil, errors.New("continuation source not found or not owned by caller")
	}
	view, err := readExactExecutionLifecycleView(context.Background(), s.Cfg, args.SourceSession, 0, 1)
	if err != nil {
		return nil, err
	}
	available := s.continuationAvailability(db, c, view)
	if available == nil {
		// A concurrent identical request may have reserved or advanced the root
		// between our first retry lookup and this observation.
		if saved, err := db.GetConversationContinuation(s.clientPrincipal(), args.RequestID); err == nil {
			if saved.SpecDigest != specDigest {
				return nil, errors.New("launch request_id was already used with different arguments")
			}
			owned, err := s.ownedConversation(db, saved.ConversationID)
			if err != nil {
				return nil, err
			}
			return continuationLaunchResult(owned, saved, true), nil
		}
		return nil, errors.New("headless continuation is unavailable for this source execution")
	}
	inputHash := sha256.Sum256([]byte(args.Input))
	turn, created, err := db.ReserveConversationContinuation(store.ConversationContinuation{OwnerID: s.clientPrincipal(), RequestID: args.RequestID, ConversationID: c.ID, SourceSession: args.SourceSession, SessionID: cmdutil.NewRuntimeID(), ProviderSessionID: view.ProviderSessionID, InputSHA256: hex.EncodeToString(inputHash[:]), SpecDigest: specDigest})
	if err != nil {
		return nil, err
	}
	if !created {
		return continuationLaunchResult(c, turn, true), nil
	}
	invocation := *c
	invocation.SessionID = turn.SessionID
	launchErr := s.launchHeadlessContinuation(&invocation, turn, available["model"].(string), args.Input)
	state, detail := "started", ""
	if launchErr != nil {
		state, detail = "failed", launchErr.Error()
	}
	if err := db.SetConversationContinuationLaunch(turn, state, detail); err != nil {
		return nil, err
	}
	turn.LaunchState, turn.LaunchError = state, detail
	return continuationLaunchResult(c, turn, false), nil
}

func (s *Server) launchHeadlessContinuation(c *store.Conversation, turn *store.ConversationContinuation, model, input string) error {
	eggCfg, err := s.loadSessionLaunchConfig(c.CWD)
	if err != nil {
		return err
	}
	return s.admitSpawn(func() error {
		home := eggclient.EffectiveSessionHome(s.Cfg, s.identity)
		release, err := eggclient.BrowserProviderResumes.Reserve(s.Cfg, home, "claude", turn.ProviderSessionID, turn.SourceSession, turn.SessionID)
		if err != nil {
			return err
		}
		spawned := false
		defer func() { release(spawned) }()
		provider, err := egg.RestoreSessionHistory("claude", c.CWD, filepath.Join(s.Cfg.Dir, "eggs", turn.SourceSession), home)
		if err != nil {
			return err
		}
		if provider != turn.ProviderSessionID {
			return errors.New("restored provider does not match the continuation")
		}
		args, managed, err := s.prepareBoundParentLaunch(c, eggCfg, headlessCoordinatorArgs(model, input))
		if err != nil {
			return err
		}
		if err := saveCoordinatorModel(s.Cfg.Dir, c.SessionID, model); err != nil {
			return err
		}
		opts := eggclient.SpawnEggOpts{Label: c.Title, Kind: "agent", AgentArgs: args, Principal: s.clientPrincipal(), ResumeSessionID: provider, ResumeSourceSessionID: turn.SourceSession, ProviderReserved: true}
		if managed != nil {
			opts = managed.launchOpts(s.Cfg, opts)
		}
		if s.startContinuation != nil {
			err = s.startContinuation(c, eggCfg, opts)
		} else {
			var client *egg.Client
			client, err = s.startSession(c.SessionID, "claude", c.CWD, eggCfg, opts)
			if err == nil {
				cmdutil.CloseWithLog("continued agent egg client", client)
			}
		}
		spawned = err == nil
		return err
	})
}
