package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRelayBrowserOverflowDetachesOnceAndCanReattach(t *testing.T) {
	oldChannel := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	defer func() { config.ReleaseChannel = oldChannel }()
	for _, limit := range []string{"messages", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			browser, peer, transport := fakeBlockedBrowser(t)
			wing, wingPeer := detachWebSocketPair(t)
			s := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
			s.Wings.Add(&ConnectedWing{ID: "wing", WingID: "wing", Conn: wing})
			s.trackBrowser(browser, "owner")
			t.Cleanup(func() { s.untrackBrowser(browser) })
			route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser, ControllerID: "old"}
			s.PTY.Set("session", route)
			transport.blocked.Store(true)
			first := []byte(`{"type":"pty.output","session_id":"session","data":"in flight"}`)
			s.forwardPTYToBrowser("session", "wing", first)
			select {
			case <-transport.entered:
			case <-time.After(time.Second):
				t.Fatal("writer never blocked")
			}
			frame := first
			count := browserOutboundMessages + 1
			if limit == "bytes" {
				frame = []byte(`{"type":"pty.output","session_id":"session","data":"` + strings.Repeat("x", 400<<10) + `"}`)
				count = 3
			}
			for range count {
				s.forwardPTYToBrowser("session", "wing", frame)
			}
			route.mu.Lock()
			attached := route.BrowserConn != nil || route.PendingController != nil
			route.mu.Unlock()
			if attached {
				t.Fatal("overflow did not immediately release controller")
			}
			s.browserMu.Lock()
			q := s.browserConns[browser].outbound
			s.browserMu.Unlock()
			q.mu.Lock()
			queued, bytes := len(q.jobs), q.bytes
			q.mu.Unlock()
			if queued != 0 || bytes != 0 {
				t.Fatalf("overflow retained %d frames / %d bytes", queued, bytes)
			}
			close(transport.release)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			errors, outputs := 0, 0
			for {
				_, data, err := peer.Read(ctx)
				if err != nil {
					break
				}
				var env ws.Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					t.Fatal(err)
				}
				switch env.Type {
				case ws.TypeError:
					var msg ws.ErrorMsg
					_ = json.Unmarshal(data, &msg)
					if msg.Message != "detached: slow connection" || msg.SessionID != "session" {
						t.Fatalf("wrong detach error: %+v", msg)
					}
					errors++
				case ws.TypePTYOutput:
					outputs++
				default:
					t.Fatalf("unexpected frame: %s", data)
				}
			}
			if ctx.Err() != nil {
				t.Fatal("slow browser did not close after its error")
			}
			if errors != 1 || outputs != 1 {
				t.Fatalf("received %d errors and %d outputs, want one error and only in-flight output", errors, outputs)
			}
			_, data, err := wingPeer.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var det ws.PTYDetach
			_ = json.Unmarshal(data, &det)
			if det.Type != ws.TypePTYDetach || det.ControllerID != "old" {
				t.Fatalf("wrong wing detach: %s", data)
			}
			replacement, replacementPeer := detachWebSocketPair(t)
			s.trackBrowser(replacement, "owner")
			t.Cleanup(func() { s.untrackBrowser(replacement) })
			if !s.PTY.SetPendingController("session", "wing", "owner", replacement, "new") {
				t.Fatal("reattach was denied")
			}
			s.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.started","session_id":"session","controller_id":"new"}`))
			s.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.output","session_id":"session","data":"replay"}`))
			// Old cleanup must not release the replacement's authority.
			s.PTY.ClearBrowser(browser)
			if _, ok := s.PTY.ControllerWing("session", replacement); !ok {
				t.Fatal("replacement not authorized")
			}
			s.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.exited","session_id":"session","exit_code":0}`))
			if s.PTY.Get("session") != nil {
				t.Fatal("exited route was retained")
			}
			for _, want := range []string{ws.TypePTYStarted, ws.TypePTYOutput, ws.TypePTYExited} {
				_, data, err := replacementPeer.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var env ws.Envelope
				_ = json.Unmarshal(data, &env)
				if env.Type != want {
					t.Fatalf("reattach order: got %s, want %s", data, want)
				}
			}
		})
	}
}

func TestRelayWingWriteHasFiveSecondDeadline(t *testing.T) {
	wing, _, transport := fakeBlockedBrowser(t)
	transport.blocked.Store(true)
	start := time.Now()
	err := writeWingJSON(context.Background(), wing, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: "session"})
	elapsed := time.Since(start)
	if err == nil || elapsed < 4*time.Second || elapsed > 6*time.Second {
		t.Fatalf("wing write returned %v after %s; want five-second deadline", err, elapsed)
	}
	t.Logf("blocked relay-to-wing write expired after %s", elapsed)
}

func TestRelayBrowserDisconnectStopsWriter(t *testing.T) {
	browser, _, transport := fakeBlockedBrowser(t)
	s := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
	s.trackBrowser(browser, "owner")
	t.Cleanup(func() { s.untrackBrowser(browser) })
	s.PTY.Set("session", &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser})
	transport.blocked.Store(true)
	frame := []byte(`{"type":"pty.output","session_id":"session","data":"blocked"}`)
	s.forwardPTYToBrowser("session", "wing", frame)
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("writer never blocked")
	}
	s.forwardPTYToBrowser("session", "wing", frame)
	stopped := make(chan struct{})
	go func() { s.untrackBrowser(browser); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("disconnect did not cancel and join blocked writer")
	}
}

func TestRelayBrowserOverflowDisconnectForwardsDetach(t *testing.T) {
	for _, attachment := range []string{"controller", "pending controller", "viewer"} {
		t.Run(attachment, func(t *testing.T) {
			browser, _, transport := fakeBlockedBrowser(t)
			wing, wingPeer := detachWebSocketPair(t)
			s := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
			s.Wings.Add(&ConnectedWing{ID: "wing", WingID: "wing", Conn: wing})
			s.trackBrowser(browser, "owner")
			t.Cleanup(func() { s.untrackBrowser(browser) })
			route := &PTYRoute{WingID: "wing", UserID: "owner"}
			want := ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: "session"}
			switch attachment {
			case "controller":
				route.BrowserConn, route.ControllerID = browser, "old"
				want.ControllerID = "old"
			case "pending controller":
				route.PendingController, route.PendingControllerID = browser, "pending"
				want.ControllerID = "pending"
			case "viewer":
				route.Viewers = map[string]*websocket.Conn{"viewer": browser}
				want.ViewerID = "viewer"
			}
			s.PTY.Set("session", route)
			transport.blocked.Store(true)
			s.enqueueBrowser(browser, 1, "session", func(ctx, rateCtx context.Context) {
				_ = writeWebSocketJSON(ctx, browser, ws.ErrorMsg{Type: ws.TypeError, Message: "in flight"})
			})
			select {
			case <-transport.entered:
			case <-time.After(time.Second):
				t.Fatal("writer never blocked")
			}
			// Overflow while the in-flight write is still blocked, then disconnect
			// before the writer can reach the slow-connection error branch.
			s.enqueueBrowser(browser, browserOutboundBytes+1, "session", func(context.Context, context.Context) {
				t.Error("overflowed job was executed")
			})
			if captured := s.PTY.browserDetachRoutes(browser, ""); len(captured) != 0 {
				t.Fatalf("overflow left routes for normal disconnect cleanup: %v", captured)
			}
			disconnected := make(chan struct{})
			go func() { s.untrackBrowser(browser); close(disconnected) }()
			t.Cleanup(func() { <-disconnected })
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, data, err := wingPeer.Read(ctx)
			if err != nil {
				t.Fatalf("wing did not receive overflow detach after disconnect within 100 ms: %v", err)
			}
			var got ws.PTYDetach
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("wrong attachment released: got %+v, want %+v", got, want)
			}
			select {
			case <-disconnected:
			case <-ctx.Done():
				t.Fatal("disconnect did not join writer after forwarding detach")
			}
		})
	}
}
