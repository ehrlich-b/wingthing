package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrAuthRejected is returned when the relay rejects the WebSocket handshake with 401.
var ErrAuthRejected = errors.New("relay rejected authentication (401)")

const (
	heartbeatInterval = 30 * time.Second
	writeTimeout      = 10 * time.Second
	maxReconnectDelay = 10 * time.Second
	maxTunnelHandlers = 64
)

// TunnelHandler is called when the wing receives an encrypted tunnel request.
type TunnelHandler func(ctx context.Context, req TunnelRequest, write PTYWriteFunc)

// PTYHandler is called when the wing receives a pty.start request.
// It should spawn the agent in a PTY and manage I/O. The write function
// sends messages back through the relay to the browser. The input channel
// receives raw JSON messages from the browser. Plaintext resize/kill messages
// from the relay are rejected; remote controls arrive through the encrypted
// tunnel, while trusted P2P controls use PushPTYInput.
type PTYHandler func(ctx context.Context, start PTYStart, write PTYWriteFunc, input <-chan []byte)

// Client is an outbound WebSocket client that connects a wing to the roost.
type Client struct {
	RoostURL string // e.g. "wss://ws.wingthing.ai/ws/wing"
	Token    string // device auth token
	WingID   string
	Hostname string // display name (os.Hostname)
	Platform string // runtime.GOOS
	Version  string // build version

	Agents     []string
	Skills     []string
	Labels     []string
	Identities []string
	Projects   []WingProject
	OrgSlug    string
	RootDir    string

	PublicKey string // X25519 identity key (base64)

	Locked       bool
	AllowedCount int
	DirectMCP    bool
	HostedRelay  string

	// RelayPubKey is the relay's EC P-256 public key (base64 DER), received during registration.
	// Used for JWT verification in direct mode.
	RelayPubKey string

	OnPTY               PTYHandler
	OnTunnel            TunnelHandler
	OnOrphanKill        func(ctx context.Context, sessionID string) // kill egg with no active goroutine
	OnReconnect         func(ctx context.Context)                   // called after re-registration with relay
	OnPasskeyRegistered func(msg PasskeyRegistered)                 // called when a user registers a passkey
	OnRegistered        func(msg RegisteredMsg)                     // additive coordinator runtime policy
	OnHostedRelayDenied func(operation string)                      // content-free local policy audit hook
	OnStateChange       func(state string, err error)               // called on connection state transitions

	// ptySessions tracks active PTY sessions for routing input/resize
	ptySessions   map[string]chan []byte // session_id → input channel
	ptySessionsMu sync.Mutex

	conn *websocket.Conn
	mu   sync.Mutex

	configMu sync.RWMutex

	tunnelHandlerOnce sync.Once
	tunnelHandlers    chan struct{}
}

func (c *Client) acquireTunnelHandler() bool {
	c.tunnelHandlerOnce.Do(func() {
		c.tunnelHandlers = make(chan struct{}, maxTunnelHandlers)
	})
	select {
	case c.tunnelHandlers <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *Client) releaseTunnelHandler() {
	<-c.tunnelHandlers
}

type clientRuntimeConfig struct {
	Labels         []string
	RootDir        string
	Locked         bool
	AllowedCount   int
	DirectMCP      bool
	HostedRelay    string
	RelayPublicKey string
}

func (c *Client) runtimeConfig() clientRuntimeConfig {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return clientRuntimeConfig{
		Labels: append([]string(nil), c.Labels...), RootDir: c.RootDir,
		Locked: c.Locked, AllowedCount: c.AllowedCount,
		DirectMCP: c.DirectMCP, HostedRelay: c.HostedRelay,
		RelayPublicKey: c.RelayPubKey,
	}
}

// UpdateRuntimeConfig atomically publishes the hot-reloadable registration
// fields so reconnect and config-push goroutines never observe a torn policy.
func (c *Client) UpdateRuntimeConfig(locked bool, allowedCount int, directMCP bool, labels []string, rootDir string) {
	c.configMu.Lock()
	c.Locked = locked
	c.AllowedCount = allowedCount
	c.DirectMCP = directMCP
	c.Labels = append([]string(nil), labels...)
	c.RootDir = rootDir
	c.configMu.Unlock()
}

// UpdateAccessConfig publishes lock/allowlist changes made by a tunnel request.
func (c *Client) UpdateAccessConfig(locked bool, allowedCount int) {
	c.configMu.Lock()
	c.Locked = locked
	c.AllowedCount = allowedCount
	c.configMu.Unlock()
}

// RelayPublicKey returns the last coordinator signing key received at registration.
func (c *Client) RelayPublicKey() string { return c.runtimeConfig().RelayPublicKey }

// Run connects to the relay and processes tasks until ctx is cancelled.
// Automatically reconnects on disconnect with exponential backoff.
// Returns ErrAuthRejected if the relay rejects the token with 401.
func (c *Client) Run(ctx context.Context) error {
	c.notifyState("connecting", nil)
	delay := time.Second
	for {
		connected, err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			c.notifyState("disconnected", ctx.Err())
			return ctx.Err()
		}
		if errors.Is(err, ErrAuthRejected) {
			c.notifyState("auth_failed", err)
			return ErrAuthRejected
		}
		if connected {
			// Was connected successfully — reset backoff
			delay = time.Second
		}
		c.notifyState("disconnected", err)
		log.Printf("relay disconnected: %v — reconnecting in %s", err, delay)
		select {
		case <-ctx.Done():
			c.notifyState("disconnected", ctx.Err())
			return ctx.Err()
		case <-time.After(delay):
		}
		c.notifyState("connecting", nil)
		delay *= 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (c *Client) notifyState(state string, err error) {
	if c.OnStateChange != nil {
		c.OnStateChange(state, err)
	}
}

func (c *Client) connectAndServe(ctx context.Context) (connected bool, err error) {
	opts := &websocket.DialOptions{
		HTTPHeader: make(map[string][]string),
	}
	opts.HTTPHeader.Set("Authorization", "Bearer "+c.Token)

	conn, response, dialErr := websocket.Dial(ctx, c.RoostURL, opts)
	if dialErr != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return false, fmt.Errorf("%w: %v", ErrAuthRejected, dialErr)
		}
		return false, fmt.Errorf("dial: %w", dialErr)
	}
	conn.SetReadLimit(512 * 1024) // 512KB — match relay limit
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() { _ = conn.CloseNow() }()
	connected = true

	// Preserve PTY sessions across reconnects — running processes survive relay outages.
	// Only initialize the map on first connect.
	c.ptySessionsMu.Lock()
	if c.ptySessions == nil {
		c.ptySessions = make(map[string]chan []byte)
	}
	c.ptySessionsMu.Unlock()

	// Send registration — projects flow through E2E tunnel only, never through relay
	runtimeConfig := c.runtimeConfig()
	reg := WingRegister{
		Type:           TypeWingRegister,
		WingID:         c.WingID,
		Hostname:       c.Hostname,
		Platform:       c.Platform,
		Version:        c.Version,
		Agents:         c.Agents,
		Skills:         c.Skills,
		Labels:         runtimeConfig.Labels,
		Identities:     c.Identities,
		Projects:       nil,
		OrgSlug:        c.OrgSlug,
		RootDir:        runtimeConfig.RootDir,
		PublicKey:      c.PublicKey,
		Locked:         runtimeConfig.Locked,
		AllowedCount:   runtimeConfig.AllowedCount,
		PurposeBinding: true,
		DirectMCP:      runtimeConfig.DirectMCP,
		HostedRelay:    runtimeConfig.HostedRelay,
	}
	if err := c.writeJSON(ctx, reg); err != nil {
		return connected, fmt.Errorf("register: %w", err)
	}

	// Start heartbeat
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go c.heartbeatLoop(hbCtx)

	// Read loop
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return connected, fmt.Errorf("read: %w", err)
		}

		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Printf("bad message: %v", err)
			continue
		}
		if denied := c.hostedRelayDenial(data, env.Type); denied != nil {
			log.Printf("[audit] hosted_relay_denied operation=%s policy=deny", env.Type)
			if c.OnHostedRelayDenied != nil {
				c.OnHostedRelayDenied(env.Type)
			}
			if err := c.writeJSON(ctx, *denied); err != nil {
				return connected, fmt.Errorf("write hosted relay denial: %w", err)
			}
			continue
		}

		switch env.Type {
		case TypeRegistered:
			var msg RegisteredMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("bad registered message: %v", err)
				continue
			}
			if msg.RelayPubKey != "" {
				c.configMu.Lock()
				c.RelayPubKey = msg.RelayPubKey
				c.configMu.Unlock()
			}
			if c.OnRegistered != nil {
				c.OnRegistered(msg)
			}
			log.Printf("registered with relay as wing %s", msg.WingID)
			c.notifyState("connected", nil)
			if c.OnReconnect != nil {
				go c.OnReconnect(ctx)
			}

		case TypePTYStart:
			var start PTYStart
			if err := json.Unmarshal(data, &start); err != nil {
				log.Printf("bad pty.start: %v", err)
				continue
			}
			if !ValidSessionID(start.SessionID) {
				log.Printf("rejected pty.start with invalid session ID %q", start.SessionID)
				if err := c.writeJSON(ctx, ErrorMsg{Type: TypeError, Message: "invalid session ID"}); err != nil {
					return connected, fmt.Errorf("report invalid PTY session ID: %w", err)
				}
				continue
			}
			if c.OnPTY != nil {
				inputCh := make(chan []byte, 64)
				if !c.registerPTYSession(start.SessionID, inputCh) {
					log.Printf("rejected duplicate pty.start for active session %s", start.SessionID)
					if err := c.writeJSON(ctx, ErrorMsg{Type: TypeError, SessionID: start.SessionID, Message: "session is already active"}); err != nil {
						return connected, fmt.Errorf("report duplicate PTY session: %w", err)
					}
					continue
				}
				go func() {
					defer c.unregisterPTYSession(start.SessionID, inputCh)
					c.OnPTY(ctx, start, func(v any) error {
						return c.writeJSON(ctx, v)
					}, inputCh)
				}()
			}

		case TypePTYAttach:
			// Forward attach to the existing session for re-key and local auth.
			var partial struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &partial); err != nil {
				continue
			}
			if !ValidSessionID(partial.SessionID) {
				continue
			}
			c.ptySessionsMu.Lock()
			ch := c.ptySessions[partial.SessionID]
			c.ptySessionsMu.Unlock()
			if ch != nil {
				select {
				case ch <- data:
				default:
				}
			}

		case TypePTYInput, TypePTYAttentionAck, TypePasskeyResponse, TypePTYMigrate:
			var partial struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &partial); err != nil {
				continue
			}
			if !ValidSessionID(partial.SessionID) {
				continue
			}
			c.ptySessionsMu.Lock()
			ch := c.ptySessions[partial.SessionID]
			c.ptySessionsMu.Unlock()
			if ch != nil {
				select {
				case ch <- data:
				default:
				}
			}

		case TypePTYResize, TypePTYKill:
			log.Printf("rejected plaintext %s from relay; encrypted tunnel control required", env.Type)

		case TypeTunnelRequest:
			var req TunnelRequest
			if err := json.Unmarshal(data, &req); err != nil {
				log.Printf("bad tunnel.req: %v", err)
				continue
			}
			if c.OnTunnel != nil {
				if !c.acquireTunnelHandler() {
					if err := c.writeJSON(ctx, ErrorMsg{
						Type: TypeError, RequestID: req.RequestID,
						Message: "wing has too many concurrent control requests",
					}); err != nil {
						return connected, fmt.Errorf("report tunnel concurrency limit: %w", err)
					}
					continue
				}
				go func() {
					defer c.releaseTunnelHandler()
					c.OnTunnel(ctx, req, func(v any) error {
						return c.writeJSON(ctx, v)
					})
				}()
			}

		case TypePasskeyRegistered:
			var msg PasskeyRegistered
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("bad passkey.registered: %v", err)
				continue
			}
			log.Printf("passkey.registered: user %s (%s) registered a passkey", msg.UserID, msg.Email)
			if c.OnPasskeyRegistered != nil {
				go c.OnPasskeyRegistered(msg)
			}

		case TypeError:
			var msg ErrorMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("bad relay error message: %v", err)
				continue
			}
			log.Printf("relay error: %s", msg.Message)

		default:
			log.Printf("unknown message type: %s", env.Type)
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := WingHeartbeat{Type: TypeWingHeartbeat, WingID: c.WingID}
			if err := c.writeJSON(ctx, hb); err != nil {
				return
			}
		}
	}
}

func (c *Client) hostedRelayDenial(data []byte, messageType string) *ErrorMsg {
	if HostedRelayAllowed(c.runtimeConfig().HostedRelay) {
		return nil
	}
	denied := ErrorMsg{Type: TypeError, Message: "hosted relay payload transport is disabled by this wing"}
	switch messageType {
	case TypePTYStart, TypePTYAttach, TypePTYInput, TypePTYResize, TypePTYKill,
		TypePTYAttentionAck, TypePTYMigrate, TypePasskeyResponse:
		var metadata struct {
			SessionID string `json:"session_id"`
			ViewerID  string `json:"viewer_id"`
		}
		_ = json.Unmarshal(data, &metadata)
		denied.SessionID = metadata.SessionID
		denied.ViewerID = metadata.ViewerID
		return &denied
	case TypeTunnelRequest:
		var req TunnelRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return &denied
		}
		if IsCoordinationTunnelPurpose(req.Purpose) && len(req.Payload) <= MaxCoordinationTunnelPayload {
			return nil
		}
		denied.RequestID = req.RequestID
		return &denied
	default:
		return nil
	}
}

// SendConfig pushes the wing's current lock state to the relay.
func (c *Client) SendConfig(ctx context.Context) error {
	runtimeConfig := c.runtimeConfig()
	return c.writeJSON(ctx, WingConfig{
		Type:         TypeWingConfig,
		WingID:       c.WingID,
		Locked:       runtimeConfig.Locked,
		AllowedCount: runtimeConfig.AllowedCount,
		DirectMCP:    runtimeConfig.DirectMCP,
		HostedRelay:  runtimeConfig.HostedRelay,
	})
}

// SendAttention sends a session.attention message to the relay (bell detected).
func (c *Client) SendAttention(ctx context.Context, sessionID string) error {
	if !HostedRelayAllowed(c.runtimeConfig().HostedRelay) {
		log.Printf("[audit] hosted_relay_denied operation=%s policy=deny", TypeSessionAttention)
		if c.OnHostedRelayDenied != nil {
			c.OnHostedRelayDenied(TypeSessionAttention)
		}
		return nil
	}
	return c.writeJSON(ctx, SessionAttention{Type: TypeSessionAttention, SessionID: sessionID})
}

// HasPTYSession returns true if a goroutine is already handling this session.
func (c *Client) HasPTYSession(sessionID string) bool {
	c.ptySessionsMu.Lock()
	defer c.ptySessionsMu.Unlock()
	_, ok := c.ptySessions[sessionID]
	return ok
}

// RegisterPTYSession creates an input channel for a reclaimed session so allowed
// relay messages and trusted P2P controls can be routed to it. Plaintext relay
// resize/kill messages are rejected in connectAndServe before reaching this channel.
// Returns the input channel and a write function.
// The caller must start a goroutine to handle the session and clean up when done.
func (c *Client) RegisterPTYSession(ctx context.Context, sessionID string) (write PTYWriteFunc, input <-chan []byte, cleanup func(), registered bool) {
	if !ValidSessionID(sessionID) {
		log.Printf("refusing to register invalid PTY session ID %q", sessionID)
		closed := make(chan []byte)
		close(closed)
		return func(any) error { return fmt.Errorf("invalid session ID") }, closed, func() {}, false
	}
	inputCh := make(chan []byte, 64)
	if !c.registerPTYSession(sessionID, inputCh) {
		closed := make(chan []byte)
		close(closed)
		return func(any) error { return fmt.Errorf("session is already active") }, closed, func() {}, false
	}

	writeFn := func(v any) error {
		return c.writeJSON(ctx, v)
	}
	cleanupFn := func() {
		c.unregisterPTYSession(sessionID, inputCh)
	}
	return writeFn, inputCh, cleanupFn, true
}

func (c *Client) registerPTYSession(sessionID string, inputCh chan []byte) bool {
	c.ptySessionsMu.Lock()
	defer c.ptySessionsMu.Unlock()
	if c.ptySessions == nil {
		c.ptySessions = make(map[string]chan []byte)
	}
	if c.ptySessions[sessionID] != nil {
		return false
	}
	c.ptySessions[sessionID] = inputCh
	return true
}

func (c *Client) unregisterPTYSession(sessionID string, inputCh chan []byte) {
	c.ptySessionsMu.Lock()
	defer c.ptySessionsMu.Unlock()
	if c.ptySessions[sessionID] == inputCh {
		delete(c.ptySessions, sessionID)
	}
}

// PushPTYInput pushes raw data into a session's input channel from outside the WebSocket read loop.
// Used by P2P DataChannels to route messages into the session handler.
// Returns true if the session exists and the message was delivered.
func (c *Client) PushPTYInput(sessionID string, data []byte) bool {
	if !ValidSessionID(sessionID) {
		log.Printf("[P2P] PushPTYInput: invalid session ID %q", sessionID)
		return false
	}
	c.ptySessionsMu.Lock()
	ch := c.ptySessions[sessionID]
	c.ptySessionsMu.Unlock()
	if ch == nil {
		log.Printf("[P2P] PushPTYInput: no session %s", sessionID)
		return false
	}
	select {
	case ch <- data:
		return true
	default:
		log.Printf("[P2P] PushPTYInput: input buffer full for session %s, dropping %d bytes", sessionID, len(data))
		return false
	}
}

func (c *Client) writeJSON(ctx context.Context, v any) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
