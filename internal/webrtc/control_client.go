package webrtc

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/pion/webrtc/v4"
)

// ControlClient is the initiating half of Wingthing's direct MCP transport.
// Signaling is performed by the caller; operation payloads use the DataChannel.
type ControlClient struct {
	pc      *webrtc.PeerConnection
	dc      *webrtc.DataChannel
	ready   chan struct{}
	readyMu sync.Once
	done    chan struct{}
	doneMu  sync.Once

	mu      sync.Mutex
	pending map[string]chan control.DirectResponse
	closed  error
}

// NewControlClient creates a host/LAN-capable peer unless ICE servers are
// supplied by the selected wing's advertised connection metadata.
func NewControlClient(actor string, iceServers []webrtc.ICEServer) (*ControlClient, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, fmt.Errorf("new peer connection: %w", err)
	}
	dc, err := pc.CreateDataChannel(control.DirectChannelPrefix+actor, nil)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create control channel: %w", err)
	}
	client := &ControlClient{
		pc: pc, dc: dc, ready: make(chan struct{}), done: make(chan struct{}),
		pending: make(map[string]chan control.DirectResponse),
	}
	dc.OnOpen(func() { client.readyMu.Do(func() { close(client.ready) }) })
	dc.OnClose(func() { client.fail(fmt.Errorf("direct control channel closed")) })
	dc.OnMessage(func(message webrtc.DataChannelMessage) {
		var response control.DirectResponse
		if json.Unmarshal(message.Data, &response) != nil || response.ID == "" {
			return
		}
		client.mu.Lock()
		waiter := client.pending[response.ID]
		client.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- response:
			default:
			}
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			client.fail(fmt.Errorf("peer connection %s", state.String()))
		}
	})
	return client, nil
}

// Offer returns a complete SDP offer, including gathered ICE candidates.
func (c *ControlClient) Offer(ctx context.Context) (string, error) {
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("create offer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-gathered:
	}
	if c.pc.LocalDescription() == nil {
		return "", fmt.Errorf("no local description after ICE gathering")
	}
	return c.pc.LocalDescription().SDP, nil
}

// AcceptAnswer completes signaling with the selected wing.
func (c *ControlClient) AcceptAnswer(sdp string) error {
	return c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// WaitReady waits for the direct data channel to open.
func (c *ControlClient) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		c.mu.Lock()
		err := c.closed
		c.mu.Unlock()
		return err
	}
}

// Call sends one control operation and waits for its correlated response.
func (c *ControlClient) Call(ctx context.Context, tool string, arguments json.RawMessage) (map[string]any, bool, error) {
	id := randomControlID()
	waiter := make(chan control.DirectResponse, 1)
	c.mu.Lock()
	if c.closed != nil {
		err := c.closed
		c.mu.Unlock()
		return nil, true, err
	}
	c.pending[id] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(control.DirectRequest{
		Version: control.ContractVersion, ID: id, Tool: tool, Arguments: arguments,
	})
	if err != nil {
		return nil, true, err
	}
	if err := c.dc.Send(payload); err != nil {
		return nil, true, fmt.Errorf("send direct control request: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, true, ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.closed
		c.mu.Unlock()
		return nil, true, err
	case response := <-waiter:
		if response.Error != "" {
			return response.Result, true, fmt.Errorf("direct control: %s", response.Error)
		}
		return response.Result, response.IsError, nil
	}
}

func (c *ControlClient) fail(err error) {
	c.mu.Lock()
	if c.closed == nil {
		c.closed = err
	}
	c.mu.Unlock()
	c.doneMu.Do(func() { close(c.done) })
	c.readyMu.Do(func() { close(c.ready) })
}

// Closed reports whether the transport has permanently failed or been closed.
// Callers may use this to evict a cached client, but must not automatically
// replay a mutating request whose response may have been lost.
func (c *ControlClient) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed != nil
}

// Close releases the peer connection and all of its data channels.
func (c *ControlClient) Close() error {
	c.fail(fmt.Errorf("peer connection closed"))
	return c.pc.Close()
}

func randomControlID() string {
	value := make([]byte, 16)
	_, _ = crand.Read(value)
	return hex.EncodeToString(value)
}
