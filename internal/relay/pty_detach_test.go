package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func detachWebSocketPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		accepted <- conn
		<-done
	}))
	t.Cleanup(func() { close(done); httpServer.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case server = <-accepted:
	case <-ctx.Done():
		t.Fatal("websocket was not accepted")
	}
	t.Cleanup(func() { _ = server.CloseNow(); _ = client.CloseNow() })
	return server, client
}

func TestBrowserOutboundFailureForwardsDetachBeforeClearingController(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	for _, failure := range []string{"write", "bandwidth"} {
		t.Run(failure, func(t *testing.T) {
			browser, browserPeer := detachWebSocketPair(t)
			wing, wingPeer := detachWebSocketPair(t)
			server := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
			server.Wings.wings["wing"] = &ConnectedWing{WingID: "wing", Conn: wing}
			server.trackBrowser(browser, "owner")
			route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser, ControllerID: "controller-1"}
			server.PTY.Set("session", route)
			if failure == "write" {
				_ = browser.CloseNow()
			} else {
				server.Bandwidth = NewBandwidthMeter(1000, 1000, nil)
				server.Bandwidth.SetExceeded([]string{"owner"})
				// Read the denial and close handshake so bandwidth cleanup
				// finishes without waiting for its network timeout.
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					for {
						if _, _, err := browserPeer.Read(ctx); err != nil {
							return
						}
					}
				}()
			}
			server.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.output","session_id":"session","data":"a"}`))
			if route.BrowserConn != nil {
				t.Fatal("failed browser remained connected")
			}
			// The normal disconnect cleanup now has no controller to capture.
			if captured := server.PTY.browserDetachRoutes(browser, ""); len(captured) != 0 {
				t.Fatalf("controller was not cleared: %v", captured)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, data, err := wingPeer.Read(ctx)
			if err != nil {
				t.Fatalf("wing never received writer detach: %v", err)
			}
			var detached ws.PTYDetach
			if err := json.Unmarshal(data, &detached); err != nil {
				t.Fatal(err)
			}
			if detached.Type != ws.TypePTYDetach || detached.SessionID != "session" || detached.ControllerID != "controller-1" || detached.ViewerID != "" {
				t.Fatalf("wrong attachment released: %+v", detached)
			}
		})
	}
}

func TestBrowserDetachCarriesExactControllerAndViewerIDs(t *testing.T) {
	routes := NewPTYRoutes()
	old, pending, viewer, stranger := &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}
	routes.Set("session", &PTYRoute{WingID: "wing", BrowserConn: old, ControllerID: "old", PendingController: pending, PendingControllerID: "new", Viewers: map[string]*websocket.Conn{"viewer": viewer}})
	captured := routes.browserDetachRoutes(old, "session")
	if len(captured) != 1 || captured[0].message.ControllerID != "old" || captured[0].wingID != "wing" {
		t.Fatalf("old disconnect capture: %v", captured)
	}
	if got := routes.browserDetachRoutes(pending, ""); len(got) != 1 || got[0].message.ControllerID != "new" {
		t.Fatalf("pending disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(viewer, ""); len(got) != 1 || got[0].message.ViewerID != "viewer" || got[0].message.ControllerID != "" {
		t.Fatalf("observer disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(stranger, ""); len(got) != 0 {
		t.Fatalf("foreign disconnect: %v", got)
	}
	if got := routes.browserDetachRoutes(old, "different-session"); len(got) != 0 {
		t.Fatalf("wrong target disconnected: %v", got)
	}
	// Simulate promotion between capture and network delivery. The queued old
	// detach retains its old ID, and clearing old connection cannot remove new.
	route := routes.Get("session")
	route.mu.Lock()
	route.BrowserConn = pending
	route.ControllerID = "new"
	route.PendingController = nil
	route.PendingControllerID = ""
	route.mu.Unlock()
	routes.ClearBrowser(old)
	if captured[0].message.ControllerID != "old" || routes.Get("session").BrowserConn != pending {
		t.Fatal("old close displaced replacement")
	}
}
