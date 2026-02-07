package ws

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const (
	maxOutbox     = 1000
	pingInterval  = 30 * time.Second
	writeTimeout  = 10 * time.Second
	authTimeout   = 10 * time.Second
)

type ClientState int

const (
	StateDisconnected ClientState = iota
	StateConnecting
	StateConnected
	StateAuthenticated
	StateReady
)

type Client struct {
	URL         string
	DeviceToken string
	OnMessage   func(*Message)

	conn   *websocket.Conn
	state  ClientState
	mu     sync.Mutex
	outbox []*Message
	done   chan struct{}
}

func NewClient(url, deviceToken string) *Client {
	return &Client{
		URL:         url,
		DeviceToken: deviceToken,
		state:       StateDisconnected,
		done:        make(chan struct{}),
	}
}

func (c *Client) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Client) setState(s ClientState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = s
}

func (c *Client) Send(msg *Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state < StateConnected || c.conn == nil {
		if len(c.outbox) >= maxOutbox {
			c.outbox = c.outbox[1:]
		}
		c.outbox = append(c.outbox, msg)
		return nil
	}

	return c.sendLocked(msg)
}

func (c *Client) sendLocked(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *Client) Connect(ctx context.Context) error {
	c.setState(StateConnecting)

	conn, _, err := websocket.Dial(ctx, c.URL, nil)
	if err != nil {
		c.setState(StateDisconnected)
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.state = StateConnected
	c.mu.Unlock()

	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.state = StateDisconnected
	c.mu.Unlock()

	select {
	case <-c.done:
	default:
		close(c.done)
	}

	if conn != nil {
		return conn.Close(websocket.StatusNormalClosure, "closing")
	}
	return nil
}

func (c *Client) Run(ctx context.Context) error {
	bo := NewBackoff(time.Second, 60*time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return nil
		default:
		}

		err := c.Connect(ctx)
		if err != nil {
			wait := bo.Next()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.done:
				return nil
			case <-time.After(wait):
				continue
			}
		}

		bo.Reset()

		if err := c.authenticate(ctx); err != nil {
			c.closeConn()
			continue
		}

		c.flushOutbox()
		c.readLoop(ctx)
		c.closeConn()
	}
}

func (c *Client) authenticate(ctx context.Context) error {
	msg, err := NewMessage(MsgAuth, AuthPayload{DeviceToken: c.DeviceToken})
	if err != nil {
		return err
	}

	c.mu.Lock()
	err = c.sendLocked(msg)
	c.mu.Unlock()
	if err != nil {
		return err
	}

	authCtx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()

	_, data, err := c.conn.Read(authCtx)
	if err != nil {
		return err
	}

	var reply Message
	if err := json.Unmarshal(data, &reply); err != nil {
		return err
	}

	if reply.Type != MsgAuthResult {
		return &ErrorPayload{Code: "unexpected_type", Message: "expected auth_result"}
	}

	var result AuthResultPayload
	if err := reply.ParsePayload(&result); err != nil {
		return err
	}

	if !result.Success {
		return &ErrorPayload{Code: "auth_failed", Message: result.Error}
	}

	c.setState(StateAuthenticated)
	c.setState(StateReady)
	return nil
}

func (c *Client) flushOutbox() {
	c.mu.Lock()
	defer c.mu.Unlock()

	flushed := 0
	for _, msg := range c.outbox {
		if err := c.sendLocked(msg); err != nil {
			break
		}
		flushed++
	}
	c.outbox = c.outbox[flushed:]
}

func (c *Client) readLoop(ctx context.Context) {
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	readCh := make(chan *Message)
	errCh := make(chan error, 1)

	go func() {
		for {
			_, data, err := c.conn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			readCh <- &msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-errCh:
			return
		case msg := <-readCh:
			if msg.Type == MsgPing {
				pong, err := NewMessage(MsgPong, nil)
				if err == nil {
					c.mu.Lock()
					_ = c.sendLocked(pong)
					c.mu.Unlock()
				}
				continue
			}
			if c.OnMessage != nil {
				c.OnMessage(msg)
			}
		case <-pingTicker.C:
			ping, err := NewMessage(MsgPing, nil)
			if err != nil {
				continue
			}
			c.mu.Lock()
			err = c.sendLocked(ping)
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *Client) closeConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close(websocket.StatusNormalClosure, "reconnecting")
		c.conn = nil
	}
	c.state = StateDisconnected
}

func (e *ErrorPayload) Error() string {
	return e.Code + ": " + e.Message
}
