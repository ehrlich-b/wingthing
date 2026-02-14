package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	heartbeatInterval = 30 * time.Second
	writeTimeout      = 10 * time.Second
	maxReconnectDelay = 10 * time.Second
)

// TunnelHandler is called when the wing receives an encrypted tunnel request.
type TunnelHandler func(ctx context.Context, req TunnelRequest, write PTYWriteFunc)

// PTYHandler is called when the wing receives a pty.start request.
// It should spawn the agent in a PTY and manage I/O. The write function
// sends messages back through the relay to the browser. The input channel
// receives raw JSON messages (pty.input and pty.resize) from the browser.
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

	Pinned      bool
	PinnedCount int

	OnPTY         PTYHandler
	OnTunnel      TunnelHandler
	OnOrphanKill  func(ctx context.Context, sessionID string) // kill egg with no active goroutine
	OnReconnect   func(ctx context.Context)                   // called after re-registration with relay
	SessionLister func(ctx context.Context) []SessionInfo

	// ptySessions tracks active PTY sessions for routing input/resize
	ptySessions   map[string]chan []byte // session_id → input channel
	ptySessionsMu sync.Mutex

	conn *websocket.Conn
	mu   sync.Mutex
}

// Run connects to the relay and processes tasks until ctx is cancelled.
// Automatically reconnects on disconnect with exponential backoff.
func (c *Client) Run(ctx context.Context) error {
	delay := time.Second
	for {
		connected, err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			// Was connected successfully — reset backoff
			delay = time.Second
		}
		log.Printf("relay disconnected: %v — reconnecting in %s", err, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (c *Client) connectAndServe(ctx context.Context) (connected bool, err error) {
	opts := &websocket.DialOptions{
		HTTPHeader: make(map[string][]string),
	}
	opts.HTTPHeader.Set("Authorization", "Bearer "+c.Token)

	conn, _, dialErr := websocket.Dial(ctx, c.RoostURL, opts)
	if dialErr != nil {
		return false, fmt.Errorf("dial: %w", dialErr)
	}
	conn.SetReadLimit(512 * 1024) // 512KB — match relay limit
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer conn.CloseNow()
	connected = true

	// Preserve PTY sessions across reconnects — running processes survive relay outages.
	// Only initialize the map on first connect.
	c.ptySessionsMu.Lock()
	if c.ptySessions == nil {
		c.ptySessions = make(map[string]chan []byte)
	}
	c.ptySessionsMu.Unlock()

	// Send registration — projects flow through E2E tunnel only, never through relay
	reg := WingRegister{
		Type:        TypeWingRegister,
		WingID:      c.WingID,
		Hostname:    c.Hostname,
		Platform:    c.Platform,
		Version:     c.Version,
		Agents:      c.Agents,
		Skills:      c.Skills,
		Labels:      c.Labels,
		Identities:  c.Identities,
		Projects:    nil,
		OrgSlug:     c.OrgSlug,
		RootDir:     c.RootDir,
		Pinned:      c.Pinned,
		PinnedCount: c.PinnedCount,
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

		switch env.Type {
		case TypeRegistered:
			var msg RegisteredMsg
			json.Unmarshal(data, &msg)
			log.Printf("registered with relay as wing %s", msg.WingID)
			if c.OnReconnect != nil {
				go c.OnReconnect(ctx)
			}

		case TypePTYStart:
			var start PTYStart
			if err := json.Unmarshal(data, &start); err != nil {
				log.Printf("bad pty.start: %v", err)
				continue
			}
			if c.OnPTY != nil {
				inputCh := make(chan []byte, 64)
				c.ptySessionsMu.Lock()
				c.ptySessions[start.SessionID] = inputCh
				c.ptySessionsMu.Unlock()
				go func() {
					defer func() {
						c.ptySessionsMu.Lock()
						delete(c.ptySessions, start.SessionID)
						c.ptySessionsMu.Unlock()
					}()
					c.OnPTY(ctx, start, func(v any) error {
						return c.writeJSON(ctx, v)
					}, inputCh)
				}()
			}

		case TypePTYAttach, TypePTYKill:
			// Forward to existing session (attach for re-key, kill to terminate)
			var partial struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &partial); err != nil {
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
			} else if env.Type == TypePTYKill && c.OnOrphanKill != nil {
				go c.OnOrphanKill(ctx, partial.SessionID)
			}

		case TypePTYInput, TypePTYResize:
			var partial struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(data, &partial); err != nil {
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

		case TypeSessionsList:
			var req SessionsList
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			go func() {
				var sessions []SessionInfo
				if c.SessionLister != nil {
					sessions = c.SessionLister(ctx)
				}
				if sessions == nil {
					sessions = []SessionInfo{}
				}
				c.writeJSON(ctx, SessionsSync{
					Type:      TypeSessionsSync,
					RequestID: req.RequestID,
					Sessions:  sessions,
				})
			}()

		case TypeTunnelRequest:
			var req TunnelRequest
			if err := json.Unmarshal(data, &req); err != nil {
				log.Printf("bad tunnel.req: %v", err)
				continue
			}
			if c.OnTunnel != nil {
				go c.OnTunnel(ctx, req, func(v any) error {
					return c.writeJSON(ctx, v)
				})
			}

		case TypeError:
			var msg ErrorMsg
			json.Unmarshal(data, &msg)
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
			// Piggyback session list on heartbeat
			if c.SessionLister != nil {
				sessions := c.SessionLister(ctx)
				if sessions == nil {
					sessions = []SessionInfo{}
				}
				c.writeJSON(ctx, SessionsSync{
					Type:     TypeSessionsSync,
					Sessions: sessions,
				})
			}
		}
	}
}

// SendConfig pushes the wing's current pinned state to the relay.
func (c *Client) SendConfig(ctx context.Context) error {
	return c.writeJSON(ctx, WingConfig{
		Type:        TypeWingConfig,
		WingID:      c.WingID,
		Pinned:      c.Pinned,
		PinnedCount: c.PinnedCount,
	})
}

// SendReclaim sends a pty.reclaim message to the relay for a surviving session.
func (c *Client) SendReclaim(ctx context.Context, sessionID, agent, cwd string) error {
	return c.writeJSON(ctx, PTYReclaim{Type: TypePTYReclaim, SessionID: sessionID, Agent: agent, CWD: cwd})
}

// HasPTYSession returns true if a goroutine is already handling this session.
func (c *Client) HasPTYSession(sessionID string) bool {
	c.ptySessionsMu.Lock()
	defer c.ptySessionsMu.Unlock()
	_, ok := c.ptySessions[sessionID]
	return ok
}

// RegisterPTYSession creates an input channel for a reclaimed session so pty.attach/input/resize/kill
// messages from the relay get routed to it. Returns the input channel and a write function.
// The caller must start a goroutine to handle the session and clean up when done.
func (c *Client) RegisterPTYSession(ctx context.Context, sessionID string) (write PTYWriteFunc, input <-chan []byte, cleanup func()) {
	inputCh := make(chan []byte, 64)
	c.ptySessionsMu.Lock()
	c.ptySessions[sessionID] = inputCh
	c.ptySessionsMu.Unlock()

	writeFn := func(v any) error {
		return c.writeJSON(ctx, v)
	}
	cleanupFn := func() {
		c.ptySessionsMu.Lock()
		delete(c.ptySessions, sessionID)
		c.ptySessionsMu.Unlock()
	}
	return writeFn, inputCh, cleanupFn
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
