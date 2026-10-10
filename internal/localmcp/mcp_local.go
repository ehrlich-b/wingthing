package localmcp

import (
	"bufio"
	"context"
	"crypto/sha256"
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
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

const localMCPProtocolVersion = "2025-11-25"

const maxConcurrentLocalMCPCalls = 64

const maxConcurrentAgentWaitAnyCalls = 4

type Server struct {
	legacyLocalDefault                 bool
	Sessions                           *wingsession.Service
	sessionLaunch                      *wingsession.Launch
	mailboxLocked                      bool
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
	return serveStdio(ctx, s.In, s.Out, s.handle)
}

func serveStdio(ctx context.Context, in io.Reader, out io.Writer, handle func(context.Context, localMCPRequest) (localMCPResponse, bool)) error {
	callCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(out)
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
		response, respond := handle(callCtx, request)
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
	base := "Wingthing is an agent manager for agents. Use terminal tools for persistent PTYs and agent_run for supervised semantic work."
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
				if isError {
					return data, true, control.ToolError(data)
				}
				return data, false, nil
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
	if admission != nil {
		server.Sessions = admission.Sessions
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
	if s.mailboxLocked && conversationBrokerMutations[name] {
		return map[string]any{"error": "the wing is locked; host mailbox mutations are paused"}, true, nil
	}
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
		return control.ErrorResult(err), true, nil
	}
	if err = s.checkBoundSessionTarget(name, arguments); err != nil {
		return control.ErrorResult(err), true, nil
	}
	switch name {
	case "wingthing_capabilities":
		data, err = s.toolCapabilities(arguments)
	case "wing_list":
		data, err = s.toolLocalWingList(arguments)
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
	default:
		err = fmt.Errorf("tool %q has no handler on %s", name, s.controlSurface())
		return nil, false, &localMCPError{Code: -32603, Message: err.Error()}
	}
	if err != nil {
		if logErr := cmdutil.Writef(s.Logs, "wingthing MCP %s: %v\n", name, err); logErr != nil {
			log.Printf("write MCP failure: %v", logErr)
		}
		return control.ErrorResult(err), true, nil
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

func (s *Server) openConversationStore() (*store.Store, error) {
	if s.Cfg == nil || s.Cfg.Dir == "" {
		return nil, errors.New("wingthing state directory is required for conversations")
	}
	if err := os.MkdirAll(s.Cfg.Dir, 0700); err != nil {
		return nil, err
	}
	return store.Open(s.Cfg.DBPath())
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
	return s.Sessions != nil && s.Sessions.Owns(s.sessionAuthority(), session)
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
		if s.Sessions != nil && s.Sessions.RunManager != nil {
			owned = s.Sessions.RunManager.ReservedSessions(s.sessionAuthority())
		}
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
			if session.Principal == s.clientPrincipal() || s.legacyLocalDefault && (session.Principal == "" || session.Principal == "default") {
				owned = append(owned, eggclient.MachineSession{LocalSession: session, Machine: *args.Remote})
			}
		}
		return map[string]any{"sessions": owned}, nil
	}
	var sessions []eggclient.LocalSession
	var err error
	if s.Sessions == nil {
		return nil, errors.New("wing session service is not ready")
	}
	sessions, err = s.Sessions.List(ctx, s.sessionAuthority())
	if err != nil {
		return nil, err
	}
	root := ""
	if s.broker != nil && s.BoundConversation != "" {
		db, err := s.openConversationStore()
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
	var session eggclient.LocalSession
	var snapshot []byte
	session, snapshot, err = s.Sessions.Snapshot(ctx, s.sessionAuthority(), owned.ID)
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
	var session eggclient.LocalSession
	session, err = s.Sessions.Send(ctx, s.sessionAuthority(), owned.ID, input, args.Enter)
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

// AgentWaitAny exposes the shared wing operation to authenticated adapters.
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
	if s.Sessions == nil {
		return nil, errors.New("wing session service is not ready")
	}
	session, err := s.Sessions.Stop(ctx, s.sessionAuthority(), args.Session)
	if err != nil {
		return nil, err
	}

	return map[string]any{"session": session.ID, "status": "stopped"}, nil
}

func (s *Server) ownsTask(task *store.Task) bool {
	if s.clientPrincipal() == "default" || s.legacyLocalDefault {
		return task.Principal == "" || task.Principal == "default"
	}
	return task.Principal == s.clientPrincipal()
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
	return s.loadSessionLaunchConfig(cwd)
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
