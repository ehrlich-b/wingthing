package egg

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/contextclient"
)

const (
	ToolCapabilityEnv                  = "WT_TOOL_CAPABILITY"
	maxToolRequestBytes                = 256 << 10
	maxConcurrentToolSocketConnections = 64
)

// ToolRequest is sent by `wt tool-call` over the Unix socket.
type ToolRequest struct {
	Capability string   `json:"capability,omitempty"`
	Action     string   `json:"action,omitempty"` // "list" for tool discovery
	Tool       string   `json:"tool,omitempty"`
	Args       []string `json:"args,omitempty"`
}

// MarshalJSON keeps tool-call's argv contract unchanged. Generated shims use
// the session-only environment, including for discovery; host callers can
// supply a capability explicitly. Never accept ambient environment on decode.
func (req ToolRequest) MarshalJSON() ([]byte, error) {
	if req.Capability == "" {
		req.Capability = os.Getenv(ToolCapabilityEnv)
	}
	type wireRequest ToolRequest
	return json.Marshal(wireRequest(req))
}

// Capabilities remain in controller and egg wrapper memory, never on disk.
var toolSocketCapabilities sync.Map // canonical socket path -> *ToolListener

// ToolSocketCapability supplies only the same-process egg spawn plumbing.
func ToolSocketCapability(sockPath string) (string, error) {
	listener, ok := toolSocketCapabilities.Load(config.CanonicalProviderPath(sockPath))
	if !ok {
		return "", fmt.Errorf("tool listener is not active for this egg")
	}
	return listener.(*ToolListener).capability, nil
}

// ToolResponse is returned to the client.
type ToolResponse struct {
	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ToolListEntry describes one tool for the list action.
type ToolListEntry struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Params      []config.ToolParam `json:"params,omitempty"`
}

// ToolListResponse is returned for the "list" action.
type ToolListResponse struct {
	Tools []ToolListEntry `json:"tools"`
}

// ToolListener accepts connections on a Unix socket and dispatches tool execution to a
// shared ToolRunner. Egg sessions reach tools this way; the remote MCP server wraps the
// same runner over HTTP.
type ToolListener struct {
	capability  string
	owner       string
	ownerID     string
	runner      *ToolRunner
	listener    net.Listener
	connections chan struct{}
	wg          sync.WaitGroup
}

// ToolContext binds a listener to its wing-owned Context client and verified owner.
type ToolContext struct {
	Client    *contextclient.Client
	Owner     string // verified EggIdentity.Email, fixed for this listener's lifetime
	OwnerID   string // verified EggIdentity.UserID, also fixed
	Reclaimed bool   // surviving egg has no trusted owner/client binding after restart
}

// NewToolListener creates and starts a tool socket listener.
// sockPath is the path for the Unix socket (e.g. ~/.wingthing/eggs/<session>/tool.sock).
func NewToolListener(sockPath string, tools []*config.ToolConfig, contexts ...ToolContext) (*ToolListener, error) {
	if err := ValidateSocketPath(sockPath); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate tool capability: %w", err)
	}
	capability := hex.EncodeToString(secret)
	return NewToolListenerWithCapability(sockPath, tools, capability, contexts...)
}

// NewToolListenerWithCapability restores authority recovered through an egg's
// authenticated host endpoint. Missing/legacy capabilities must fail closed.
func NewToolListenerWithCapability(sockPath string, tools []*config.ToolConfig, capability string, contexts ...ToolContext) (*ToolListener, error) {
	if err := ValidateSocketPath(sockPath); err != nil {
		return nil, err
	}
	secret, err := hex.DecodeString(capability)
	if err != nil || len(secret) != 32 {
		return nil, fmt.Errorf("invalid egg tool capability")
	}
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale tool socket: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen tool socket: %w", err)
	}
	if err := os.Chmod(sockPath, 0o700); err != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, fmt.Errorf("secure tool socket: %w", err)
	}
	var tc ToolContext
	if len(contexts) > 0 {
		tc = contexts[0]
	}
	tl := &ToolListener{
		capability:  capability,
		owner:       tc.Owner,
		ownerID:     tc.OwnerID,
		runner:      newSessionToolRunner(tools, tc),
		listener:    ln,
		connections: make(chan struct{}, maxConcurrentToolSocketConnections),
	}
	toolSocketCapabilities.Store(config.CanonicalProviderPath(sockPath), tl)
	tl.wg.Add(1)
	go tl.acceptLoop()
	return tl, nil
}

func newSessionToolRunner(tools []*config.ToolConfig, tc ToolContext) *ToolRunner {
	runner := NewToolRunner(tools, tc.Client)
	if tc.Reclaimed {
		runner.contextUnavailable = "Context tools are unavailable in sessions that survived a wing restart; start a new session"
	}
	return runner
}

// ObserveController permanently revokes Context authority when another verified
// user claims input. Returning to the original owner never restores it.
func (tl *ToolListener) ObserveController(userID string) {
	if tl == nil || userID == tl.ownerID {
		return
	}
	tl.runner.mu.Lock()
	tl.runner.contextUnavailable = "Context tools are disabled after another user took control of this session"
	tl.runner.mu.Unlock()
}

// Close stops the listener and waits for in-flight requests to finish.
func (tl *ToolListener) Close() error {
	toolSocketCapabilities.CompareAndDelete(config.CanonicalProviderPath(tl.listener.Addr().String()), tl)
	err := tl.listener.Close()
	tl.wg.Wait()
	return err
}

// Reload replaces the tool configs atomically.
func (tl *ToolListener) Reload(tools []*config.ToolConfig) {
	tl.runner.Reload(tools)
}

func (tl *ToolListener) acceptLoop() {
	defer tl.wg.Done()
	for {
		conn, err := tl.listener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed") {
				log.Printf("tool socket accept: %v", err)
			}
			return
		}
		select {
		case tl.connections <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		tl.wg.Add(1)
		go func() {
			defer tl.wg.Done()
			defer func() { <-tl.connections }()
			tl.handleConn(conn)
		}()
	}
}

func (tl *ToolListener) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		log.Printf("tool socket deadline: %v", err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(conn, maxToolRequestBytes+1))
	if err != nil {
		if writeErr := writeJSON(conn, ToolResponse{Error: "read failed: " + err.Error()}); writeErr != nil {
			log.Printf("tool socket write read error: %v", writeErr)
		}
		return
	}
	if len(data) > maxToolRequestBytes {
		if writeErr := writeJSON(conn, ToolResponse{Error: "tool request too large"}); writeErr != nil {
			log.Printf("tool socket write size error: %v", writeErr)
		}
		return
	}
	var req ToolRequest
	if err := json.Unmarshal(data, &req); err != nil {
		if writeErr := writeJSON(conn, ToolResponse{Error: "invalid JSON: " + err.Error()}); writeErr != nil {
			log.Printf("tool socket write JSON error: %v", writeErr)
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Capability), []byte(tl.capability)) != 1 {
		if err := writeJSON(conn, ToolResponse{Error: "tool authentication failed: missing or invalid egg capability; start a new egg session if this is a legacy egg"}); err != nil {
			log.Printf("tool socket write authentication error: %v", err)
		}
		return
	}
	if req.Action == "list" {
		if err := writeJSON(conn, ToolListResponse{Tools: tl.runner.List()}); err != nil {
			log.Printf("tool socket write list: %v", err)
		}
		return
	}
	if req.Tool == "" {
		if err := writeJSON(conn, ToolResponse{Error: "missing tool name"}); err != nil {
			log.Printf("tool socket write validation error: %v", err)
		}
		return
	}
	// Extend deadline based on tool timeout.
	deadline := 5 * time.Minute
	if tt := tl.runner.TimeoutFor(req.Tool); tt+30*time.Second > deadline {
		deadline = tt + 30*time.Second
	}
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		log.Printf("tool socket extended deadline: %v", err)
		return
	}
	if err := writeJSON(conn, tl.runner.CallAs(req.Tool, req.Args, tl.owner, nil)); err != nil {
		log.Printf("tool socket write response: %v", err)
	}
}

func writeJSON(conn net.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}
