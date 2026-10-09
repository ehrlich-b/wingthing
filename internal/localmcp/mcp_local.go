package localmcp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/promptmgr"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/google/uuid"
)

const localMCPProtocolVersion = "2025-11-25"

const maxConcurrentLocalMCPCalls = 64

const maxConcurrentAgentWaitAnyCalls = 4

type Server struct {
	Sessions                           *wingsession.Service
	sessionLaunch                      *wingsession.Launch
	sessionRole                        string
	sessionBrowser                     bool
	sessionPublicKey, sessionAuthToken string
	Version                            string
	Cfg                                *config.Config
	In                                 io.Reader
	Out                                io.Writer
	Logs                               io.Writer
	Principal                          string
	Unsandboxed                        bool
	Grants                             map[string]bool
	MaxSessions                        int
	MaxSpawnsPerHour                   int
	spawnMu                            sync.Mutex
	admitMu                            sync.Mutex // held across bounds check + spawn + record
	spawnTimes                         []time.Time
	admission                          *AdmissionState // shared by remote connections on one wing
	identity                           eggclient.EggIdentity
	Actor                              string
	MCPClient                          string // local clients.yaml identity, independent of the audit actor
	BoundConversation                  string
	Surface                            control.Surface
	allowedPaths                       []string
	enforcePathBounds                  bool
	runAgentTask                       func(context.Context, *config.Config, *store.Store, *store.Task, taskrun.TaskRunOptions) error
	startContinuation                  func(*store.Conversation, *egg.EggConfig, eggclient.SpawnEggOpts) error
	spawnFork                          func(*eggclient.SessionForkPlan) error
	launchConfig                       func(string) (*egg.EggConfig, error)
	forkTrace                          bool
	forkIdleTimeout                    time.Duration
	forkTools                          []*config.ToolConfig
	// tools, when set, further limits callable tools by name; grants are
	// per category and cannot express the host mailbox's fixed subset.
	tools map[string]bool
	// broker is the protected registration of a host mailbox dispatcher.
	broker *conversationBrokerRegistration
	// hostMailboxUnavailable, when set, refuses host mailbox selection for a
	// launcher whose spawn cannot apply the broker launch contract.
	hostMailboxUnavailable string
}

// mcpAdmissionState keeps process-local spawn admission shared across reconnecting
// remote MCP clients. The filesystem-backed max-sessions check is also serialized by
// this lock, so two data channels cannot race through the final available slot.
// This remains a guardrail rather than a durable quota across wing restarts.
type AdmissionState struct {
	Sessions   *wingsession.Service
	mu         sync.Mutex
	spawnTimes map[string][]time.Time
}

func NewMCPAdmissionState() *AdmissionState {
	return &AdmissionState{spawnTimes: map[string][]time.Time{}}
}

type activeMCPAgentRun struct {
	principal string
	cancel    context.CancelFunc
	done      <-chan struct{}
}

var activeMCPAgentRuns sync.Map

var activeAgentWaitAnyCalls = struct {
	sync.Mutex
	counts map[string]int
}{counts: make(map[string]int)}

func (s *Server) clientPrincipal() string {
	if s.Principal == "" {
		return "default"
	}
	return s.Principal
}

func (s *Server) clientActor() string {
	if strings.TrimSpace(s.Actor) != "" {
		return strings.TrimSpace(s.Actor)
	}
	return s.clientPrincipal()
}

func (s *Server) toolAllowed(name string) bool {
	if s.tools != nil && !s.tools[name] {
		return false
	}
	if s.Grants == nil {
		return true
	}
	tool, known := control.Lookup(name)
	return known && s.Grants[tool.Grant]
}

type localMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCP clients may attach protocol metadata such as progress tokens to a tool
// call. It is coordinator metadata, not a tool argument, so accept it at the
// envelope boundary while keeping strict decoding for every other field.
type localMCPToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type localMCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *localMCPError  `json:"error,omitempty"`
}

type localMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data is set only by the host mailbox client to report whether a call
	// was dispatched; direct servers never populate it.
	Data any `json:"data,omitempty"`
}

type LocalMCPTool = control.Tool

func (s *Server) Serve(ctx context.Context) error {
	callCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()
	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(s.Out)
	var calls sync.WaitGroup
	requestSlots := make(chan struct{}, maxConcurrentLocalMCPCalls)
	var encodeMu sync.Mutex
	var encodeErr error
	writeResponse := func(response localMCPResponse) {
		encodeMu.Lock()
		defer encodeMu.Unlock()
		if encodeErr == nil {
			encodeErr = encoder.Encode(response)
		}
	}
	dispatch := func(request localMCPRequest) {
		response, respond := s.handle(callCtx, request)
		if respond {
			writeResponse(response)
		}
	}
	contextCanceled := false
scanLoop:
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			contextCanceled = true
			break scanLoop
		default:
		}

		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			writeResponse(localMCPResponse{
				JSONRPC: "2.0",
				Error:   &localMCPError{Code: -32700, Message: "parse error"},
			})
			continue
		}
		// Tool calls may wait for terminals or agent runs. Dispatching them
		// independently lets the same stdio client send agent_stop, steering,
		// and status calls while another request is waiting.
		if request.Method == "tools/call" {
			if !acquireLocalMCPCallSlot(requestSlots) {
				if len(request.ID) > 0 {
					writeResponse(localMCPResponse{
						JSONRPC: "2.0", ID: request.ID,
						Error: &localMCPError{Code: -32000, Message: "too many concurrent tool calls"},
					})
				}
				continue
			}
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-requestSlots }()
				dispatch(request)
			}()
			continue
		}
		dispatch(request)
	}
	scanErr := scanner.Err()
	// stdin EOF means the owning MCP client is gone. Cancel bounded waits and
	// transport calls, but not the durable sessions/runs they were observing.
	cancelCalls()
	calls.Wait()
	if scanErr != nil {
		return fmt.Errorf("read MCP request: %w", scanErr)
	}
	if contextCanceled {
		return ctx.Err()
	}
	return encodeErr
}

func acquireLocalMCPCallSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) handle(ctx context.Context, request localMCPRequest) (localMCPResponse, bool) {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &localMCPError{Code: -32600, Message: "invalid request"}
		return response, len(request.ID) > 0
	}

	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": localMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":      "wingthing-local",
				"version":   s.Version,
				"principal": s.clientPrincipal(),
				"actor":     s.clientActor(),
			},
			"instructions": s.mcpInstructions(),
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		tools := LocalMCPTools()
		if s.Grants != nil {
			filtered := tools[:0]
			for _, tool := range tools {
				if s.toolAllowed(tool.Name) {
					filtered = append(filtered, tool)
				}
			}
			tools = filtered
		}
		response.Result = map[string]any{"tools": tools}
	case "tools/call":
		var call localMCPToolCallParams
		if err := decodeStrict(request.Params, &call); err != nil {
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
			break
		}
		if call.Name == "" {
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params: name is required"}
			break
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		data, isError, protocolErr := s.callTool(ctx, call.Name, call.Arguments)
		if protocolErr != nil {
			response.Error = protocolErr
			break
		}
		response.Result = localMCPToolResult(data, isError)
	default:
		if len(request.ID) == 0 {
			return localMCPResponse{}, false
		}
		response.Error = &localMCPError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response, len(request.ID) > 0
}

func (s *Server) mcpInstructions() string {
	base := "Wingthing is an agent manager for agents. Use terminal tools for persistent PTYs, agent_run for supervised semantic work, prompt_loop for bounded iteration, and swarm_run for a dependency DAG."
	if s.Unsandboxed {
		return base + " This server trusts an outer VM/container boundary: spawned processes have the full authority of the local OS user."
	}
	return base
}

func LocalMCPTools() []LocalMCPTool {
	return control.Tools(control.SurfaceLocalMCP)
}

// roostNativeMCPTools adapts the local typed control surface to authenticated
// Streamable HTTP MCP. The request principal is supplied by the roost after
// bearer-token verification and never accepted from tool arguments.
func RoostNativeMCPTools(version string, cfg *config.Config, sharedHost bool, sources ...func() (*config.WingConfig, *egg.EggConfig)) []mcppkg.NativeTool {
	return RoostNativeMCPToolsWithSessions(version, cfg, sharedHost, nil, sources...)
}

func RoostNativeMCPToolsWithSessions(version string, cfg *config.Config, sharedHost bool, sessionSource func() *wingsession.Service, sources ...func() (*config.WingConfig, *egg.EggConfig)) []mcppkg.NativeTool {
	// Standalone callers capture once; the embedded roost supplies the wing's
	// synchronized runtime snapshot, which changes on administrator reloads.
	var initial *config.WingConfig
	var initialEgg *egg.EggConfig
	var policyErr error
	var policySource func() (*config.WingConfig, *egg.EggConfig)
	if len(sources) > 0 {
		policySource = sources[0]
	} else {
		initial, policyErr = config.LoadWingConfig(cfg.Dir)
		if policyErr == nil {
			initialEgg, policyErr = loadRuntimeEggDefault(cfg.Dir, initial)
		}
		policySource = func() (*config.WingConfig, *egg.EggConfig) { return initial, initialEgg }
	}
	var tools []mcppkg.NativeTool
	admission := NewMCPAdmissionState()
	for _, localTool := range control.ToolsForAuthority(control.SurfaceHTTPMCP, control.AuthorityWing) {
		tool := localTool
		tools = append(tools, mcppkg.NativeTool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: tool.InputSchema, Annotations: tool.Annotations,
			Call: func(ctx context.Context, principal mcppkg.Principal, arguments json.RawMessage) (map[string]any, bool, error) {
				if principal.UserID == "" {
					return nil, true, errors.New("authenticated user identity is required")
				}
				if policyErr != nil {
					return nil, true, fmt.Errorf("load roost path policy: %w", policyErr)
				}
				wingCfg, wingDefault := policySource()
				paths, err := roostMCPPaths(wingCfg, principal.Email)
				if err != nil {
					return nil, true, err
				}
				server := newRoostNativeMCPServer(version, cfg, sharedHost, admission, principal, paths, func() (*config.WingConfig, *egg.EggConfig) { return wingCfg, wingDefault })
				if sessionSource != nil {
					server.Sessions = sessionSource()
					if server.Sessions == nil {
						return nil, true, errors.New("wing session service is not ready")
					}
				}
				data, isError, protocolErr := server.callTool(ctx, tool.Name, arguments)
				if protocolErr != nil {
					return map[string]any{"error": protocolErr.Message}, true, nil
				}
				return data, isError, nil
			},
		})
	}
	return tools
}

func newRoostNativeMCPServer(version string, cfg *config.Config, sharedHost bool, admission *AdmissionState, principal mcppkg.Principal, paths []string, sources ...func() (*config.WingConfig, *egg.EggConfig)) *Server {
	grants := GrantSet(defaultDirectMCPGrants)
	if portalTool, ok := control.Lookup("wing_list"); ok {
		grants[portalTool.Grant] = true
	}
	server := &Server{Version: version,
		Cfg: cfg, Logs: os.Stderr,
		Principal:         roostSessionPrincipal(principal.UserID),
		Actor:             principal.ClientID,
		Surface:           control.SurfaceHTTPMCP,
		Grants:            grants,
		MaxSessions:       defaultDirectMCPMaxSessions,
		MaxSpawnsPerHour:  defaultDirectMCPMaxSpawnsPerHour,
		admission:         admission,
		allowedPaths:      append([]string(nil), paths...),
		enforcePathBounds: true,
		identity: eggclient.EggIdentity{
			UserID: principal.UserID, Email: principal.Email, SharedHost: sharedHost,
			AllowedPaths: append([]string(nil), paths...), SealedFS: sharedHost,
		},
	}
	var wingDefault *egg.EggConfig
	var wingCfg *config.WingConfig
	var err error
	if len(sources) > 0 {
		wingCfg, wingDefault = sources[0]()
		if wingDefault == nil {
			err = errors.New("roost runtime egg policy is not ready")
		}
	} else {
		wingCfg, err = config.LoadWingConfig(cfg.Dir)
		if err == nil {
			wingDefault, err = loadRuntimeEggDefault(cfg.Dir, wingCfg)
		}
	}
	if wingCfg != nil {
		server.identity.OrgWing = wingCfg.Org != ""
	}
	if sharedHost || server.identity.OrgWing {
		member := wingCfg == nil || !wingCfg.IsAdmin(principal.Email)
		server.enforcePathBounds = member
		home, _ := os.UserHomeDir()
		server.launchConfig = runtimeLaunchConfig(wingCfg, home, member, paths, wingDefault, err)
	}
	server.sessionRole = "owner"
	if (sharedHost || server.identity.OrgWing) && (wingCfg == nil || !wingCfg.IsAdmin(principal.Email)) {
		server.sessionRole = "member"
	}
	if admission != nil && admission.Sessions != nil {
		server.Sessions = admission.Sessions
	} else {
		home, _ := os.UserHomeDir()
		server.Sessions = &wingsession.Service{Config: cfg, Home: home, SharedHost: sharedHost, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wingCfg, Egg: wingDefault} }}
	}

	return server
}

func loadRuntimeEggDefault(dir string, wingCfg *config.WingConfig) (*egg.EggConfig, error) {
	path := filepath.Join(dir, "egg.yaml")
	if wingCfg != nil && wingCfg.EggConfig != "" {
		path = wingCfg.EggConfig
	}
	cfg, err := egg.ResolveEggConfig(path)
	if errors.Is(err, os.ErrNotExist) && (wingCfg == nil || wingCfg.EggConfig == "") {
		cfg = egg.DefaultEggConfig()
		err = nil
	}
	return cfg, err
}

func runtimeLaunchConfig(wingCfg *config.WingConfig, home string, member bool, allowedRoots []string, wingDefault *egg.EggConfig, loadErr error) func(string) (*egg.EggConfig, error) {
	var roots []string
	if wingCfg != nil {
		roots = wingpolicy.ResolvePathStrings(wingCfg.Paths.Strings(), home)
	}
	return func(cwd string) (*egg.EggConfig, error) {
		if loadErr != nil {
			return nil, loadErr
		}
		if wingDefault == nil {
			return nil, errors.New("administrator runtime egg policy is not ready")
		}
		return eggclient.LoadRoostEggConfig(cwd, roots, allowedRoots, member, wingDefault)
	}
}

func roostSessionPrincipal(userID string) string {
	digest := sha256.Sum256([]byte(userID))
	return "user-" + hex.EncodeToString(digest[:10])
}

func roostMCPPaths(wingCfg *config.WingConfig, email string) ([]string, error) {
	if wingCfg == nil {
		return nil, errors.New("roost runtime policy is not ready")
	}
	home, _ := os.UserHomeDir()
	role := "member"
	if wingCfg.IsAdmin(email) {
		role = "admin"
	}
	return wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, email, role, home)), nil
}

func localMCPToolResult(data map[string]any, isError bool) map[string]any {
	encoded, err := json.Marshal(data)
	if err != nil {
		encoded = []byte(`{"error":"could not encode tool result"}`)
		isError = true
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(encoded)}},
		"structuredContent": data,
		"isError":           isError,
	}
}

func (s *Server) callTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, bool, *localMCPError) {
	var data map[string]any
	var err error
	isError := false
	defer func() {
		decision := "allowed"
		if err != nil || isError {
			decision = "error"
		}
		if auditErr := s.auditToolCall(name, arguments, data, decision); auditErr != nil {
			if logErr := cmdutil.Writef(s.Logs, "wingthing MCP audit: %v\n", auditErr); logErr != nil {
				log.Printf("write MCP audit failure: %v", logErr)
			}
		}
	}()
	tool, known := control.Lookup(name)
	if !known || !tool.Supports(s.controlSurface()) {
		err = fmt.Errorf("unknown tool: %s", name)
		return nil, false, &localMCPError{Code: -32602, Message: err.Error()}
	}
	if !s.toolAllowed(name) {
		err = fmt.Errorf("principal %q lacks grant %q", s.clientPrincipal(), tool.Grant)
		if s.tools != nil && !s.tools[name] {
			err = fmt.Errorf("tool %q is not available on this connection", name)
		}
		return map[string]any{"error": err.Error()}, true, nil
	}
	if err = s.checkBoundSessionTarget(name, arguments); err != nil {
		return map[string]any{"error": err.Error()}, true, nil
	}
	switch name {
	case "wingthing_capabilities":
		data, err = s.toolCapabilities(arguments)
	case "message_send":
		data, err = s.toolMessageSend(arguments)
	case "message_list":
		data, err = s.toolMessageList(arguments)
	case "message_wait":
		data, err = s.toolMessageWait(ctx, arguments)
	case "sandbox_explain":
		data, err = s.toolSandboxExplain(arguments)
	case "terminal_list":
		data, err = s.ToolTerminalList(ctx, arguments)
	case "terminal_read":
		data, err = s.toolTerminalRead(ctx, arguments)
	case "session_status":
		data, err = s.toolSessionStatus(ctx, arguments)
	case "session_read":
		data, err = s.toolSessionRead(ctx, arguments)
	case "session_wait":
		data, err = s.toolSessionWait(ctx, arguments)
	case "session_prompt":
		data, err = s.toolSessionPrompt(ctx, arguments)
	case "session_fork":
		data, err = s.ToolSessionFork(ctx, arguments)
	case "terminal_send":
		data, err = s.toolTerminalSend(ctx, arguments)
	case "terminal_wait":
		data, err = s.toolTerminalWait(ctx, arguments)
	case "terminal_start":
		data, err = s.toolTerminalStart(arguments)
	case "agent_start":
		data, err = s.toolAgentStart(arguments)
	case "conversation_list":
		data, err = s.ToolConversationList(arguments)
	case "conversation_bootstrap":
		data, err = s.toolConversationBootstrap(arguments)
	case "conversation_read":
		data, err = s.ToolConversationRead(ctx, arguments)
	case "conversation_checkpoint":
		data, err = s.toolConversationCheckpoint(arguments)
	case "conversation_wake":
		data, err = s.ToolConversationWake(arguments)
	case "agent_run":
		data, err = s.toolAgentRun(arguments)
	case "agent_status":
		data, err = s.toolAgentStatus(arguments)
	case "agent_wait":
		data, err = s.toolAgentWait(ctx, arguments)
	case "agent_wait_any":
		data, err = s.toolAgentWaitAny(ctx, arguments)
	case "agent_result":
		data, err = s.toolAgentResult(arguments)
	case "agent_events":
		data, err = s.toolAgentEvents(arguments)
	case "agent_steer":
		data, err = s.toolAgentSteer(arguments)
	case "agent_stop":
		data, err = s.toolAgentStop(arguments)
	case "terminal_rename":
		data, err = s.ToolTerminalRename(ctx, arguments)
	case "terminal_stop":
		data, err = s.toolTerminalStop(ctx, arguments)
	case "prompt_list":
		data, err = s.toolPromptList(arguments)
	case "prompt_get":
		data, err = s.toolPromptGet(arguments)
	case "prompt_save":
		data, err = s.toolPromptSave(arguments)
	case "prompt_run":
		data, isError, err = s.toolPromptRun(ctx, arguments)
	case "task_get":
		data, err = s.toolTaskGet(arguments)
	case "prompt_loop":
		data, isError, err = s.toolPromptLoop(ctx, arguments)
	case "swarm_run":
		data, isError, err = s.toolSwarmRun(ctx, arguments)
	default:
		err = fmt.Errorf("tool %q has no handler on %s", name, s.controlSurface())
		return nil, false, &localMCPError{Code: -32603, Message: err.Error()}
	}
	if err != nil {
		if logErr := cmdutil.Writef(s.Logs, "wingthing MCP %s: %v\n", name, err); logErr != nil {
			log.Printf("write MCP failure: %v", logErr)
		}
		return map[string]any{"error": err.Error()}, true, nil
	}
	return data, isError, nil
}

func (s *Server) auditToolCall(tool string, arguments json.RawMessage, result map[string]any, decision string) (resultErr error) {
	if s.Cfg == nil || s.Cfg.Dir == "" {
		return nil
	}
	target := control.AuditTarget(tool, arguments, result)
	digest := sha256.Sum256(arguments)
	record := map[string]any{
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
		"principal":       s.clientPrincipal(),
		"tool":            tool,
		"decision":        decision,
		"argument_sha256": fmt.Sprintf("%x", digest[:]),
		"isolation":       s.sessionIsolationMode(),
	}
	if s.Actor != "" {
		record["actor"] = s.Actor
	}
	if target != "" {
		record["target"] = target
	}
	if err := os.MkdirAll(s.Cfg.Dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.Cfg.Dir, "mcp-audit.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close MCP audit log: %w", err))
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	return json.NewEncoder(file).Encode(record)
}

func (s *Server) toolCapabilities(arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	agents := make([]map[string]any, 0)
	for _, definition := range agentpkg.Definitions() {
		profile := egg.Profile(definition.Name)
		path, lookErr := exec.LookPath(definition.Command)
		agents = append(agents, map[string]any{
			"name":                  definition.Name,
			"command":               definition.Command,
			"installed":             lookErr == nil,
			"path":                  path,
			"interactive":           true,
			"headless":              true,
			"resume":                definition.ResumeFlag != "",
			"resume_flag":           definition.ResumeFlag,
			"provider_substitution": definition.ProviderSubstitution,
			"release_canary":        definition.ReleaseCanary,
			"max_parallel":          definition.MaxParallel,
			"network_domains":       profile.Domains,
			"persistent_storage":    append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...),
		})
	}
	surface := s.controlSurface()
	operations := make([]string, 0)
	for _, tool := range control.Tools(surface) {
		if s.toolAllowed(tool.Name) {
			operations = append(operations, tool.Name)
		}
	}
	return map[string]any{
		"version":           s.Version,
		"principal":         s.clientPrincipal(),
		"agents":            agents,
		"actor":             s.clientActor(),
		"objects":           control.ObjectKinds(surface),
		"session_isolation": s.sessionIsolationMode(),
		"control_contract": map[string]any{
			"version":    control.ContractVersion,
			"surface":    string(surface),
			"operations": operations,
		},
		"transports": map[string]any{
			"local": true,
			"ssh":   true,
			"web":   true,
		},
	}, nil
}

func (s *Server) controlSurface() control.Surface {
	if s.Surface == "" {
		return control.SurfaceLocalMCP
	}
	return s.Surface
}

const maxMessageContentBytes = 32 << 10

var messageKinds = map[string]bool{
	"message":  true,
	"status":   true,
	"question": true,
	"answer":   true,
	"evidence": true,
	"error":    true,
}

func (s *Server) toolMessageSend(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Content    string `json:"content"`
		Channel    string `json:"channel"`
		ToActor    string `json:"to_actor"`
		Kind       string `json:"kind"`
		ReplyTo    string `json:"reply_to"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Content) == "" {
		return nil, errors.New("content is required")
	}
	if len([]byte(args.Content)) > maxMessageContentBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", maxMessageContentBytes)
	}
	channel, err := normalizeMessageChannel(args.Channel)
	if err != nil {
		return nil, err
	}
	toActor, err := normalizeMessageActorRef(args.ToActor, "to_actor")
	if err != nil {
		return nil, err
	}
	replyTo, err := normalizeMessageID(args.ReplyTo, "reply_to")
	if err != nil {
		return nil, err
	}
	kind := args.Kind
	if kind == "" {
		kind = "message"
	}
	if !messageKinds[kind] {
		return nil, fmt.Errorf("unsupported message kind %q", kind)
	}
	ttl := args.TTLSeconds
	if ttl == 0 {
		ttl = 86400
	}
	if ttl < 60 || ttl > 604800 {
		return nil, errors.New("ttl_seconds must be between 60 and 604800")
	}

	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("message store", db)
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	message := &store.Message{
		MessageID:      "msg-" + uuid.NewString(),
		OwnerID:        s.clientPrincipal(),
		SenderActor:    s.clientActor(),
		RecipientActor: toActor,
		Channel:        channel,
		Kind:           kind,
		ReplyTo:        replyTo,
		Content:        args.Content,
		ExpiresAt:      time.Now().UTC().Add(time.Duration(ttl) * time.Second),
	}
	if err := db.CreateMessage(message); err != nil {
		return nil, err
	}
	return map[string]any{
		"message":    messageResult(message),
		"message_id": message.MessageID,
		"owner":      s.clientPrincipal(),
		"actor":      s.clientActor(),
	}, nil
}

func (s *Server) toolMessageList(arguments json.RawMessage) (map[string]any, error) {
	var args messageListArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if err := args.normalize(); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("message store", db)
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	messages, err := db.ListMessages(s.clientPrincipal(), s.clientActor(), args.Channel, args.AfterID, args.Limit, args.IncludeSent)
	if err != nil {
		return nil, err
	}
	return messageListResult(s, args.Channel, args.AfterID, messages, false), nil
}

func (s *Server) toolMessageWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args messageWaitArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if err := args.normalize(); err != nil {
		return nil, err
	}
	db, err := s.openMessageStore()
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("message store", db)
	if err := db.PurgeExpiredMessages(); err != nil {
		return nil, err
	}
	timeout := args.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	if timeout < 0.1 || timeout > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	timer := time.NewTimer(time.Duration(timeout * float64(time.Second)))
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		messages, err := db.ListMessages(s.clientPrincipal(), s.clientActor(), args.Channel, args.AfterID, args.Limit, false)
		if err != nil {
			return nil, err
		}
		if len(messages) > 0 {
			return messageListResult(s, args.Channel, args.AfterID, messages, false), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return messageListResult(s, args.Channel, args.AfterID, nil, true), nil
		case <-ticker.C:
		}
	}
}

type messageListArgs struct {
	Channel     string `json:"channel"`
	AfterID     string `json:"after_id"`
	Limit       int    `json:"limit"`
	IncludeSent bool   `json:"include_sent"`
}

func (args *messageListArgs) normalize() error {
	channel, err := normalizeMessageChannel(args.Channel)
	if err != nil {
		return err
	}
	afterID, err := normalizeMessageID(args.AfterID, "after_id")
	if err != nil {
		return err
	}
	args.Channel = channel
	args.AfterID = afterID
	if args.Limit == 0 {
		args.Limit = 20
	}
	if args.Limit < 1 || args.Limit > 20 {
		return errors.New("limit must be between 1 and 20")
	}
	return nil
}

type messageWaitArgs struct {
	Channel        string  `json:"channel"`
	AfterID        string  `json:"after_id"`
	Limit          int     `json:"limit"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
}

func (args *messageWaitArgs) normalize() error {
	list := messageListArgs{Channel: args.Channel, AfterID: args.AfterID, Limit: args.Limit}
	if err := list.normalize(); err != nil {
		return err
	}
	args.Channel, args.AfterID, args.Limit = list.Channel, list.AfterID, list.Limit
	return nil
}

func normalizeMessageChannel(channel string) (string, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "factory"
	}
	if err := eggclient.ValidateSessionName(channel); err != nil {
		return "", fmt.Errorf("invalid message channel: %w", err)
	}
	return channel, nil
}

func normalizeMessageActorRef(actor, field string) (string, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", nil
	}
	if len(actor) > 256 {
		return "", fmt.Errorf("%s must be at most 256 characters", field)
	}
	for _, r := range actor {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("%s must contain printable non-space ASCII", field)
		}
	}
	return actor, nil
}

func normalizeMessageID(id, field string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	if len(id) > 128 || !strings.HasPrefix(id, "msg-") {
		return "", fmt.Errorf("%s is not a Wingthing message ID", field)
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "", fmt.Errorf("%s is not a Wingthing message ID", field)
		}
	}
	return id, nil
}

func (s *Server) openMessageStore() (*store.Store, error) {
	if s.Cfg == nil || s.Cfg.Dir == "" {
		return nil, errors.New("wingthing state directory is required for messages")
	}
	if err := os.MkdirAll(s.Cfg.Dir, 0700); err != nil {
		return nil, err
	}
	return store.Open(s.Cfg.DBPath())
}

func messageResult(message *store.Message) map[string]any {
	return map[string]any{
		"message_id":   message.MessageID,
		"sender_actor": message.SenderActor,
		"to_actor":     message.RecipientActor,
		"channel":      message.Channel,
		"kind":         message.Kind,
		"reply_to":     message.ReplyTo,
		"content":      message.Content,
		"created_at":   message.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at":   message.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func messageListResult(s *Server, channel, afterID string, messages []*store.Message, timedOut bool) map[string]any {
	items := make([]map[string]any, 0, len(messages))
	next := afterID
	for _, message := range messages {
		items = append(items, messageResult(message))
		next = message.MessageID
	}
	return map[string]any{
		"owner":         s.clientPrincipal(),
		"actor":         s.clientActor(),
		"channel":       channel,
		"messages":      items,
		"next_after_id": next,
		"timed_out":     timedOut,
	}
}

func (s *Server) sessionIsolationMode() string {
	if s.Unsandboxed {
		return "outer-boundary"
	}
	return "wingthing-sandbox"
}

func (s *Server) toolSandboxExplain(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Agent           string `json:"agent"`
		Config          string `json:"config"`
		CWD             string `json:"cwd"`
		ProviderBaseURL string `json:"provider_base_url"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	cwd, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	if args.Config != "" {
		args.Config, err = s.resolveConfigPath(args.Config)
		if err != nil {
			return nil, err
		}
	}
	var eggCfg *egg.EggConfig
	var source string
	if s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) {
		if args.Config != "" {
			return nil, errors.New("sandbox_explain uses the administrator runtime policy on shared or organization hosts")
		}
		eggCfg, err = s.loadLaunchConfig(cwd)
		if err != nil {
			return nil, err
		}
		source = "administrator runtime policy"
	} else if s.Unsandboxed && args.Config != "" {
		return nil, errors.New("sandbox_explain config cannot be combined with MCP server --unsandboxed; spawned processes use the outer host boundary")
	} else if s.Unsandboxed {
		eggCfg = egg.UnsandboxedEggConfig()
		source = "MCP server --unsandboxed"
	} else {
		eggCfg, source, err = eggclient.LoadEggConfigForExplain(args.Config, cwd)
		if err != nil {
			return nil, err
		}
	}
	home, _ := os.UserHomeDir()
	policy, err := eggclient.ExplainPolicyWithProvider(eggCfg, args.Agent, home, source, args.ProviderBaseURL)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": policy}, nil
}

func (s *Server) resolveConfigPath(path string) (string, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox config path: %w", err)
	}
	canonical := filepath.Clean(resolved)
	if evaluated, evalErr := filepath.EvalSymlinks(canonical); evalErr == nil {
		canonical = evaluated
	}
	if s.enforcePathBounds && (len(s.allowedPaths) == 0 || !wingpolicy.IsUnderPaths(canonical, s.allowedPaths)) {
		return "", fmt.Errorf("sandbox config %q is outside this user's roost paths", path)
	}
	return canonical, nil
}

func (s *Server) ownsSession(session eggclient.LocalSession) bool {
	if s.Sessions != nil {
		return s.Sessions.Owns(s.sessionAuthority(), session)
	}
	principal := s.clientPrincipal()
	if principal == "default" {
		return session.Principal == "" || session.Principal == principal
	}
	return session.Principal == principal
}

func (s *Server) resolveOwnedSession(ctx context.Context, ref string) (eggclient.LocalSession, error) {
	session, err := eggclient.ResolveOwnedActiveSession(ctx, s.Cfg, ref, s.ownsSession)
	if err != nil {
		return eggclient.LocalSession{}, err
	}
	if !s.ownsSession(session) {
		return eggclient.LocalSession{}, errors.New("session not found or not owned by caller")
	}
	if s.enforcePathBounds && (len(s.allowedPaths) == 0 || !wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(session.CWD), s.allowedPaths)) {
		return eggclient.LocalSession{}, errors.New("session not found or not owned by caller")
	}
	// A host mailbox connection was checked against its tree by exact ID; a
	// live label or prefix match must not substitute another session. Direct
	// bound connections keep the deployed label/prefix resolution.
	if s.broker != nil && session.ID != ref {
		return eggclient.LocalSession{}, errors.New("session is outside this MCP connection's bound task tree; use its exact execution session ID")
	}
	return session, nil
}

func (s *Server) checkSessionBounds() error {
	if s.MaxSessions > 0 {
		sessions, err := eggclient.DiscoverSessionRefs(s.Cfg)
		if err != nil {
			return err
		}
		owned := 0
		for _, session := range sessions {
			if s.ownsSession(session) {
				owned++
			}
		}
		if owned >= s.MaxSessions {
			return fmt.Errorf("principal %q reached max_sessions=%d", s.clientPrincipal(), s.MaxSessions)
		}
	}
	return nil
}

func (s *Server) checkSpawnBounds() error {
	if err := s.checkSessionBounds(); err != nil {
		return err
	}
	if s.MaxSpawnsPerHour > 0 {
		s.spawnMu.Lock()
		defer s.spawnMu.Unlock()
		cutoff := time.Now().Add(-time.Hour)
		kept := s.spawnTimes[:0]
		for _, timestamp := range s.spawnTimes {
			if timestamp.After(cutoff) {
				kept = append(kept, timestamp)
			}
		}
		s.spawnTimes = kept
		if len(s.spawnTimes) >= s.MaxSpawnsPerHour {
			return fmt.Errorf("principal %q reached max_spawns_per_hour=%d", s.clientPrincipal(), s.MaxSpawnsPerHour)
		}
	}
	return nil
}

func (s *Server) recordSpawn() {
	if s.MaxSpawnsPerHour <= 0 {
		return
	}
	s.spawnMu.Lock()
	s.spawnTimes = append(s.spawnTimes, time.Now())
	s.spawnMu.Unlock()
}

// admitSpawn serializes bounds admission: the check, the spawn, and the
// recording happen under one lock so concurrent tool calls cannot both observe
// a free slot and together exceed max_sessions or max_spawns_per_hour.
func (s *Server) admitSpawn(spawn func() error) error {
	if s.admission != nil {
		s.admission.mu.Lock()
		defer s.admission.mu.Unlock()
		if err := s.checkSharedSpawnBounds(); err != nil {
			return err
		}
		if err := spawn(); err != nil {
			return err
		}
		if s.MaxSpawnsPerHour > 0 {
			principal := s.clientPrincipal()
			s.admission.spawnTimes[principal] = append(s.admission.spawnTimes[principal], time.Now())
		}
		return nil
	}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if err := s.checkSpawnBounds(); err != nil {
		return err
	}
	if err := spawn(); err != nil {
		return err
	}
	s.recordSpawn()
	return nil
}

// checkSharedSpawnBounds runs with admission.mu held.
func (s *Server) checkSharedSpawnBounds() error {
	if err := s.checkSessionBounds(); err != nil {
		return err
	}
	if s.MaxSpawnsPerHour > 0 {
		principal := s.clientPrincipal()
		cutoff := time.Now().Add(-time.Hour)
		history := s.admission.spawnTimes[principal]
		kept := history[:0]
		for _, timestamp := range history {
			if timestamp.After(cutoff) {
				kept = append(kept, timestamp)
			}
		}
		if len(kept) == 0 {
			delete(s.admission.spawnTimes, principal)
		} else {
			s.admission.spawnTimes[principal] = kept
		}
		if len(kept) >= s.MaxSpawnsPerHour {
			return fmt.Errorf("principal %q reached max_spawns_per_hour=%d", principal, s.MaxSpawnsPerHour)
		}
	}
	return nil
}

func (s *Server) ToolTerminalList(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Remote *string `json:"remote"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Remote != nil {
		if err := config.ValidateRemoteName(*args.Remote); err != nil {
			return nil, err
		}
		// A local path or conversation boundary cannot authorize inventory on
		// another filesystem. Keep these restricted connections on their host.
		if s.enforcePathBounds || s.BoundConversation != "" {
			return nil, errors.New("remote terminal_list is unavailable on a path- or conversation-bound MCP connection")
		}
		remote, err := remotepkg.ConfiguredRemote(s.Cfg.Dir, *args.Remote)
		if err != nil {
			return nil, err
		}
		sessions, err := eggclient.QueryRemoteSessions(s.Version, ctx, *args.Remote, remote, remotepkg.Streams(ctx))
		if err != nil {
			return nil, err
		}
		owned := make([]eggclient.MachineSession, 0, len(sessions))
		for _, session := range sessions {
			if s.ownsSession(session) {
				owned = append(owned, eggclient.MachineSession{LocalSession: session, Machine: *args.Remote})
			}
		}
		return map[string]any{"sessions": owned}, nil
	}
	var sessions []eggclient.LocalSession
	var err error
	if s.Sessions != nil {
		sessions, err = s.Sessions.List(ctx, s.sessionAuthority())
	} else {
		sessions, err = eggclient.DiscoverActiveSessions(ctx, s.Cfg)
	}
	if err != nil {
		return nil, err
	}
	root := ""
	if s.broker != nil && s.BoundConversation != "" {
		db, err := s.openMessageStore()
		if err != nil {
			return nil, err
		}
		bound, err := s.ownedConversation(db, s.BoundConversation)
		cmdutil.CloseWithLog("bound terminal list store", db)
		if err != nil {
			return nil, err
		}
		root = bound.RootID
	}
	owned := make([]eggclient.LocalSession, 0, len(sessions))
	for _, session := range sessions {
		if root != "" && session.RootConversationID != root {
			continue
		}
		if s.ownsSession(session) && (!s.enforcePathBounds || (len(s.allowedPaths) > 0 && wingpolicy.IsUnderPaths(wingpolicy.CanonicalSessionPath(session.CWD), s.allowedPaths))) {
			owned = append(owned, session)
		}
	}
	// Each entry includes the shared hook-derived status from session discovery.
	return map[string]any{"sessions": owned}, nil
}

func (s *Server) toolTerminalRead(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	session, snapshot, err := eggclient.ReadSessionSnapshot(ctx, s.Cfg, owned.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"session":     session.ID,
		"label":       session.Name,
		"ansi":        string(snapshot),
		"base64":      base64.StdEncoding.EncodeToString(snapshot),
		"byte_length": len(snapshot),
	}, nil
}

func (s *Server) toolTerminalSend(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Input   string `json:"input"`
		Enter   bool   `json:"enter"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	input := []byte(args.Input)
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	session, err := eggclient.SendSessionInput(ctx, s.Cfg, owned.ID, input, args.Enter, s.identity.UserID)
	if err != nil {
		return nil, err
	}
	bytesSent := len(input)
	if args.Enter {
		bytesSent++
	}
	return map[string]any{"session": session.ID, "bytes_sent": bytesSent}, nil
}

func (s *Server) toolTerminalWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session        string  `json:"session"`
		Contains       string  `json:"contains"`
		IdleSeconds    float64 `json:"idle_seconds"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	owned, err := s.resolveOwnedSession(waitCtx, args.Session)
	if err != nil {
		return nil, err
	}
	if args.Contains != "" {
		session, err := eggclient.WaitForSessionText(waitCtx, s.Cfg, owned.ID, args.Contains)
		if err != nil {
			return nil, err
		}
		return map[string]any{"session": session.ID, "condition": "contains", "value": args.Contains}, nil
	}
	if args.IdleSeconds == 0 {
		args.IdleSeconds = 2
	}
	if args.IdleSeconds < 0.2 {
		return nil, errors.New("idle_seconds must be at least 0.2")
	}
	session, ec, err := eggclient.OpenLocalEgg(waitCtx, s.Cfg, owned.ID)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, statusErr := ec.Status(waitCtx)
		if statusErr != nil {
			return nil, statusErr
		}
		if float64(status.IdleSeconds) >= args.IdleSeconds {
			return map[string]any{"session": session.ID, "condition": "idle", "idle_seconds": status.IdleSeconds}, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) toolTerminalStart(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Command []string `json:"command"`
		CWD     string   `json:"cwd"`
		Label   string   `json:"label"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	args.CWD = resolvedCWD
	kind := "command"
	if len(args.Command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		args.Command = []string{shell}
		kind = "shell"
	}
	eggCfg, err := s.loadSessionLaunchConfig(args.CWD)
	if err != nil {
		return nil, err
	}
	sessionID := cmdutil.NewRuntimeID()
	if err := s.admitSpawn(func() error {
		ec, spawnErr := s.startSession(sessionID, "", args.CWD, eggCfg,
			eggclient.SpawnEggOpts{Label: args.Label, Kind: kind, Command: args.Command, Principal: s.clientPrincipal()})
		if spawnErr != nil {
			return spawnErr
		}
		cmdutil.CloseWithLog("spawned terminal egg client", ec)
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{
		"session": sessionID, "label": args.Label, "kind": kind,
		"command": args.Command, "cwd": args.CWD, "isolation": s.sessionIsolationMode(),
	}, nil
}

func (s *Server) toolAgentStart(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Agent                string   `json:"agent"`
		Model                string   `json:"model"`
		CWD                  string   `json:"cwd"`
		Label                string   `json:"label"`
		Unattended           bool     `json:"unattended"`
		Args                 []string `json:"args"`
		ConversationRole     string   `json:"conversation_role"`
		ParentConversationID string   `json:"parent_conversation_id"`
		RequestID            string   `json:"request_id"`
		ResumeSession        string   `json:"resume_session,omitempty"`
		Input                string   `json:"input,omitempty"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.ResumeSession != "" {
		return s.toolAgentContinue(arguments)
	}
	if args.Input != "" {
		return nil, errors.New("input requires resume_session")
	}
	if args.Agent == "" {
		return nil, errors.New("agent is required")
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	if err := eggclient.ValidateSessionName(args.Label); err != nil {
		return nil, err
	}
	if err := eggclient.ValidateAgentArgs(args.Args); err != nil {
		return nil, err
	}
	modelArgs, err := agentModelArgs(args.Agent, args.Model)
	if err != nil {
		return nil, err
	}
	args.Args = append(modelArgs, args.Args...)
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	args.CWD = resolvedCWD
	var eggCfg *egg.EggConfig
	eggCfg, err = s.loadSessionLaunchConfig(args.CWD)
	if err != nil {
		return nil, err
	}
	if args.Unattended {
		copyCfg := *eggCfg
		eggCfg = &copyCfg
		eggCfg.DangerouslySkipPermissions = true
	}
	sessionID := cmdutil.NewRuntimeID()
	// Hash normalized launch arguments, excluding the retry key itself.
	spec := args
	spec.RequestID = ""
	conversation, created, err := s.reserveAgentConversation(args.Agent, args.CWD, args.Label, args.ConversationRole, args.ParentConversationID, args.RequestID, sessionID, spec)
	if err != nil {
		return nil, err
	}
	if conversation != nil && !created {
		return conversationLaunchResult(conversation, true), nil
	}
	if conversation != nil && conversation.ParentID == "" {
		model := args.Model
		for i, arg := range args.Args {
			if arg == "--model" && i+1 < len(args.Args) {
				model = args.Args[i+1]
			} else if value, ok := strings.CutPrefix(arg, "--model="); ok {
				model = value
			}
		}
		if err := saveCoordinatorModel(s.Cfg.Dir, sessionID, model); err != nil {
			if saveErr := s.markConversationLaunch(conversation, err); saveErr != nil {
				return nil, saveErr
			}
			return nil, err
		}
	}
	var managedParent *conversationBrokerRegistration
	args.Args, managedParent, err = s.prepareBoundParentLaunch(conversation, eggCfg, args.Args)
	if err != nil {
		if saveErr := s.markConversationLaunch(conversation, err); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	spawnErr := s.admitSpawn(func() error {
		if err := s.preflightBrokerChild(eggCfg, args.Agent, args.CWD, sessionID); err != nil {
			return err
		}
		opts := eggclient.SpawnEggOpts{Label: args.Label, Kind: "agent", AgentArgs: args.Args, Principal: s.clientPrincipal()}
		// Broker-managed parents and every child of a host mailbox carry the
		// protected state/executable set and omit the browser bridge.
		if managedParent != nil {
			opts = managedParent.launchOpts(s.Cfg, opts)
		} else if s.broker != nil {
			opts = s.broker.launchOpts(s.Cfg, opts)
		}
		ec, spawnErr := s.startSession(sessionID, args.Agent, args.CWD, eggCfg, opts)
		if spawnErr != nil {
			return spawnErr
		}
		cmdutil.CloseWithLog("spawned agent egg client", ec)
		return nil
	})
	if err := s.markConversationLaunch(conversation, spawnErr); err != nil {
		return nil, err
	}
	if spawnErr != nil {
		return nil, spawnErr
	}
	if conversation != nil {
		conversation.LaunchState = "started"
		return conversationLaunchResult(conversation, false), nil
	}
	return map[string]any{
		"session": sessionID, "label": args.Label, "agent": args.Agent,
		"cwd": args.CWD, "args": args.Args, "isolation": s.sessionIsolationMode(),
	}, nil
}

func agentModelArgs(agentName, model string) ([]string, error) {
	if model == "" {
		return nil, nil
	}
	if strings.TrimSpace(model) != model || strings.IndexByte(model, 0) >= 0 {
		return nil, errors.New("model must be a non-empty provider model name without surrounding whitespace")
	}
	switch agentName {
	case "claude":
		return []string{"--model", model}, nil
	case "codex":
		return []string{"-m", model}, nil
	default:
		return nil, fmt.Errorf("model selection is not defined for agent %q", agentName)
	}
}

type agentRunArgs struct {
	Prompt         string `json:"prompt"`
	Agent          string `json:"agent"`
	Model          string `json:"model"`
	CWD            string `json:"cwd"`
	Label          string `json:"label"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type agentRunFollowup struct {
	parentID  string
	direction string
}

const maxAgentSteerPriorResultChars = 200000

func (s *Server) toolAgentRun(arguments json.RawMessage) (map[string]any, error) {
	var args agentRunArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	return s.submitAgentRun(args, nil)
}

func (s *Server) submitAgentRun(args agentRunArgs, followup *agentRunFollowup) (map[string]any, error) {
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if args.Agent == "" {
		args.Agent = s.Cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	if _, err := agentModelArgs(args.Agent, args.Model); err != nil {
		return nil, err
	}
	if err := eggclient.ValidateSessionName(args.Label); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 900
	}
	if args.TimeoutSeconds < 10 || args.TimeoutSeconds > 7200 {
		return nil, errors.New("timeout_seconds must be between 10 and 7200")
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, err
	}
	var dependsOn *string
	var parentID *string
	if followup != nil {
		encoded, _ := json.Marshal([]string{followup.parentID})
		value := string(encoded)
		dependsOn = &value
		parent := followup.parentID
		parentID = &parent
	}
	now := time.Now().UTC()
	task := &store.Task{
		ID: cmdutil.GenTaskID(), Type: "agent_run", What: args.Prompt,
		Agent: args.Agent, Model: args.Model, TimeoutSeconds: args.TimeoutSeconds,
		RunAt: now, CreatedAt: now,
		ParentID: parentID, DependsOn: dependsOn, CWD: resolvedCWD,
		Principal: s.clientPrincipal(), RunnerPID: os.Getpid(),
	}
	if s.Unsandboxed {
		task.Isolation = "privileged"
	}
	if err := s.freezeTaskLaunchConfig(task); err != nil {
		return nil, err
	}
	if err := s.admitSpawn(func() error {
		taskStore, openErr := store.Open(s.Cfg.DBPath())
		if openErr != nil {
			return openErr
		}
		defer cmdutil.CloseWithLog("task store", taskStore)
		if createErr := taskStore.CreateTask(task); createErr != nil {
			return createErr
		}
		if args.Label != "" {
			label := args.Label
			if labelErr := taskStore.AppendLog(task.ID, "label", &label); labelErr != nil {
				log.Printf("record label for agent run %s: %v", task.ID, labelErr)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	s.startAgentRun(task.ID, followup)
	data := agentRunStatusData(task)
	data["run_id"] = task.ID
	if args.Label != "" {
		data["label"] = args.Label
	}
	return data, nil
}

func (s *Server) startAgentRun(runID string, followup *agentRunFollowup) {
	options, optionsErr := s.agentTaskRunOptions()
	if optionsErr != nil {
		s.setAgentRunError(runID, optionsErr)
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	key := s.agentRunKey(runID)
	done := make(chan struct{})
	activeMCPAgentRuns.Store(key, activeMCPAgentRun{principal: s.clientPrincipal(), cancel: cancel, done: done})
	go func() {
		defer close(done)
		defer cancel()
		defer activeMCPAgentRuns.Delete(key)
		var resolvedFollowupPrompt string
		if followup != nil {
			if err := s.waitForAgentRunTerminal(runCtx, followup.parentID); err != nil {
				s.setAgentRunError(runID, err)
				return
			}
			parent, parentStore, err := s.ownedAgentRun(followup.parentID)
			if err != nil {
				s.setAgentRunError(runID, err)
				return
			}
			if err := parentStore.Close(); err != nil {
				s.setAgentRunError(runID, fmt.Errorf("close parent task store: %w", err))
				return
			}
			if !agentRunTerminal(parent.Status) {
				s.setAgentRunError(runID, fmt.Errorf("parent agent run %s finished with status %s", parent.ID, parent.Status))
				return
			}
			var parentResult string
			if parent.Output != nil {
				parentResult = *parent.Output
			}
			var parentError string
			if parent.Error != nil {
				parentError = *parent.Error
			}
			resolvedFollowupPrompt = agentSteerPrompt(parent.What, parentResult, parentError, followup.direction)
		}
		taskStore, err := store.Open(s.Cfg.DBPath())
		if err != nil {
			s.setAgentRunError(runID, err)
			return
		}
		defer cmdutil.CloseWithLog("task store", taskStore)
		task, err := taskStore.GetTask(runID)
		if err != nil || task == nil {
			if err == nil {
				err = fmt.Errorf("run %q disappeared before execution", runID)
			}
			s.setAgentRunError(runID, err)
			return
		}
		if followup != nil {
			if err := taskStore.SetTaskWhat(runID, resolvedFollowupPrompt); err != nil {
				s.setAgentRunError(runID, fmt.Errorf("record resolved follow-up prompt: %w", err))
				return
			}
			task.What = resolvedFollowupPrompt
		}
		var runErr error
		if s.runAgentTask != nil {
			runErr = s.runAgentTask(runCtx, s.Cfg, taskStore, task, options)
		} else {
			runErr = taskrun.RunTaskToWithOptions(runCtx, s.Cfg, taskStore, task, io.Discard, options)
		}
		if runErr != nil {
			s.setAgentRunError(runID, runErr)
		}
	}()
}

func (s *Server) agentTaskRunOptions() (taskrun.TaskRunOptions, error) {
	options := taskrun.TaskRunOptions{
		SharedHost:   s.identity.SharedHost,
		AllowedPaths: append([]string(nil), s.identity.AllowedPaths...),
	}
	if s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) {
		options.UserHome = filepath.Join(s.Cfg.Dir, "user-homes", eggclient.UserHash(s.identity.UserID))
		if !s.identity.SharedHost {
			if err := os.MkdirAll(options.UserHome, 0700); err != nil {
				return taskrun.TaskRunOptions{}, fmt.Errorf("create isolated agent home: %w", err)
			}
		}
	}
	return options, nil
}

func (s *Server) freezeTaskLaunchConfig(task *store.Task) error {
	if s.Unsandboxed && s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) {
		return errors.New("privileged isolation is not available on a shared or organization host")
	}
	cwd, err := s.resolveWorkingDirectory(task.CWD)
	if err != nil {
		return err
	}
	cfg, err := s.loadLaunchConfig(cwd)
	if err != nil {
		return err
	}
	task.EggConfigYAML, err = cfg.TaskYAML()
	task.CWD = cwd
	return err
}

func (s *Server) runTask(ctx context.Context, taskStore *store.Store, task *store.Task) error {
	options, err := s.agentTaskRunOptions()
	if err != nil {
		return err
	}
	if s.runAgentTask != nil {
		return s.runAgentTask(ctx, s.Cfg, taskStore, task, options)
	}
	return taskrun.RunTaskToWithOptions(ctx, s.Cfg, taskStore, task, io.Discard, options)
}

func (s *Server) setAgentRunError(runID string, runErr error) {
	if runErr == nil {
		return
	}
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		log.Printf("record agent run %s failure: open store: %v", runID, err)
		return
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	if err := taskStore.SetTaskError(runID, runErr.Error()); err != nil {
		log.Printf("record agent run %s failure: %v", runID, err)
	}
}

func (s *Server) agentRunKey(runID string) string {
	return s.Cfg.DBPath() + "\x00" + runID
}

func (s *Server) ownedAgentRun(runID string) (*store.Task, *store.Store, error) {
	if runID == "" {
		return nil, nil, errors.New("run_id is required")
	}
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	task, err := s.loadOwnedAgentRun(taskStore, runID)
	if err == nil && task == nil {
		err = fmt.Errorf("agent run %q not found or not owned by caller", runID)
	}
	if err != nil {
		return nil, nil, cmdutil.CloseAndJoin("task store", taskStore, err)
	}
	return task, taskStore, nil
}

func (s *Server) loadOwnedAgentRun(taskStore *store.Store, runID string) (*store.Task, error) {
	task, err := taskStore.GetTask(runID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.Type != "agent_run" || !s.ownsTask(task) {
		return nil, nil
	}
	if (task.Status == "pending" || task.Status == "running") && task.RunnerPID > 0 && !procinfo.OwnedProcessIsAlive(task.RunnerPID) {
		if err := taskStore.SetTaskError(task.ID, fmt.Sprintf("supervising Wingthing process %d exited", task.RunnerPID)); err != nil {
			return nil, fmt.Errorf("mark orphaned agent run failed: %w", err)
		}
		task, err = taskStore.GetTask(runID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("agent run %q disappeared after orphan cleanup", runID)
		}
	}
	return task, nil
}

func agentRunTerminal(status string) bool {
	return status == "done" || status == "failed" || status == "timeout" || status == "stopped"
}

func agentRunStatusData(task *store.Task) map[string]any {
	data := map[string]any{
		"run_id": task.ID, "status": task.Status, "agent": task.Agent,
		"model": task.Model, "cwd": task.CWD, "isolation": task.Isolation,
		"timeout_seconds": task.TimeoutSeconds,
		"created_at":      task.CreatedAt.UTC().Format(time.RFC3339),
	}
	if task.StartedAt != nil {
		data["started_at"] = task.StartedAt.UTC().Format(time.RFC3339)
	}
	if task.FinishedAt != nil {
		data["finished_at"] = task.FinishedAt.UTC().Format(time.RFC3339)
	}
	return data
}

func (s *Server) toolAgentStatus(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	return agentRunStatusData(task), nil
}

func (s *Server) toolAgentWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID          string  `json:"run_id"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	err := s.waitForAgentRunTerminal(waitCtx, args.RunID)
	task, taskStore, loadErr := s.ownedAgentRun(args.RunID)
	if loadErr != nil {
		return nil, loadErr
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	data := agentRunStatusData(task)
	if errors.Is(err, context.DeadlineExceeded) {
		data["timed_out"] = true
		return data, nil
	}
	return data, err
}

func (s *Server) waitForAgentRunTerminal(ctx context.Context, runID string) error {
	task, taskStore, err := s.ownedAgentRun(runID)
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	if agentRunTerminal(task.Status) {
		return nil
	}
	return waitForAgentRunCondition(ctx, func() (bool, error) {
		task, err = taskStore.GetTask(runID)
		if err != nil {
			return false, err
		}
		if task == nil || task.Type != "agent_run" || !s.ownsTask(task) {
			return false, fmt.Errorf("agent run %q not found", runID)
		}
		return agentRunTerminal(task.Status), nil
	})
}

// The task store has no notifications, so both agent wait tools share one
// bounded polling cadence, including when updates come from another process.
func waitForAgentRunCondition(ctx context.Context, ready func() (bool, error)) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if done, err := ready(); err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// AgentWaitAny exposes the same authorized and audited operation to the CLI.
func (s *Server) AgentWaitAny(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	data, isError, protocolErr := s.callTool(ctx, "agent_wait_any", arguments)
	if protocolErr != nil {
		return nil, errors.New(protocolErr.Message)
	}
	if isError {
		return nil, fmt.Errorf("%v", data["error"])
	}
	return data, nil
}

func (s *Server) toolAgentWaitAny(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunIDs         []string `json:"run_ids"`
		TimeoutSeconds float64  `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if len(args.RunIDs) < 1 || len(args.RunIDs) > 64 {
		return nil, errors.New("run_ids must contain between 1 and 64 IDs")
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 600")
	}
	release, err := s.admitAgentWaitAny()
	if err != nil {
		return nil, err
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	runIDs := make([]string, 0, len(args.RunIDs))
	seen := make(map[string]bool, len(args.RunIDs))
	for _, runID := range args.RunIDs {
		if !seen[runID] {
			seen[runID] = true
			runIDs = append(runIDs, runID)
		}
	}
	var data map[string]any
	err = waitForAgentRunCondition(waitCtx, func() (bool, error) {
		tasks, err := s.loadOwnedAgentRunStatuses(taskStore.DB(), runIDs)
		if err != nil {
			return false, err
		}
		finished := []map[string]any{}
		pending := []string{}
		lookupErrors := []map[string]any{}
		for _, runID := range runIDs {
			task := tasks[runID]
			if task != nil && (task.Status == "pending" || task.Status == "running") && task.RunnerPID > 0 && !procinfo.OwnedProcessIsAlive(task.RunnerPID) {
				if err := taskStore.SetTaskError(runID, fmt.Sprintf("supervising Wingthing process %d exited", task.RunnerPID)); err != nil {
					return false, fmt.Errorf("mark orphaned agent run failed: %w", err)
				}
				task.Status = "failed"
			}
			if task == nil {
				lookupErrors = append(lookupErrors, map[string]any{"run_id": runID, "error": fmt.Sprintf("agent run %q not found or not owned by caller", runID)})
			} else if agentRunTerminal(task.Status) {
				finished = append(finished, map[string]any{"run_id": runID, "status": task.Status})
			} else {
				pending = append(pending, runID)
			}
		}
		data = map[string]any{"finished": finished, "pending": pending}
		if len(lookupErrors) > 0 {
			data["errors"] = lookupErrors
		}
		return len(finished) > 0 || len(pending) == 0, nil
	})
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return data, nil
	}
	return data, err
}

func (s *Server) admitAgentWaitAny() (func(), error) {
	key := s.Cfg.DBPath() + "\x00" + s.clientPrincipal()
	activeAgentWaitAnyCalls.Lock()
	defer activeAgentWaitAnyCalls.Unlock()
	if activeAgentWaitAnyCalls.counts[key] >= maxConcurrentAgentWaitAnyCalls {
		return nil, errors.New("too many concurrent waits (maximum 4 per principal)")
	}
	activeAgentWaitAnyCalls.counts[key]++
	return func() {
		activeAgentWaitAnyCalls.Lock()
		defer activeAgentWaitAnyCalls.Unlock()
		activeAgentWaitAnyCalls.counts[key]--
		if activeAgentWaitAnyCalls.counts[key] == 0 {
			delete(activeAgentWaitAnyCalls.counts, key)
		}
	}, nil
}

func (s *Server) loadOwnedAgentRunStatuses(db *sql.DB, runIDs []string) (map[string]*store.Task, error) {
	arguments := make([]any, 0, len(runIDs)+2)
	arguments = append(arguments, s.clientPrincipal(), s.clientPrincipal())
	for _, runID := range runIDs {
		arguments = append(arguments, runID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(runIDs)), ",")
	rows, err := db.Query(`SELECT id, status, runner_pid FROM tasks
		WHERE type = 'agent_run' AND (principal = ? OR (principal = '' AND ? = 'default'))
		AND id IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("agent run rows", rows)
	tasks := make(map[string]*store.Task, len(runIDs))
	for rows.Next() {
		task := &store.Task{}
		if err := rows.Scan(&task.ID, &task.Status, &task.RunnerPID); err != nil {
			return nil, err
		}
		tasks[task.ID] = task
	}
	return tasks, rows.Err()
}

func (s *Server) toolAgentResult(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID    string `json:"run_id"`
		MaxChars int    `json:"max_chars"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.MaxChars == 0 {
		args.MaxChars = 50000
	}
	if args.MaxChars < 1 || args.MaxChars > 200000 {
		return nil, errors.New("max_chars must be between 1 and 200000")
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	data := agentRunStatusData(task)
	data["ready"] = agentRunTerminal(task.Status)
	if task.Output != nil {
		outputRunes := []rune(*task.Output)
		output := string(outputRunes)
		if len(outputRunes) > args.MaxChars {
			output = string(outputRunes[:args.MaxChars])
			data["truncated"] = true
			data["total_chars"] = len(outputRunes)
		}
		data["output"] = output
	}
	if task.Error != nil {
		data["error"] = *task.Error
	}
	return data, nil
}

func (s *Server) toolAgentEvents(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
		Limit int    `json:"limit"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Limit > 200 {
		return nil, errors.New("limit must be between 1 and 200")
	}
	_, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	rows, err := taskStore.DB().Query(`SELECT timestamp, event, COALESCE(detail, '') FROM task_log WHERE task_id = ? ORDER BY id DESC LIMIT ?`, args.RunID, args.Limit)
	if err != nil {
		return nil, err
	}
	var events []map[string]any
	for rows.Next() {
		var timestamp, event, detail string
		if err := rows.Scan(&timestamp, &event, &detail); err != nil {
			return nil, err
		}
		entry := map[string]any{"timestamp": timestamp, "event": event}
		if detail != "" {
			entry["detail"] = detail
		}
		events = append(events, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return map[string]any{"run_id": args.RunID, "events": events}, nil
}

func (s *Server) toolAgentSteer(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID  string `json:"run_id"`
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	parent, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	if err := taskStore.Close(); err != nil {
		return nil, fmt.Errorf("close task store: %w", err)
	}
	if !agentRunTerminal(parent.Status) {
		return nil, fmt.Errorf("agent run %s is not terminal (status %s)", parent.ID, parent.Status)
	}
	model := args.Model
	if model == "" {
		model = parent.Model
	}
	parentID := parent.ID
	return s.submitAgentRun(agentRunArgs{
		Prompt: "Prior request:\n" + parent.What + "\n\nNew direction:\n" + args.Prompt,
		Agent:  parent.Agent, Model: model,
		CWD: parent.CWD, Label: "followup-" + parent.ID, TimeoutSeconds: parent.TimeoutSeconds,
	}, &agentRunFollowup{parentID: parentID, direction: args.Prompt})
}

func agentSteerPrompt(parentRequest, parentResult, parentError, direction string) string {
	resultRunes := []rune(parentResult)
	if len(resultRunes) > maxAgentSteerPriorResultChars {
		parentResult = string(resultRunes[:maxAgentSteerPriorResultChars]) + "\n\n[Wingthing truncated the prior result for this follow-up.]"
	}
	prompt := "Prior request:\n" + parentRequest + "\n\nPrior result:\n" + parentResult
	if parentError != "" {
		prompt += "\n\nPrior error:\n" + parentError
	}
	return prompt + "\n\nNew direction:\n" + direction
}

func (s *Server) toolAgentStop(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	task, taskStore, err := s.ownedAgentRun(args.RunID)
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	if agentRunTerminal(task.Status) {
		return agentRunStatusData(task), nil
	}
	activeValue, ok := activeMCPAgentRuns.Load(s.agentRunKey(args.RunID))
	active, valid := activeValue.(activeMCPAgentRun)
	if !ok || !valid || active.principal != s.clientPrincipal() {
		// The runner can finish between the first database read and the active
		// map lookup. Reload before reporting a detached run so an idempotent
		// stop never turns a successful completion race into an error.
		latest, reloadErr := taskStore.GetTask(args.RunID)
		if reloadErr != nil {
			return nil, reloadErr
		}
		if latest != nil && agentRunTerminal(latest.Status) {
			return agentRunStatusData(latest), nil
		}
		return nil, errors.New("run is no longer attached to this Wingthing process")
	}
	active.cancel()
	select {
	case <-active.done:
	case <-time.After(15 * time.Second):
		return nil, errors.New("agent run cancellation is still in progress")
	}
	latest, err := taskStore.GetTask(args.RunID)
	if err != nil || latest == nil {
		return nil, fmt.Errorf("reload stopped agent run: %w", err)
	}
	message := "stopped by MCP principal " + s.clientPrincipal()
	if err := taskStore.SetTaskError(args.RunID, message); err != nil {
		return nil, err
	}
	latest.Status = "failed"
	latest.Error = &message
	data := agentRunStatusData(latest)
	data["stopped"] = true
	return data, nil
}

func (s *Server) ToolTerminalRename(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" || args.Name == "" {
		return nil, errors.New("session and name are required")
	}
	if err := eggclient.ValidateSessionName(args.Name); err != nil {
		return nil, err
	}
	session, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	lock, err := eggclient.AcquireSessionNameLock(s.Cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if err := eggclient.EnsureSessionNameAvailable(s.Cfg, args.Name, session.ID); err != nil {
		return nil, err
	}
	if err := eggclient.WriteSessionName(filepath.Join(s.Cfg.Dir, "eggs", session.ID), args.Name); err != nil {
		return nil, err
	}
	return map[string]any{"session": session.ID, "name": args.Name}, nil
}

func (s *Server) toolTerminalStop(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Session string `json:"session"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Session == "" {
		return nil, errors.New("session is required")
	}
	owned, err := s.resolveOwnedSession(ctx, args.Session)
	if err != nil {
		return nil, err
	}
	var session eggclient.LocalSession
	if s.Sessions != nil {
		session, err = s.Sessions.Stop(ctx, s.sessionAuthority(), owned.ID)
		if err != nil {
			return nil, err
		}
	} else {
		var ec *egg.Client
		session, ec, err = eggclient.OpenLocalEgg(ctx, s.Cfg, owned.ID)
		if err != nil {
			return nil, err
		}
		defer cmdutil.CloseWithLog("egg client", ec)
		if err := ec.Kill(ctx, session.ID); err != nil {
			return nil, err
		}
	}

	return map[string]any{"session": session.ID, "status": "stopped"}, nil
}

func (s *Server) toolPromptList(arguments json.RawMessage) (map[string]any, error) {
	if err := requireEmptyObject(arguments); err != nil {
		return nil, err
	}
	assets, err := promptmgr.New(s.Cfg.PromptsDir()).List()
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompts": assets}, nil
}

func (s *Server) toolPromptGet(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Name == "" {
		return nil, errors.New("name is required")
	}
	asset, err := promptmgr.New(s.Cfg.PromptsDir()).Get(args.Name, args.Revision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompt": asset}, nil
}

func (s *Server) toolPromptSave(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		Name             string   `json:"name"`
		Description      string   `json:"description"`
		Template         string   `json:"template"`
		Variables        []string `json:"variables"`
		Agent            string   `json:"agent"`
		CWD              string   `json:"cwd"`
		ExpectedRevision string   `json:"expected_revision"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Name == "" || strings.TrimSpace(args.Template) == "" {
		return nil, errors.New("name and template are required")
	}
	if args.Agent != "" {
		if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
			return nil, fmt.Errorf("unsupported agent %q", args.Agent)
		}
	}
	asset, err := promptmgr.New(s.Cfg.PromptsDir()).Save(promptmgr.Asset{
		Name: args.Name, Description: args.Description, Template: args.Template,
		Variables: args.Variables, Agent: args.Agent, CWD: args.CWD,
	}, args.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	return map[string]any{"prompt": asset}, nil
}

func (s *Server) toolPromptRun(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args struct {
		Prompt     string            `json:"prompt"`
		PromptName string            `json:"prompt_name"`
		Revision   string            `json:"revision"`
		Variables  map[string]string `json:"variables"`
		Agent      string            `json:"agent"`
		CWD        string            `json:"cwd"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	hasRaw := strings.TrimSpace(args.Prompt) != ""
	hasSaved := args.PromptName != ""
	if hasRaw == hasSaved {
		return nil, false, errors.New("provide exactly one of prompt or prompt_name")
	}
	promptName := ""
	promptRevision := ""
	if hasSaved {
		asset, err := promptmgr.New(s.Cfg.PromptsDir()).Get(args.PromptName, args.Revision)
		if err != nil {
			return nil, false, err
		}
		args.Prompt, err = promptmgr.Render(asset, args.Variables)
		if err != nil {
			return nil, false, err
		}
		promptName = asset.Name
		promptRevision = asset.Revision
		if args.Agent == "" {
			args.Agent = asset.Agent
		}
		if args.CWD == "" {
			args.CWD = asset.CWD
		}
	} else if args.Revision != "" || len(args.Variables) > 0 {
		return nil, false, errors.New("revision and variables require prompt_name")
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD
	task, runErr := s.executePrompt(ctx, args.Prompt, args.Agent, args.CWD, promptName, promptRevision, nil, nil)
	if task == nil {
		return nil, true, runErr
	}
	return taskData(task), runErr != nil, nil
}

func (s *Server) toolTaskGet(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TaskID == "" {
		return nil, errors.New("task_id is required")
	}
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	task, err := taskStore.GetTask(args.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", args.TaskID)
	}
	if !s.ownsTask(task) {
		owner := task.Principal
		if owner == "" {
			owner = "human/default"
		}
		return nil, fmt.Errorf("task %s is owned by principal %q; caller is %q", task.ID, owner, s.clientPrincipal())
	}
	return taskData(task), nil
}

func (s *Server) ownsTask(task *store.Task) bool {
	if s.clientPrincipal() == "default" {
		return task.Principal == "" || task.Principal == "default"
	}
	return task.Principal == s.clientPrincipal()
}

func (s *Server) executePrompt(ctx context.Context, prompt, agentName, cwd, promptName, promptRevision string, parentID, dependsOn *string) (*store.Task, error) {
	if agentName == "" {
		agentName = s.Cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(agentName); !ok {
		return nil, fmt.Errorf("unsupported agent %q", agentName)
	}
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	task := &store.Task{
		ID: cmdutil.GenTaskID(), Type: "prompt", What: prompt, Agent: agentName,
		RunAt: time.Now().UTC(), ParentID: parentID, DependsOn: dependsOn, CWD: cwd,
		PromptName: promptName, PromptRevision: promptRevision, Principal: s.clientPrincipal(),
	}
	if s.Unsandboxed {
		task.Isolation = "privileged"
	}
	if err := s.freezeTaskLaunchConfig(task); err != nil {
		return nil, err
	}
	if err := taskStore.CreateTask(task); err != nil {
		return nil, err
	}
	runErr := s.runTask(ctx, taskStore, task)
	stored, getErr := taskStore.GetTask(task.ID)
	if getErr != nil {
		return task, errors.Join(runErr, getErr)
	}
	return stored, runErr
}

type promptLoopArgs struct {
	Prompt        string `json:"prompt"`
	Agent         string `json:"agent"`
	CWD           string `json:"cwd"`
	MaxIterations int    `json:"max_iterations"`
	UntilContains string `json:"until_contains"`
}

func (s *Server) toolPromptLoop(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args promptLoopArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, false, errors.New("prompt is required")
	}
	if args.MaxIterations == 0 {
		args.MaxIterations = 3
	}
	if args.MaxIterations < 1 || args.MaxIterations > 12 {
		return nil, false, errors.New("max_iterations must be between 1 and 12")
	}
	if args.Agent == "" {
		args.Agent = s.Cfg.DefaultAgent
	}
	if _, ok := agentpkg.LookupDefinition(args.Agent); !ok {
		return nil, false, fmt.Errorf("unsupported agent %q", args.Agent)
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD

	probe := &store.Task{CWD: args.CWD}
	if s.Unsandboxed {
		probe.Isolation = "privileged"
	}
	if err := s.freezeTaskLaunchConfig(probe); err != nil {
		return nil, false, err
	}

	root, rootStore, err := s.createMetaTask("loop", args.Prompt, args.Agent, args.CWD)
	if err != nil {
		return nil, false, err
	}
	if err := rootStore.Close(); err != nil {
		return nil, false, fmt.Errorf("close root task store: %w", err)
	}
	results := make([]map[string]any, 0, args.MaxIterations)
	var previousTaskID string
	failed := false
	stopReason := "max_iterations"
	for iteration := 1; iteration <= args.MaxIterations; iteration++ {
		select {
		case <-ctx.Done():
			failed = true
			stopReason = "cancelled"
			iteration = args.MaxIterations
			continue
		default:
		}
		var dependsJSON *string
		if previousTaskID != "" {
			encoded, _ := json.Marshal([]string{previousTaskID})
			value := string(encoded)
			dependsJSON = &value
		}
		task, runErr := s.executePrompt(ctx, args.Prompt, args.Agent, args.CWD, "", "", &root.ID, dependsJSON)
		if task != nil {
			entry := taskData(task)
			entry["iteration"] = iteration
			results = append(results, entry)
			previousTaskID = task.ID
		}
		if runErr != nil || task == nil || task.Status != "done" {
			failed = true
			stopReason = "failed"
			break
		}
		if args.UntilContains != "" && task.Output != nil && strings.Contains(*task.Output, args.UntilContains) {
			stopReason = "condition_met"
			break
		}
	}
	status := "done"
	if failed {
		status = "failed"
	}
	data := map[string]any{
		"loop_id": root.ID, "status": status, "stop_reason": stopReason,
		"iterations": results, "until_contains": args.UntilContains,
	}
	if err := s.finishMetaTask(root.ID, status, data); err != nil {
		return nil, true, err
	}
	return data, failed, nil
}

type swarmNodeSpec struct {
	ID        string   `json:"id"`
	Prompt    string   `json:"prompt"`
	Agent     string   `json:"agent"`
	DependsOn []string `json:"depends_on"`
}

type swarmRunArgs struct {
	Name        string          `json:"name"`
	CWD         string          `json:"cwd"`
	MaxParallel int             `json:"max_parallel"`
	Nodes       []swarmNodeSpec `json:"nodes"`
}

type swarmNodeResult struct {
	logicalID string
	task      *store.Task
	err       error
}

func (s *Server) toolSwarmRun(ctx context.Context, arguments json.RawMessage) (map[string]any, bool, error) {
	var args swarmRunArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, false, err
	}
	if args.MaxParallel == 0 {
		args.MaxParallel = 2
	}
	if args.MaxParallel < 1 || args.MaxParallel > 4 {
		return nil, false, errors.New("max_parallel must be between 1 and 4")
	}
	if len(args.Nodes) < 1 || len(args.Nodes) > 16 {
		return nil, false, errors.New("nodes must contain between 1 and 16 entries")
	}
	if err := validateSwarm(args.Nodes, s.Cfg.DefaultAgent); err != nil {
		return nil, false, err
	}
	if args.Name == "" {
		args.Name = "agent swarm"
	}
	resolvedCWD, err := s.resolveWorkingDirectory(args.CWD)
	if err != nil {
		return nil, false, err
	}
	args.CWD = resolvedCWD

	probe := &store.Task{CWD: args.CWD}
	if s.Unsandboxed {
		probe.Isolation = "privileged"
	}
	if err := s.freezeTaskLaunchConfig(probe); err != nil {
		return nil, false, err
	}

	root, rootStore, err := s.createMetaTask("swarm", args.Name, s.Cfg.DefaultAgent, args.CWD)
	if err != nil {
		return nil, false, err
	}
	defer cmdutil.CloseWithLog("root task store", rootStore)

	taskIDs := make(map[string]string, len(args.Nodes))
	byID := make(map[string]swarmNodeSpec, len(args.Nodes))
	for _, node := range args.Nodes {
		taskIDs[node.ID] = cmdutil.GenTaskID()
		byID[node.ID] = node
	}
	for _, node := range args.Nodes {
		agentName := node.Agent
		if agentName == "" {
			agentName = s.Cfg.DefaultAgent
		}
		depends := make([]string, 0, len(node.DependsOn))
		for _, dep := range node.DependsOn {
			depends = append(depends, taskIDs[dep])
		}
		var dependsJSON *string
		if len(depends) > 0 {
			encoded, _ := json.Marshal(depends)
			value := string(encoded)
			dependsJSON = &value
		}
		parentID := root.ID
		task := &store.Task{
			ID: taskIDs[node.ID], Type: "prompt", What: node.Prompt, Agent: agentName,
			RunAt: time.Now().UTC(), ParentID: &parentID, DependsOn: dependsJSON, CWD: args.CWD,
			Principal: s.clientPrincipal(),
		}
		if s.Unsandboxed {
			task.Isolation = "privileged"
		}
		if err := s.freezeTaskLaunchConfig(task); err != nil {
			return nil, true, errors.Join(err, rootStore.UpdateTaskStatus(root.ID, "failed"))
		}
		if err := rootStore.CreateTask(task); err != nil {
			return nil, true, errors.Join(err, rootStore.UpdateTaskStatus(root.ID, "failed"))
		}
	}

	state := make(map[string]string, len(args.Nodes))
	results := make(map[string]*store.Task, len(args.Nodes))
	agentSemaphores := make(map[string]chan struct{})
	for _, definition := range agentpkg.Definitions() {
		if definition.MaxParallel > 0 {
			agentSemaphores[definition.Name] = make(chan struct{}, definition.MaxParallel)
		}
	}
	for len(state) < len(args.Nodes) {
		var ready []swarmNodeSpec
		for _, node := range args.Nodes {
			if state[node.ID] != "" {
				continue
			}
			allTerminal := true
			dependencyFailed := false
			for _, dep := range node.DependsOn {
				if state[dep] == "" {
					allTerminal = false
					break
				}
				if state[dep] != "done" {
					dependencyFailed = true
				}
			}
			if !allTerminal {
				continue
			}
			if dependencyFailed {
				message := "one or more dependencies failed"
				if err := rootStore.SetTaskError(taskIDs[node.ID], message); err != nil {
					return nil, true, fmt.Errorf("mark blocked swarm node %s failed: %w", node.ID, err)
				}
				skipped, err := rootStore.GetTask(taskIDs[node.ID])
				if err != nil {
					return nil, true, fmt.Errorf("reload blocked swarm node %s: %w", node.ID, err)
				}
				results[node.ID] = skipped
				state[node.ID] = "blocked"
				continue
			}
			ready = append(ready, node)
		}
		if len(ready) == 0 {
			break
		}

		resultCh := make(chan swarmNodeResult, len(ready))
		semaphore := make(chan struct{}, args.MaxParallel)
		var wg sync.WaitGroup
		for _, node := range ready {
			node := node
			state[node.ID] = "running"
			wg.Add(1)
			go func() {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				agentName := node.Agent
				if agentName == "" {
					agentName = s.Cfg.DefaultAgent
				}
				if agentSemaphore := agentSemaphores[agentName]; agentSemaphore != nil {
					agentSemaphore <- struct{}{}
					defer func() { <-agentSemaphore }()
				}
				taskStore, openErr := store.Open(s.Cfg.DBPath())
				if openErr != nil {
					resultCh <- swarmNodeResult{logicalID: node.ID, err: openErr}
					return
				}
				defer cmdutil.CloseWithLog("task store", taskStore)
				task, getErr := taskStore.GetTask(taskIDs[node.ID])
				if getErr == nil && task != nil {
					runErr := s.runTask(ctx, taskStore, task)
					refreshed, refreshErr := taskStore.GetTask(task.ID)
					if refreshErr == nil {
						task = refreshed
					}
					getErr = errors.Join(runErr, refreshErr)
				}
				resultCh <- swarmNodeResult{logicalID: node.ID, task: task, err: getErr}
			}()
		}
		wg.Wait()
		close(resultCh)
		for result := range resultCh {
			results[result.logicalID] = result.task
			if result.err != nil || result.task == nil || result.task.Status != "done" {
				state[result.logicalID] = "failed"
			} else {
				state[result.logicalID] = "done"
			}
		}
	}

	failed := false
	nodeData := make([]map[string]any, 0, len(args.Nodes))
	for _, node := range args.Nodes {
		entry := map[string]any{
			"id": node.ID, "task_id": taskIDs[node.ID], "status": state[node.ID],
			"depends_on": node.DependsOn,
		}
		if task := results[node.ID]; task != nil {
			entry["task"] = taskData(task)
		}
		if state[node.ID] != "done" {
			failed = true
		}
		nodeData = append(nodeData, entry)
	}
	status := "done"
	if failed {
		status = "failed"
	}
	data := map[string]any{"swarm_id": root.ID, "name": args.Name, "status": status, "nodes": nodeData}
	if err := s.finishMetaTask(root.ID, status, data); err != nil {
		return nil, true, err
	}
	return data, failed, nil
}

func validateSwarm(nodes []swarmNodeSpec, defaultAgent string) error {
	byID := make(map[string]swarmNodeSpec, len(nodes))
	for _, node := range nodes {
		if err := eggclient.ValidateSessionName(node.ID); err != nil || node.ID == "" {
			return fmt.Errorf("invalid swarm node ID %q", node.ID)
		}
		if _, exists := byID[node.ID]; exists {
			return fmt.Errorf("duplicate swarm node ID %q", node.ID)
		}
		if strings.TrimSpace(node.Prompt) == "" {
			return fmt.Errorf("swarm node %q has an empty prompt", node.ID)
		}
		agentName := node.Agent
		if agentName == "" {
			agentName = defaultAgent
		}
		if _, ok := agentpkg.LookupDefinition(agentName); !ok {
			return fmt.Errorf("swarm node %q uses unsupported agent %q", node.ID, agentName)
		}
		byID[node.ID] = node
	}
	for _, node := range nodes {
		for _, dependency := range node.DependsOn {
			if dependency == node.ID {
				return fmt.Errorf("swarm node %q depends on itself", node.ID)
			}
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("swarm node %q depends on unknown node %q", node.ID, dependency)
			}
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("swarm dependency cycle includes %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range byID[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) createMetaTask(kind, what, agentName, cwd string) (*store.Task, *store.Store, error) {
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	task := &store.Task{
		ID: cmdutil.GenTaskID(), Type: kind, What: what, Agent: agentName,
		RunAt: time.Now().UTC(), Status: "pending", CWD: cwd, Principal: s.clientPrincipal(),
	}
	if err := taskStore.CreateTask(task); err != nil {
		return nil, nil, cmdutil.CloseAndJoin("task store", taskStore, err)
	}
	if err := taskStore.UpdateTaskStatus(task.ID, "running"); err != nil {
		return nil, nil, cmdutil.CloseAndJoin("task store", taskStore, err)
	}
	return task, taskStore, nil
}

func (s *Server) finishMetaTask(taskID, status string, data map[string]any) error {
	taskStore, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		return err
	}
	defer cmdutil.CloseWithLog("task store", taskStore)
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if status == "failed" {
		if err := taskStore.SetTaskError(taskID, "one or more child tasks failed"); err != nil {
			return err
		}
		return taskStore.SetTaskOutput(taskID, string(encoded))
	}
	if err := taskStore.SetTaskOutput(taskID, string(encoded)); err != nil {
		return err
	}
	return taskStore.UpdateTaskStatus(taskID, "done")
}

func taskData(task *store.Task) map[string]any {
	data := map[string]any{
		"id": task.ID, "type": task.Type, "prompt": task.What,
		"agent": task.Agent, "isolation": task.Isolation, "status": task.Status,
		"run_at":      task.RunAt.UTC().Format(time.RFC3339),
		"created_at":  task.CreatedAt.UTC().Format(time.RFC3339),
		"retry_count": task.RetryCount, "max_retries": task.MaxRetries,
		"principal": task.Principal,
	}
	if task.Model != "" {
		data["model"] = task.Model
	}
	if task.CWD != "" {
		data["cwd"] = task.CWD
	}
	if task.PromptName != "" {
		data["prompt_name"] = task.PromptName
		data["prompt_revision"] = task.PromptRevision
	}
	if task.ParentID != nil {
		data["parent_id"] = *task.ParentID
	}
	if task.DependsOn != nil {
		var dependencies []string
		if json.Unmarshal([]byte(*task.DependsOn), &dependencies) == nil {
			data["depends_on"] = dependencies
		}
	}
	if task.StartedAt != nil {
		data["started_at"] = task.StartedAt.UTC().Format(time.RFC3339)
	}
	if task.FinishedAt != nil {
		data["finished_at"] = task.FinishedAt.UTC().Format(time.RFC3339)
	}
	if task.Output != nil {
		data["output"] = *task.Output
	}
	if task.Error != nil {
		data["error"] = *task.Error
	}
	return data
}

func (s *Server) resolveWorkingDirectory(cwd string) (string, error) {
	if cwd == "" && s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) && len(s.allowedPaths) > 0 {
		cwd = s.allowedPaths[0]
	}
	resolved, err := ResolveWorkingDirectory(cwd)
	if err != nil {
		return "", err
	}
	canonical := wingpolicy.CanonicalSessionPath(resolved)
	if s.enforcePathBounds && len(s.allowedPaths) == 0 {
		return "", errors.New("this roost user has no configured workspace paths")
	}
	if len(s.allowedPaths) > 0 && !wingpolicy.IsUnderPaths(canonical, s.allowedPaths) && (s.enforcePathBounds || !(s.identity.SharedHost || s.identity.OrgWing)) {
		return "", fmt.Errorf("working directory %q is outside this user's roost paths", resolved)
	}
	return canonical, nil
}

// Every launch on this surface rechecks cwd and uses the caller's current policy.
func (s *Server) loadLaunchConfig(cwd string) (*egg.EggConfig, error) {
	cwd, err := s.resolveWorkingDirectory(cwd)
	if err != nil {
		return nil, err
	}
	if s.launchConfig != nil {
		return s.launchConfig(cwd)
	}
	if s.broker != nil {
		return s.broker.childEggConfig(cwd)
	}
	if s.identity.UserID != "" && (s.identity.SharedHost || s.identity.OrgWing) {
		return nil, errors.New("administrator runtime egg policy is required")
	}
	return eggclient.LoadSpawnEggConfig("", cwd, s.Unsandboxed)
}

func ResolveWorkingDirectory(cwd string) (string, error) {
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
	} else {
		cwd, err = filepath.Abs(cwd)
	}
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil {
		return "", fmt.Errorf("working directory %q: %w", cwd, statErr)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", cwd)
	}
	return cwd, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func requireEmptyObject(data []byte) error {
	var value map[string]any
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return errors.New("tool accepts no arguments")
	}
	return nil
}

func durationSeconds(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
