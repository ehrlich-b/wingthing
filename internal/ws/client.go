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

// TaskHandler is called when the wing receives a task to execute.
type TaskHandler func(ctx context.Context, task TaskSubmit) (output string, err error)

// ChunkSender sends a text chunk back to the relay for a running task.
type ChunkSender func(taskID, text string)

// TaskHandlerWithChunks is called when the wing receives a task, with a chunk sender for streaming.
type TaskHandlerWithChunks func(ctx context.Context, task TaskSubmit, send ChunkSender) (output string, err error)

// PTYWriteFunc sends a message back to the relay over the wing's WebSocket.
type PTYWriteFunc func(v any) error

// PTYHandler is called when the wing receives a pty.start request.
// It should spawn the agent in a PTY and manage I/O. The write function
// sends messages back through the relay to the browser. The input channel
// receives raw JSON messages (pty.input and pty.resize) from the browser.
type PTYHandler func(ctx context.Context, start PTYStart, write PTYWriteFunc, input <-chan []byte)

// Client is an outbound WebSocket client that connects a wing to the relay.
type Client struct {
	RelayURL  string // e.g. "wss://ws.wingthing.ai/ws/wing"
	Token     string // device auth token
	MachineID string

	Agents     []string
	Skills     []string
	Labels     []string
	Identities []string

	OnTask TaskHandlerWithChunks
	OnPTY  PTYHandler

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

	conn, _, dialErr := websocket.Dial(ctx, c.RelayURL, opts)
	if dialErr != nil {
		return false, fmt.Errorf("dial: %w", dialErr)
	}
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

	// Send registration
	reg := WingRegister{
		Type:       TypeWingRegister,
		MachineID:  c.MachineID,
		Agents:     c.Agents,
		Skills:     c.Skills,
		Labels:     c.Labels,
		Identities: c.Identities,
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

		case TypeTaskSubmit:
			var task TaskSubmit
			if err := json.Unmarshal(data, &task); err != nil {
				log.Printf("bad task: %v", err)
				continue
			}
			go c.handleTask(ctx, task)

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

		case TypeError:
			var msg ErrorMsg
			json.Unmarshal(data, &msg)
			log.Printf("relay error: %s", msg.Message)

		default:
			log.Printf("unknown message type: %s", env.Type)
		}
	}
}

func (c *Client) handleTask(ctx context.Context, task TaskSubmit) {
	if c.OnTask == nil {
		c.sendError(ctx, task.TaskID, "wing has no task handler")
		return
	}

	sender := func(taskID, text string) {
		chunk := TaskChunk{Type: TypeTaskChunk, TaskID: taskID, Text: text}
		c.writeJSON(ctx, chunk)
	}

	_, err := c.OnTask(ctx, task, sender)
	if err != nil {
		c.sendError(ctx, task.TaskID, err.Error())
		return
	}

	done := TaskDone{Type: TypeTaskDone, TaskID: task.TaskID}
	c.writeJSON(ctx, done)
}

func (c *Client) sendError(ctx context.Context, taskID, msg string) {
	errMsg := TaskErrorMsg{Type: TypeTaskError, TaskID: taskID, Error: msg}
	c.writeJSON(ctx, errMsg)
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := WingHeartbeat{Type: TypeWingHeartbeat, MachineID: c.MachineID}
			if err := c.writeJSON(ctx, hb); err != nil {
				return
			}
		}
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
