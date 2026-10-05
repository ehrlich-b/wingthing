package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

type conversationLink struct {
	ConversationID       string `json:"conversation_id,omitempty"`
	RootConversationID   string `json:"root_conversation_id,omitempty"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	ConversationRole     string `json:"conversation_role,omitempty"`
}

func linkForConversation(c *store.Conversation) conversationLink {
	if c == nil {
		return conversationLink{}
	}
	role := "child"
	if c.ParentID == "" {
		role = "parent"
	}
	return conversationLink{c.ID, c.RootID, c.ParentID, role}
}

func sessionConversationLink(cfg *config.Config, session string) conversationLink {
	// Do not create or migrate state just to enrich legacy session inventory.
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return conversationLink{}
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return conversationLink{}
	}
	defer closeWithLog("conversation inventory store", db)
	c, err := db.ConversationForSession(session)
	if err != nil {
		return conversationLink{}
	}
	return linkForConversation(c)
}

func (s *localMCPServer) reserveAgentConversation(agent, cwd, title, role, parent, requestID, sessionID string, spec any) (*store.Conversation, bool, error) {
	if s.boundConversation != "" {
		if parent != "" && parent != s.boundConversation {
			return nil, false, errors.New("parent_conversation_id must match this MCP connection's bound conversation")
		}
		parent = s.boundConversation
		if role == "parent" {
			return nil, false, errors.New("a conversation-bound MCP connection can only launch children")
		}
	}
	if parent == "" && role == "" && requestID == "" {
		return nil, true, nil
	}
	if role != "" && role != "parent" && role != "child" {
		return nil, false, errors.New("conversation_role must be parent or child")
	}
	if role == "parent" && parent != "" {
		return nil, false, errors.New("a parent conversation cannot have a parent")
	}
	if role == "child" && parent == "" {
		return nil, false, errors.New("child requires parent_conversation_id")
	}
	if agent != "claude" {
		return nil, false, errors.New("linked conversations currently support Claude; other agents remain available as terminals")
	}
	if strings.TrimSpace(requestID) != requestID || requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\x00\r\n") {
		return nil, false, errors.New("linked launch requires a bounded request_id for reconnect-safe retries")
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, false, err
	}
	defer closeWithLog("conversation launch store", db)
	if parent != "" {
		c, err := db.GetConversation(s.clientPrincipal(), parent)
		if err != nil || (s.enforcePathBounds && !isUnderPaths(canonicalSessionPath(c.CWD), s.allowedPaths)) {
			return nil, false, errors.New("parent conversation not found or not owned by caller")
		}
	}
	encoded, err := json.Marshal(struct {
		Spec   any    `json:"spec"`
		Parent string `json:"parent"`
		Role   string `json:"role"`
	}{spec, parent, role})
	if err != nil {
		return nil, false, err
	}
	digest := sha256.Sum256(encoded)
	wingID := ""
	if wc, err := config.LoadWingConfig(s.cfg.Dir); err == nil {
		wingID = wc.WingID
	}
	return db.ReserveConversation(store.Conversation{ID: newRuntimeID(), OwnerID: s.clientPrincipal(), ParentID: parent, Title: title, Agent: agent, CWD: cwd, WingID: wingID, SessionID: sessionID, LaunchKey: requestID, SpecDigest: hex.EncodeToString(digest[:])})
}

func (s *localMCPServer) markConversationLaunch(c *store.Conversation, spawnErr error) error {
	if c == nil {
		return nil
	}
	db, err := s.openMessageStore()
	if err != nil {
		return err
	}
	defer closeWithLog("conversation launch store", db)
	state, detail := "started", ""
	if spawnErr != nil {
		state, detail = "failed", spawnErr.Error()
	}
	return db.SetConversationLaunch(c.ID, state, detail)
}

func conversationLaunchResult(c *store.Conversation, reused bool) map[string]any {
	out := map[string]any{"session": c.SessionID, "conversation_id": c.ID, "root_conversation_id": c.RootID, "parent_conversation_id": c.ParentID, "agent": c.Agent, "label": c.Title, "cwd": c.CWD, "wing_id": c.WingID, "launch_state": c.LaunchState, "reused": reused}
	out["coordinator_context"] = contextForConversation(c)
	if c.LaunchError != "" {
		out["launch_error"] = c.LaunchError
	}
	out["ready"] = false
	out["readiness"] = "inspect session_status for native provider readiness"
	if c.LaunchState == "starting" {
		out["reconciliation_required"] = true
	}
	return out
}

func (s *localMCPServer) ownedConversation(db *store.Store, id string) (*store.Conversation, error) {
	if err := validateSessionID(id); err != nil {
		return nil, errors.New("invalid conversation_id")
	}
	c, err := db.GetConversation(s.clientPrincipal(), id)
	if err != nil {
		return nil, errors.New("conversation not found or not owned by caller")
	}
	if s.enforcePathBounds && !isUnderPaths(canonicalSessionPath(c.CWD), s.allowedPaths) {
		return nil, errors.New("conversation not found or not owned by caller")
	}
	if s.boundConversation != "" {
		bound, err := db.GetConversation(s.clientPrincipal(), s.boundConversation)
		if err != nil || bound.RootID != c.RootID {
			return nil, errors.New("conversation is outside this MCP connection's bound task tree")
		}
	}
	return c, nil
}

func (s *localMCPServer) toolConversationList(arguments json.RawMessage) (map[string]any, error) {
	var args struct{}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer closeWithLog("conversation list store", db)
	root := ""
	if s.boundConversation != "" {
		bound, err := s.ownedConversation(db, s.boundConversation)
		if err != nil {
			return nil, err
		}
		root = bound.RootID
	}
	nodes, err := db.ListConversations(s.clientPrincipal(), root)
	if err != nil {
		return nil, err
	}
	visible := make([]*store.Conversation, 0, len(nodes))
	for _, node := range nodes {
		if !s.enforcePathBounds || isUnderPaths(canonicalSessionPath(node.CWD), s.allowedPaths) {
			visible = append(visible, node)
		}
	}
	return map[string]any{"conversations": visible, "limit": 256}, nil
}

func (s *localMCPServer) toolConversationRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		ConversationID string `json:"conversation_id"`
		After          int64  `json:"after_cursor"`
		Limit          int    `json:"limit"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.After < 0 {
		return nil, errors.New("after_cursor must not be negative")
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Limit > 100 {
		return nil, errors.New("limit must be between 1 and 100")
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer closeWithLog("conversation read store", db)
	c, err := s.ownedConversation(db, args.ConversationID)
	if err != nil {
		return nil, err
	}
	nodes, err := db.ListConversations(s.clientPrincipal(), c.RootID)
	if err != nil {
		return nil, err
	}
	tasks := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		if s.enforcePathBounds && !isUnderPaths(canonicalSessionPath(node.CWD), s.allowedPaths) {
			continue
		}
		task := map[string]any{"conversation": node, "coordinator_context": contextForConversation(node)}
		executions, executionErr := db.ConversationExecutions(node.ID)
		if executionErr != nil {
			return nil, executionErr
		}
		var view egg.SessionView
		var viewErr error
		for _, execution := range executions {
			copy := *node
			copy.SessionID = execution
			executionView, err := syncConversationStates(ctx, s.cfg, db, &copy)
			if execution == node.SessionID {
				view, viewErr = executionView, err
			}
			if err != nil && execution != node.SessionID {
				task["history_unavailable"] = true
			}
		}
		if viewErr == nil {
			view.Events = []egg.SessionEvent{}
			task["lifecycle"] = view
		} else {
			task["lifecycle_error"] = viewErr.Error()
		}
		tasks = append(tasks, task)
	}
	events, err := db.ConversationEvents(s.clientPrincipal(), c.RootID, args.After, args.Limit+1)
	if err != nil {
		return nil, err
	}
	hasMore := len(events) > args.Limit
	if hasMore {
		events = events[:args.Limit]
	}
	next := args.After
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	return map[string]any{"conversation": c, "coordinator_context": contextForConversation(c), "tasks": tasks, "events": events, "next_cursor": next, "has_more": hasMore, "delivery": "replayable; checkpoint explicitly to acknowledge"}, nil
}

func syncConversationStates(ctx context.Context, cfg *config.Config, db *store.Store, c *store.Conversation) (egg.SessionView, error) {
	after, err := db.ConversationImportCursor(c.SessionID)
	if err != nil {
		return egg.SessionView{}, err
	}
	var view egg.SessionView
	// Bound one reconciliation to 1,000 journal records. The persisted import
	// cursor lets the next read retain every intermediate attention/completion.
	for page := 0; page < 5; page++ {
		view, err = readExactExecutionLifecycleView(ctx, cfg, c.SessionID, after, 200)
		if err != nil {
			return view, err
		}
		events := make([]store.ConversationEvent, 0)
		for _, event := range view.Events {
			if event.State == "" {
				continue
			}
			if event.Source == "claude_transcript" && view.StateSource != "claude_transcript" {
				continue
			}
			events = append(events, store.ConversationEvent{SourceCursor: event.Sequence, State: event.State, StateSource: event.Source, Type: event.Type})
		}
		if err := db.ImportConversationStates(c, events, view.Cursor); err != nil {
			return view, err
		}
		after = view.Cursor
		if !view.HasMore {
			if err := db.RecordConversationState(c, view.StateCursor, view.State, view.StateSource); err != nil {
				return view, err
			}
			break
		}
	}
	return view, nil
}

// readExactExecutionLifecycleView reads a durably recorded execution ID. Unlike
// human selectors it never falls back to a name or ID prefix, so a retired
// execution cannot alias a surviving sibling whose ID begins with it.
func readExactExecutionLifecycleView(ctx context.Context, cfg *config.Config, id string, after int64, limit int) (egg.SessionView, error) {
	if err := ctx.Err(); err != nil {
		return egg.SessionView{}, err
	}
	if id == "" {
		return egg.SessionView{}, errors.New("session is required")
	}
	if err := validateSessionName(id); err != nil {
		return egg.SessionView{}, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", id)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
		return egg.SessionView{}, errors.New("session not found")
	}
	if err != nil {
		return egg.SessionView{}, err
	}
	meta := readEggMetaValues(dir)
	session := localSession{ID: id, Name: readSessionName(dir), Principal: readSessionPrincipal(dir), Agent: meta["agent"], Kind: meta["kind"], CWD: meta["cwd"]}
	return lifecycleViewForSession(cfg, session, after, limit)
}

func (s *localMCPServer) toolConversationCheckpoint(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		ConversationID string `json:"conversation_id"`
		Expected       int64  `json:"expected_revision"`
		After          int64  `json:"after_cursor"`
		Checkpoint     string `json:"checkpoint"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer closeWithLog("conversation checkpoint store", db)
	if _, err := s.ownedConversation(db, args.ConversationID); err != nil {
		return nil, err
	}
	if err := db.CheckpointConversation(s.clientPrincipal(), args.ConversationID, args.Expected, args.After, args.Checkpoint); err != nil {
		return nil, err
	}
	c, err := db.GetConversation(s.clientPrincipal(), args.ConversationID)
	return map[string]any{"conversation": c, "coordinator_context": contextForConversation(c)}, err
}

func inheritConversationExecution(cfg *config.Config, source, target string) error {
	if source == "" {
		return nil
	}
	if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer closeWithLog("conversation resume store", db)
	return db.ResumeConversationExecution(source, target)
}

// Bootstrap is deliberately output-only: it does not edit clients.yaml, add
// grants, copy provider credentials, or select a different Wingthing state dir.
func conversationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "conversation", Short: "Inspect persistent parent and child conversations"}
	var client string
	cmd.PersistentFlags().StringVar(&client, "client", "default", "existing local MCP owner/client")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server := &localMCPServer{cfg: cfg, principal: client, logs: os.Stderr}
		result, err := server.toolConversationList(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	var after int64
	read := &cobra.Command{Use: "read <conversation>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server := &localMCPServer{cfg: cfg, principal: client, logs: os.Stderr}
		input, _ := json.Marshal(map[string]any{"conversation_id": args[0], "after_cursor": after})
		result, err := server.toolConversationRead(cmd.Context(), input)
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	read.Flags().Int64Var(&after, "after-cursor", 0, "return durable state deliveries after this cursor")
	bootstrap := &cobra.Command{Use: "bootstrap <conversation>", Short: "Print a reproducible Claude MCP configuration without changing permissions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		db, err := store.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer closeWithLog("conversation bootstrap store", db)
		c, err := db.GetConversation(client, args[0])
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("conversation not found or not owned by caller")
		}
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return writeSessionJSON(map[string]any{"mcpServers": map[string]any{"wingthing": map[string]any{"command": executable, "args": []string{"mcp", "stdio", "--client", client, "--conversation", c.ID}, "env": map[string]string{"WINGTHING_DIR": filepath.Clean(cfg.Dir)}}}})
	}}
	list.Flags().Bool("json", true, "print structured inventory")
	read.Flags().Bool("json", true, "print structured conversation tree")
	bootstrap.Flags().Bool("json", true, "print Claude MCP configuration")
	cmd.AddCommand(list, read, bootstrap, conversationWakeCmd(&client))
	cmd.AddCommand(conversationBrokerCmds()...)
	return cmd
}

func validateBoundConversation(s *localMCPServer) error {
	if s.boundConversation == "" {
		return nil
	}
	db, err := s.openMessageStore()
	if err != nil {
		return err
	}
	defer closeWithLog("conversation binding store", db)
	c, lookupErr := s.ownedConversation(db, s.boundConversation)
	err = lookupErr
	if err != nil {
		return fmt.Errorf("bind MCP conversation: %w", err)
	}
	// A browser-created personal root carries the existing owner identity.
	// Propagate it only when its persisted principal matches the same user's
	// existing owner hash. This adds no client grants or credential access.
	dir := filepath.Join(s.cfg.Dir, "eggs", c.SessionID)
	owner := readEggOwner(dir)
	if owner != "" && c.OwnerID == roostSessionPrincipal(owner) && readSessionPrincipal(dir) == c.OwnerID {
		wc, err := config.LoadWingConfig(s.cfg.Dir)
		if err != nil {
			return err
		}
		if wc.Org != "" {
			return errors.New("personal conversation MCP binding is unavailable on an organization wing")
		}
		s.identity.UserID = owner
		s.identity.Email = readEggOwnerEmail(dir)
	}
	return nil
}

func (s *localMCPServer) prepareBoundParentMCP(c *store.Conversation, cfg *egg.EggConfig, args []string) ([]string, error) {
	args, _, err := s.prepareBoundParentLaunch(c, cfg, args)
	return args, err
}

// prepareBoundParentLaunch also returns the host mailbox registration when the
// parent is broker-managed; its launchOpts must then be applied to the spawn.
func (s *localMCPServer) prepareBoundParentLaunch(c *store.Conversation, cfg *egg.EggConfig, args []string) ([]string, *conversationBrokerRegistration, error) {
	if c == nil || c.ParentID != "" {
		return args, nil, nil
	}
	for _, arg := range args {
		if arg == "--mcp-config" || strings.HasPrefix(arg, "--mcp-config=") || arg == "--strict-mcp-config" {
			return nil, nil, errors.New("linked parent supplies its own wingthing MCP configuration; caller --mcp-config/--strict-mcp-config conflicts with that binding")
		}
	}
	if !s.unsandboxed {
		rendered, err := cfg.YAML()
		if err != nil {
			return nil, nil, err
		}
		home := effectiveSessionHome(s.cfg, s.identity)
		policy, err := loadSessionFilePolicy(ws.SessionInfo{CWD: c.CWD, EggConfig: rendered}, home)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := policy.writableRoot(canonicalPolicyPath(s.cfg.Dir)); !ok {
			refusal := fmt.Errorf("parent MCP cannot write isolated Wingthing state %q under the existing sandbox policy; use an already writable workspace containing that state directory (no mounts or grants were changed)", s.cfg.Dir)
			// Activation contract: the direct in-sandbox server is unchanged
			// wherever it was accepted, and stable keeps this refusal. Only in
			// preview does this layout, where the provider cannot write the
			// state, use the host mailbox.
			if config.Channel() != "preview" {
				return nil, nil, refusal
			}
			if _, ok := policy.writableRoot(canonicalPolicyPath(c.CWD)); !ok {
				return nil, nil, errors.New("parent MCP configuration requires an already writable workspace")
			}
			args, reg, brokerErr := s.prepareBrokerParentMCP(c, cfg, args)
			if brokerErr != nil {
				return nil, nil, fmt.Errorf("%w; host mailbox unavailable: %w", refusal, brokerErr)
			}
			return args, reg, nil
		}
		if _, ok := policy.writableRoot(canonicalPolicyPath(c.CWD)); !ok {
			return nil, nil, errors.New("parent MCP configuration requires an already writable workspace")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	configuration := map[string]any{"mcpServers": map[string]any{"wingthing": map[string]any{"command": executable, "args": []string{"mcp", "stdio", "--client", s.clientPrincipal(), "--conversation", c.ID}, "env": map[string]string{"WINGTHING_DIR": filepath.Clean(s.cfg.Dir)}}}}
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(c.CWD)
	if err != nil {
		return nil, nil, err
	}
	defer closeWithLog("parent MCP workspace", root)
	relative := filepath.Join(".wingthing-conversations", c.ID, "mcp.json")
	if err := root.MkdirAll(filepath.Dir(relative), 0700); err != nil {
		return nil, nil, err
	}
	file, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := root.ReadFile(relative)
		if readErr != nil || string(existing) != string(data) {
			return nil, nil, errors.New("existing parent MCP configuration differs from the reserved binding")
		}
		return parentMCPArguments(args, c, relative), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if err := file.Close(); err != nil {
		return nil, nil, err
	}
	return parentMCPArguments(args, c, relative), nil, nil
}

func parentMCPArguments(args []string, c *store.Conversation, relative string) []string {
	return append(args, "--mcp-config", filepath.Join(c.CWD, relative), "--append-system-prompt", publicCoordinatorPrompt(c))
}

// Called only after prepareBrowserResume has verified the source owner,
// provider identity and current workspace policy.
func prepareConversationResumeMCP(cfg *config.Config, wc *config.WingConfig, start ws.PTYStart, eggCfg *egg.EggConfig, sharedHost bool) ([]string, string, error) {
	if start.ResumeSessionID == "" {
		return nil, "", nil
	}
	principal := readSessionPrincipal(filepath.Join(cfg.Dir, "eggs", start.ResumeSessionID))
	if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
		return nil, principal, nil
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, "", err
	}
	defer closeWithLog("conversation resume binding store", db)
	c, err := db.ConversationForSession(start.ResumeSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, principal, nil
	}
	if err != nil {
		return nil, "", err
	}
	if wc.Org != "" || sharedHost || c.OwnerID != roostSessionPrincipal(start.UserID) || principal != c.OwnerID {
		return nil, "", errors.New("conversation resume binding does not match this personal owner")
	}
	// The same authenticated browser owner, grants, bounds and path policy as
	// browserSessionControl. A host mailbox registration captures this finite
	// ceiling; the direct transport does not consult it.
	home, _ := os.UserHomeDir()
	paths := canonicalPaths(pathsForRequest(wc.Paths, start.Email, "owner", home))
	server := &localMCPServer{cfg: cfg, principal: principal, actor: "browser", surface: control.SurfaceHTTPMCP,
		grants: grantSet(defaultDirectMCPGrants), maxSessions: defaultDirectMCPMaxSessions, maxSpawnsPerHour: defaultDirectMCPMaxSpawnsPerHour,
		allowedPaths: paths, enforcePathBounds: len(paths) > 0, identity: EggIdentity{UserID: start.UserID, Email: start.Email},
		// The browser PTY spawn cannot yet apply the broker launch contract
		// (protected targets, no browser bridge), so resume keeps the refusal.
		hostMailboxUnavailable: "a resumed browser parent cannot apply the host mailbox launch contract"}
	// Logical linkage is committed after spawn. The public role facts must already
	// identify this invocation, rather than the source execution being resumed.
	invocation := *c
	invocation.SessionID = start.SessionID
	args, err := server.prepareBoundParentMCP(&invocation, eggCfg, nil)
	return args, principal, err
}

func (s *localMCPServer) toolConversationBootstrap(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer closeWithLog("conversation bootstrap store", db)
	c, err := s.ownedConversation(db, args.ConversationID)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	configuration := map[string]any{"mcpServers": map[string]any{"wingthing": map[string]any{"command": executable, "args": []string{"mcp", "stdio", "--client", s.clientPrincipal(), "--conversation", c.ID}, "env": map[string]string{"WINGTHING_DIR": filepath.Clean(s.cfg.Dir)}}}}
	return map[string]any{"conversation_id": c.ID, "coordinator_context": contextForConversation(c), "coordinator_prompt": publicCoordinatorPrompt(c), "mcp_client": s.clientPrincipal(), "configuration": configuration, "automatic": false, "requirements": []string{"existing clients.yaml must already authorize this client if configured", "provider sandbox must already permit the executable, state directory and transport", "configure the provider with this file; no credential or permission migration is performed"}}, nil
}
