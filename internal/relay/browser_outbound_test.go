package relay

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Block the fake browser's transport, rather than relying on OS buffer sizes.
type blockedBrowserConn struct {
	net.Conn
	blocked   atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	closed    chan struct{}
	enterOnce sync.Once
	closeOnce sync.Once
}

func (c *blockedBrowserConn) Write(p []byte) (int, error) {
	if c.blocked.Load() {
		c.enterOnce.Do(func() { close(c.entered) })
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}
func (c *blockedBrowserConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type browserListener struct {
	net.Listener
	accepted chan *blockedBrowserConn
}

func (l browserListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	b := &blockedBrowserConn{Conn: c, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	l.accepted <- b
	return b, nil
}
func fakeBlockedBrowser(t *testing.T) (*websocket.Conn, *websocket.Conn, *blockedBrowserConn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	transports := make(chan *blockedBrowserConn, 1)
	done := make(chan struct{})
	h := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- c
		<-done
	}))
	h.Listener = browserListener{h.Listener, transports}
	h.Start()
	t.Cleanup(func() { close(done); h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := <-accepted
	transport := <-transports
	t.Cleanup(func() { _ = c.CloseNow(); _ = peer.CloseNow() })
	return c, peer, transport
}

func TestRelayBlockedBrowserDoesNotStallSharedWing(t *testing.T) {
	for _, recipient := range []string{"controller", "viewer", "tunnel"} {
		t.Run(recipient, func(t *testing.T) {
			slow, _, transport := fakeBlockedBrowser(t)
			fast, peer := detachWebSocketPair(t)
			s := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection), tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
			s.Wings.Add(&ConnectedWing{ID: "shared", WingID: "shared"})
			s.trackBrowser(slow, "owner")
			s.trackBrowser(fast, "owner")
			t.Cleanup(func() { s.untrackBrowser(slow); s.untrackBrowser(fast) })
			route := &PTYRoute{WingID: "shared", UserID: "owner", BrowserConn: slow}
			frame := []byte(`{"type":"pty.output","session_id":"slow","data":"blocked"}`)
			if recipient == "viewer" {
				route.BrowserConn = nil
				route.Viewers = map[string]*websocket.Conn{"viewer": slow}
				frame = []byte(`{"type":"pty.output","session_id":"slow","viewer_id":"viewer","data":"blocked"}`)
			}
			s.PTY.Set("slow", route)
			s.PTY.Set("fast", &PTYRoute{WingID: "shared", UserID: "owner", BrowserConn: fast})
			if recipient == "tunnel" && !s.registerTunnelRequest("shared", "request", slow, false) {
				t.Fatal("tunnel registration failed")
			}
			transport.blocked.Store(true)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				// The wing read loop processes these two sessions sequentially.
				if recipient == "tunnel" {
					s.forwardTunnelToBrowser("shared", "request", []byte(`{"type":"tunnel.response","request_id":"request","payload":"blocked"}`), true)
				} else {
					s.forwardPTYToBrowser("slow", "shared", frame)
				}
				s.forwardPTYToBrowser("fast", "shared", []byte(`{"type":"pty.output","session_id":"fast","data":"ready"}`))
			}()
			defer func() { close(transport.release); <-finished }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			select {
			case <-transport.entered:
			case <-ctx.Done():
				t.Fatal("fake browser never blocked")
			}
			start := time.Now()
			readCtx, readCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer readCancel()
			_, data, err := peer.Read(readCtx)
			if err != nil {
				t.Fatalf("second session output did not arrive within 100 ms: %v", err)
			}
			if !strings.Contains(string(data), `"data":"ready"`) {
				t.Fatalf("wrong output: %s", data)
			}
			t.Logf("second session arrived in %s while first browser remained blocked", time.Since(start))
		})
	}
}
