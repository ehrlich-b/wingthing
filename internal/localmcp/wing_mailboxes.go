package localmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingconnect"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"golang.org/x/sys/unix"
)

// Legacy registrations retain their original Tools and never acquire these
// run or remote grants merely because the wing has been upgraded.
var scopedParentTools = append(append([]string{}, conversationBrokerTools...),
	"wing_list", "agent_run", "agent_status", "agent_wait", "agent_wait_any", "agent_result", "agent_events", "agent_stop", "agent_steer")

func sealMailboxPolicy(cfg *config.Config, policy *egg.EggConfig) error {
	state := wingpolicy.CanonicalPolicyPath(cfg.Dir)
	socket, err := controlsocket.Path(cfg.Dir)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	policy.FS = append(append([]string{}, policy.FS...), "deny:"+state, "deny:"+wingpolicy.CanonicalPolicyPath(socket), "deny:~/.ssh")
	if !wingpolicy.SessionPolicyContains(state, wingpolicy.CanonicalPolicyPath(socket)) {
		policy.FS = append(policy.FS, "deny:"+wingpolicy.CanonicalPolicyPath(filepath.Dir(socket)))
	}
	// Pin the HOME ancestry as literal directory entries. Descendant workspace
	// writes remain available, while an agent cannot rename an ancestor to move
	// protected state out of its sandbox boundary (including isolated profiles).
	for path := wingpolicy.CanonicalPolicyPath(home); ; path = filepath.Dir(path) {
		policy.FS = append(policy.FS, "deny-write:"+path)
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}

type wingMailboxes struct {
	ctx            context.Context
	cancel         context.CancelFunc
	version, owner string
	sessions       *wingsession.Service
	admission      *AdmissionState
	remotes        *rememberedWings
	mu             sync.Mutex
	active         map[string]bool
	wg             sync.WaitGroup
}

func newWingMailboxes(ctx context.Context, version, owner string, sessions *wingsession.Service, admission *AdmissionState, remotes *rememberedWings) *wingMailboxes {
	ctx, cancel := context.WithCancel(ctx)
	return &wingMailboxes{ctx: ctx, cancel: cancel, version: version, owner: owner, sessions: sessions, admission: admission, remotes: remotes, active: map[string]bool{}}
}
func (m *wingMailboxes) Close() error { m.cancel(); m.wg.Wait(); return nil }
func (m *wingMailboxes) restore() {
	entries, _ := os.ReadDir(filepath.Join(m.sessions.Config.Dir, conversationBrokersDir))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		reg, err := loadConversationBrokerRegistration(m.sessions.Config, entry.Name())
		if _, alive := eggclient.ReadAliveEggPID(filepath.Join(m.sessions.Config.Dir, "eggs", reg.SessionID)); err == nil && alive {
			_ = m.start(reg)
		}
	}
}
func (m *wingMailboxes) configure(s *Server) {
	s.startMailbox = m.start
	s.captureWings = m.capture
}
func (m *wingMailboxes) start(reg conversationBrokerRegistration) error {
	if reg.UserID != "" && reg.UserID != m.owner {
		return errors.New("mailbox belongs to another wing owner")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return m.ctx.Err()
	}
	if m.active[reg.SessionID] {
		return nil
	}
	m.active[reg.SessionID] = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); delete(m.active, reg.SessionID); m.mu.Unlock() }()
		err := runConversationBroker(m.version, m.ctx, m.sessions.Config, reg.SessionID, os.Stderr, m.admission, func(ctx context.Context, call conversationMailboxCall) (localMCPResponse, error) {
			return m.call(ctx, reg, call)
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrConversationBrokerRunning) {
			_, _ = fmt.Fprintf(os.Stderr, "wing mailbox %s: %v\n", reg.SessionID, err)
		}
	}()
	return nil
}

func (m *wingMailboxes) capture(s *Server) (map[string][]string, error) {
	local := m.remotes.wingID
	// Local children inherit the parent's policy and workspace, independently of
	// wider owner paths. Remote workspaces are separate host-local paths.
	wings := map[string][]string{local: {wingpolicy.CanonicalPolicyPath(s.sessionLaunch.CWD)}}
	p, err := m.remotes.pool(controlsocket.Hello{Client: s.conversationMCPClient()})
	if err != nil {
		return nil, err
	}
	entries, registryError := p.Entries()
	if registryError != "" {
		return nil, errors.New(registryError)
	}
	for _, entry := range entries {
		id, _ := entry["wing_id"].(string)
		if id == "" {
			continue
		}
		if s.restrictWings {
			if paths, granted := s.wingGrants[id]; !granted || len(paths) == 0 {
				continue
			}
		}
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		data, denied, err := p.Call(ctx, id, "wingthing_capabilities", json.RawMessage(`{}`))
		cancel()
		if err != nil || denied {
			continue
		} // offline wings cannot add unknown paths
		wire, _ := json.Marshal(data["paths"])
		var paths []string
		if json.Unmarshal(wire, &paths) == nil && len(paths) > 0 {
			if s.restrictWings {
				paths, _ = intersectBrokerPaths(s.wingGrants[id], true, paths)
			}
			if len(paths) > 0 {
				wings[id] = paths
			}
		}
	}
	return wings, nil
}

func (m *wingMailboxes) server(reg conversationBrokerRegistration) (*Server, error) {
	s, wc, err := reg.server(m.version, m.sessions.Config, m.admission)
	if err != nil {
		return nil, err
	}
	s.Sessions = m.sessions
	s.identity.UserID = m.owner
	s.mailboxLocked = wc.Locked
	return s, nil
}
func (m *wingMailboxes) call(ctx context.Context, reg conversationBrokerRegistration, call conversationMailboxCall) (localMCPResponse, error) {
	s, err := m.server(reg)
	if err != nil {
		return localMCPResponse{}, err
	}
	req := localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: call.Method, Params: call.Params}
	if !reg.Scoped || call.Method != "tools/call" {
		response, _ := s.handle(ctx, req)
		if reg.Scoped && call.Method == "tools/list" {
			tools := []control.Tool{}
			for _, tool := range control.Tools(control.SurfaceDirectMCP) {
				if s.toolAllowed(tool.Name) {
					tools = append(tools, tool)
				}
			}
			response.Result = map[string]any{"tools": tools}
		}
		return response, nil
	}
	var params localMCPToolCallParams
	if err := decodeStrict(call.Params, &params); err != nil {
		return localMCPResponse{JSONRPC: "2.0", Error: &localMCPError{Code: -32602, Message: err.Error()}}, nil
	}
	if params.Task != nil {
		return localMCPResponse{JSONRPC: "2.0", Error: &localMCPError{Code: -32602, Message: "task augmentation is unavailable on this scoped mailbox"}}, nil
	}
	response := m.dispatch(ctx, s, control.DirectRequest{Version: control.ContractVersion, ID: "1", Tool: params.Name, Arguments: params.Arguments})
	if err := response.Err(); err != nil {
		return localMCPResponse{}, err
	}
	return localMCPResponse{JSONRPC: "2.0", Result: localMCPToolResult(response.Result, response.IsError)}, nil
}

type mailboxChild struct {
	WingID       string `json:"wing_id"`
	RunID        string `json:"run_id,omitempty"`
	SessionID    string `json:"session_id"`
	CWD          string `json:"cwd"`
	AdmissionKey string `json:"admission_key,omitempty"`
}

type wingCallScope struct {
	Paths            []string `json:"paths"`
	Tools            []string `json:"tools"`
	MaxSessions      int      `json:"max_sessions"`
	MaxSpawnsPerHour int      `json:"max_spawns_per_hour"`
}

func remoteMailboxHello(s *Server, paths []string) controlsocket.Hello {
	tools := []string{}
	for _, name := range s.broker.Tools {
		if s.toolAllowed(name) {
			tools = append(tools, name)
		}
	}
	data, _ := json.Marshal(wingCallScope{Paths: paths, Tools: tools, MaxSessions: s.MaxSessions, MaxSpawnsPerHour: s.MaxSpawnsPerHour})
	return controlsocket.Hello{Client: s.broker.LauncherActor, Scope: string(data)}
}

func childKey(wing, id string) string { return wing + "/" + id }

// The ledger is outside provider-writable state. The lock covers only a bounded
// read/update, never the lifetime of a wait or remote operation.
func mailboxChildren(cfg *config.Config, reg *conversationBrokerRegistration, add *mailboxChild) (map[string]mailboxChild, error) {
	dir := conversationBrokerDir(cfg, reg.SessionID)
	lock, err := os.OpenFile(filepath.Join(dir, "children.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return nil, err
	}
	children := map[string]mailboxChild{}
	path := filepath.Join(dir, "children.json")
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > 1<<20 {
			return nil, errors.New("mailbox child ledger exceeds limit")
		}
		if err := json.Unmarshal(data, &children); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if add != nil {
		if add.RunID != "" {
			children[childKey(add.WingID, add.RunID)] = *add
		}
		children[childKey(add.WingID, add.SessionID)] = *add
		if len(children) > 1024 {
			return nil, errors.New("mailbox child ledger is full")
		}
		data, err := json.Marshal(children)
		if err != nil {
			return nil, err
		}
		if err := writePrivateDurable(path, data, false); err != nil {
			return nil, err
		}
	}
	return children, nil
}

func (m *wingMailboxes) scopePaths(s *Server, id string) ([]string, error) {
	paths, granted := s.broker.Wings[id]
	if !granted || len(paths) == 0 {
		return nil, errors.New("wing is outside this parent's captured grants")
	}
	if id == m.remotes.wingID {
		paths, _ = intersectBrokerPaths(paths, true, s.allowedPaths)
	} else {
		clients, err := LoadLocalMCPClientsConfig(s.Cfg)
		if err != nil {
			return nil, err
		}
		if entry, configured := clients.Clients[s.broker.LauncherActor]; configured {
			current, granted := entry.Wings[id]
			if !granted {
				return nil, errors.New("remote wing grant was revoked")
			}
			paths, _ = intersectBrokerPaths(paths, true, current)
			if len(current) == 0 {
				paths = nil
			}
		}
		remotes, err := config.LoadRemotes(s.Cfg.Dir)
		if err != nil {
			return nil, err
		}
		count := 0
		for _, remote := range remotes {
			if remote.WingID == id {
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("remote wing is no longer uniquely granted in the remembered directory")
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("workspace grant was revoked")
	}
	return paths, nil
}

func (m *wingMailboxes) dispatch(ctx context.Context, s *Server, request control.DirectRequest) control.DirectResponse {
	fail := func(err error) control.DirectResponse {
		return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: control.ErrorResult(err), IsError: true}
	}
	if !s.toolAllowed(request.Tool) {
		return fail(errors.New("tool is outside this parent's current grants"))
	}
	if s.mailboxLocked && conversationBrokerMutations[request.Tool] {
		return fail(errors.New("the wing is locked; mailbox mutations are paused"))
	}
	if request.Tool == "wing_list" {
		if err := requireEmptyObject(request.Arguments); err != nil {
			return fail(err)
		}
		p, err := m.remotes.pool(controlsocket.Hello{Client: s.broker.LauncherActor})
		if err != nil {
			return fail(err)
		}
		entries, _ := p.Entries()
		entries = append([]map[string]any{{"wing_id": m.remotes.wingID, "name": "local", "online": true, "mcp_transport": "local-socket"}}, entries...)
		allowed := []map[string]any{}
		for _, entry := range entries {
			id, _ := entry["wing_id"].(string)
			if paths, err := m.scopePaths(s, id); err == nil {
				entry["paths"] = paths
				allowed = append(allowed, entry)
			}
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: map[string]any{"wings": allowed, "count": len(allowed), "control_scope": "parent"}}
	}
	var fields map[string]json.RawMessage
	if len(request.Arguments) == 0 {
		request.Arguments = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(request.Arguments, &fields); err != nil || fields == nil {
		return fail(errors.New("arguments must be an object"))
	}
	id := m.remotes.wingID
	if raw := fields["wing_id"]; raw != nil {
		if err := json.Unmarshal(raw, &id); err != nil || id == "" {
			return fail(errors.New("wing_id must be a string"))
		}
		delete(fields, "wing_id")
	}
	paths, err := m.scopePaths(s, id)
	if err != nil {
		return fail(err)
	}
	children, err := mailboxChildren(s.Cfg, s.broker, nil)
	if err != nil {
		return fail(err)
	}
	if strings.HasPrefix(request.Tool, "agent_") && request.Tool != "agent_run" && request.Tool != "agent_start" {
		var ids []string
		if request.Tool == "agent_wait_any" {
			if err := json.Unmarshal(fields["run_ids"], &ids); err != nil {
				return fail(err)
			}
		} else {
			var run string
			if err := json.Unmarshal(fields["run_id"], &run); err != nil {
				return fail(err)
			}
			ids = []string{run}
		}
		for _, run := range ids {
			child, ok := children[childKey(id, run)]
			if !ok || child.RunID != run || !wingpolicy.IsUnderPaths(child.CWD, paths) {
				return fail(errors.New("run is outside this parent's recorded children"))
			}
		}
	}
	if id != m.remotes.wingID && boundSessionTargetTools[request.Tool] {
		var session string
		_ = json.Unmarshal(fields["session"], &session)
		child, ok := children[childKey(id, session)]
		if !ok || child.SessionID != session || !wingpolicy.IsUnderPaths(child.CWD, paths) {
			return fail(errors.New("session is outside this parent's recorded children"))
		}
	}
	if request.Tool == "agent_run" || request.Tool == "agent_start" {
		if request.Tool == "agent_start" && id != m.remotes.wingID {
			return fail(errors.New("remote children must use agent_run"))
		}
		var cwd string
		_ = json.Unmarshal(fields["cwd"], &cwd)
		if cwd == "" && id == m.remotes.wingID {
			cwd = s.broker.Workspace
			fields["cwd"], _ = json.Marshal(cwd)
		}
		resolved := filepath.Clean(cwd)
		if id == m.remotes.wingID {
			resolved = wingpolicy.CanonicalPolicyPath(cwd)
		}
		// Remote paths are host-local. Its wing resolves symlinks and checks
		// canonical cwd against the transmitted ceiling before admission.
		if !filepath.IsAbs(cwd) || !wingpolicy.IsUnderPaths(resolved, paths) {
			return fail(errors.New("cwd is outside this parent's captured workspace grants"))
		}
	}
	callerKey, admissionKey := "", ""
	if request.Tool == "agent_run" || request.Tool == "agent_steer" || request.Tool == "agent_start" {
		keyField := "idempotency_key"
		if request.Tool == "agent_start" {
			keyField = "request_id"
		}
		var key string
		if err := json.Unmarshal(fields[keyField], &key); err != nil || key == "" {
			return fail(errors.New(keyField + " is required for scoped admission"))
		}
		if len(key) > 200 || request.Tool == "agent_start" && (len(key) > 128 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\x00\r\n")) {
			return fail(errors.New(keyField + " exceeds the admission key bounds"))
		}
		callerKey = key
		digest := sha256.Sum256([]byte(s.broker.SessionID + "\x00" + id + "\x00" + key))
		admissionKey = "parent-" + hex.EncodeToString(digest[:])
		fields[keyField], _ = json.Marshal(admissionKey)
		if len(children) > 1022 {
			known := false
			for _, child := range children {
				if child.WingID == id && child.AdmissionKey == admissionKey {
					known = true
					break
				}
			}
			if !known {
				return fail(errors.New("mailbox child ledger is full"))
			}
		}
	}
	args, _ := json.Marshal(fields)
	request.Arguments = args
	var response control.DirectResponse
	if id == m.remotes.wingID {
		response = s.handleDirectRequest(ctx, request)
		if response.Result != nil {
			response.Result = control.QualifyResult(id, response.Result)
		}
	} else {
		if strings.HasPrefix(request.Tool, "conversation_") {
			return fail(errors.New("conversation tools are local to this parent's wing"))
		}
		p, err := m.remotes.pool(remoteMailboxHello(s, paths))
		if err != nil {
			return fail(err)
		}
		data, denied, err := p.Call(ctx, id, request.Tool, args)
		if err != nil {
			response := fail(err)
			var unknown *wingconnect.UnknownOutcome
			if errors.As(err, &unknown) {
				reported := *unknown
				reported.Key = callerKey
				response = fail(&reported)
				response.Result["outcome"] = "unknown"
				response.Result["wing_id"] = id
				if callerKey != "" {
					response.Result["idempotency_key"] = callerKey
				}
			}
			return response
		}
		response = control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: data, IsError: denied}
	}
	// The receiver sees a parent-specific namespace. The parent must retain its
	// original key so retrying an echoed receipt reconciles the same admission.
	if callerKey != "" && response.Result != nil {
		keyField := "idempotency_key"
		if request.Tool == "agent_start" {
			keyField = "request_id"
		}
		response.Result[keyField] = callerKey
	}
	if response.Result != nil && request.Tool == "wingthing_capabilities" {
		response.Result["paths"] = paths
	}
	if !response.IsError && response.Error == "" && (request.Tool == "agent_run" || request.Tool == "agent_steer" || request.Tool == "agent_start") {
		child := mailboxChild{WingID: id, AdmissionKey: admissionKey}
		child.RunID, _ = response.Result["run_id"].(string)
		child.SessionID, _ = response.Result["session_id"].(string)
		if child.SessionID == "" {
			child.SessionID, _ = response.Result["session"].(string)
		}
		child.CWD, _ = response.Result["cwd"].(string)
		if child.SessionID == "" || !wingpolicy.IsUnderPaths(child.CWD, paths) {
			return fail(errors.New("wing returned a child outside the admitted scope"))
		}
		if _, err := mailboxChildren(s.Cfg, s.broker, &child); err != nil {
			response := fail(fmt.Errorf("child was admitted but its receipt could not be recorded; retry the original key: %w", err))
			response.Result["outcome"], response.Result["wing_id"], response.Result["idempotency_key"] = "unknown", id, callerKey
			return response
		}
	}
	return response
}
