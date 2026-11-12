package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRelaySharedRateLimitDoesNotReportQuota(t *testing.T) {
	for _, recipient := range []string{"controller", "viewer", "tunnel", "coordination"} {
		t.Run(recipient, func(t *testing.T) {
			t.Parallel()
			browser, peer := detachWebSocketPair(t)
			meter := NewBandwidthMeter(SustainedRate, 1<<20, nil)
			meter.SetTierLookup(func(string) string { return "pro" })
			// Other connections for the same account can each reserve a burst. At
			// production rates, three reservations put this connection >5s behind.
			limiter := meter.limiter("owner")
			now := time.Now()
			for range 3 {
				if r := limiter.ReserveN(now, 1<<20); !r.OK() {
					t.Fatal("reservation failed")
				}
			}
			s := &Server{LocalMode: true, Bandwidth: meter, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection), tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
			s.Wings.Add(&ConnectedWing{ID: "wing", WingID: "wing"})
			s.trackBrowser(browser, "owner")
			t.Cleanup(func() { s.untrackBrowser(browser) })
			route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser}
			data := []byte(`{"type":"pty.output","session_id":"session","data":"ready"}`)
			switch recipient {
			case "viewer":
				route.BrowserConn = nil
				route.Viewers = map[string]*websocket.Conn{"viewer": browser}
				data = []byte(`{"type":"pty.output","session_id":"session","viewer_id":"viewer","data":"ready"}`)
			case "tunnel", "coordination":
				data = []byte(`{"type":"tunnel.response","request_id":"request","payload":"ready"}`)
			}
			s.PTY.Set("session", route)
			if recipient == "tunnel" || recipient == "coordination" {
				if !s.registerTunnelRequest("wing", "request", browser, recipient == "coordination") {
					t.Fatal("registration failed")
				}
				s.forwardTunnelToBrowser("wing", "request", data, true)
			} else {
				s.forwardPTYToBrowser("session", "wing", data)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_, got, err := peer.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(data) {
				t.Fatalf("rate-limited Pro browser got %s, want payload %s", got, data)
			}
			if elapsed := time.Since(now); elapsed < 5*time.Second {
				t.Fatalf("rate wait only took %s", elapsed)
			} else {
				t.Logf("payload arrived after %s shared rate wait without a quota error", elapsed)
			}
			route.mu.Lock()
			defer route.mu.Unlock()
			if recipient == "controller" && route.BrowserConn != browser {
				t.Fatal("rate wait detached controller")
			}
		})
	}
}

func TestRelayMonthlyQuotaStillReportsExceeded(t *testing.T) {
	for _, recipient := range []string{"controller", "viewer"} {
		t.Run(recipient, func(t *testing.T) {
			browser, peer := detachWebSocketPair(t)
			meter := NewBandwidthMeter(SustainedRate, 1<<20, nil)
			meter.counter("owner").Store(freeMonthlyCap)
			s := &Server{LocalMode: true, Bandwidth: meter, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
			s.trackBrowser(browser, "owner")
			t.Cleanup(func() { s.untrackBrowser(browser) })
			route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser}
			data := []byte(`{"type":"pty.output","session_id":"session","data":"ready"}`)
			if recipient == "viewer" {
				route.BrowserConn = nil
				route.Viewers = map[string]*websocket.Conn{"viewer": browser}
				data = []byte(`{"type":"pty.output","session_id":"session","viewer_id":"viewer","data":"ready"}`)
			}
			s.PTY.Set("session", route)
			s.forwardPTYToBrowser("session", "wing", data)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, got, err := peer.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var msg ws.BandwidthExceeded
			if err := json.Unmarshal(got, &msg); err != nil || msg.Type != ws.TypeBandwidthExceeded {
				t.Fatalf("want monthly quota error, got %s (%v)", got, err)
			}
			// Complete the controller's close handshake before joining its writer.
			if recipient == "controller" {
				_, _, _ = peer.Read(ctx)
			}
		})
	}
}

func TestRelayRateDeadlineDoesNotReportQuotaOrDetach(t *testing.T) {
	for _, recipient := range []string{"controller", "metered"} {
		t.Run(recipient, func(t *testing.T) {
			browser, peer := detachWebSocketPair(t)
			meter := NewBandwidthMeter(SustainedRate, 1<<20, nil)
			meter.SetTierLookup(func(string) string { return "pro" })
			for range 3 {
				meter.limiter("owner").ReserveN(time.Now(), 1<<20)
			}
			s := &Server{LocalMode: true, Bandwidth: meter, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
			s.trackBrowser(browser, "owner")
			t.Cleanup(func() { s.untrackBrowser(browser) })
			route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser}
			// WaitN returns immediately because the shared reservations exceed this
			// caller's deadline. That error must never be reported as monthly quota.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if recipient == "controller" {
					s.writeControllerPayload(ctx, ctx, route, browser, "owner", "session", []byte("limited"))
				} else if s.writeMeteredRelayPayload(ctx, ctx, browser, []byte("limited")) {
					done <- context.Canceled
					return
				}
				done <- writeBrowserPayload(ctx, browser, []byte("still attached"))
			}()
			_, got, err := peer.Read(ctx)
			if err != nil || string(got) != "still attached" {
				t.Fatalf("rate error emitted quota or closed browser: %s (%v)", got, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			route.mu.Lock()
			defer route.mu.Unlock()
			if route.BrowserConn != browser {
				t.Fatal("rate error detached controller")
			}
		})
	}
}

func TestRelayDisconnectCancelsSharedRateWait(t *testing.T) {
	browser, _ := detachWebSocketPair(t)
	meter := NewBandwidthMeter(SustainedRate, 1<<20, nil)
	meter.SetTierLookup(func(string) string { return "pro" })
	for range 3 {
		meter.limiter("owner").ReserveN(time.Now(), 1<<20)
	}
	s := &Server{LocalMode: true, Bandwidth: meter, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
	s.trackBrowser(browser, "owner")
	t.Cleanup(func() { s.untrackBrowser(browser) })
	s.PTY.Set("session", &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser})
	s.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.output","session_id":"session","data":"ready"}`))
	deadline := time.After(time.Second)
	for meter.MonthlyUsage("owner") == 0 {
		select {
		case <-deadline:
			t.Fatal("writer did not reach bandwidth wait")
		case <-time.After(time.Millisecond):
		}
	}
	done := make(chan struct{})
	go func() { s.untrackBrowser(browser); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("disconnect did not cancel rate wait and join writer within 100 ms")
	}
}

func TestRelayBrowserWriteHasFiveSecondDeadline(t *testing.T) {
	browser, _, transport := fakeBlockedBrowser(t)
	s := &Server{LocalMode: true, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection)}
	s.trackBrowser(browser, "owner")
	t.Cleanup(func() { s.untrackBrowser(browser) })
	s.PTY.Set("session", &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser})
	transport.blocked.Store(true)
	start := time.Now()
	s.forwardPTYToBrowser("session", "wing", []byte(`{"type":"pty.output","session_id":"session","data":"blocked"}`))
	select {
	case <-transport.closed:
		if elapsed := time.Since(start); elapsed < 4*time.Second {
			t.Fatalf("socket closed too soon: %s", elapsed)
		} else {
			t.Logf("blocked browser write expired after %s", elapsed)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("browser socket write lost its five-second deadline")
	}
}

func TestRelayBrowserOverflowCancelsSharedRateWait(t *testing.T) {
	for _, recipient := range []string{"controller", "viewer", "tunnel", "coordination"} {
		for _, limit := range []string{"messages", "bytes"} {
			t.Run(recipient+"/"+limit, func(t *testing.T) {
				browser, peer := detachWebSocketPair(t)
				wing, wingPeer := detachWebSocketPair(t)
				meter := NewBandwidthMeter(SustainedRate, 1<<20, nil)
				meter.SetTierLookup(func(string) string { return "pro" })
				limiter := meter.limiter("owner")
				// Shared connections can reserve minutes of bandwidth in advance.
				for range 100 {
					if r := limiter.ReserveN(time.Now(), 1<<20); !r.OK() {
						t.Fatal("reservation failed")
					}
				}
				tokens := limiter.Tokens()
				s := &Server{LocalMode: true, Bandwidth: meter, PTY: NewPTYRoutes(), Wings: NewWingRegistry(), browserConns: make(map[*websocket.Conn]*browserConnection), tunnelRequests: make(map[tunnelRequestKey]pendingTunnelRequest)}
				s.Wings.Add(&ConnectedWing{ID: "wing", WingID: "wing", Conn: wing})
				s.trackBrowser(browser, "owner")
				t.Cleanup(func() { s.untrackBrowser(browser) })
				route := &PTYRoute{WingID: "wing", UserID: "owner", BrowserConn: browser, ControllerID: "old"}
				wantDetach := ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: "session", ControllerID: "old"}
				if recipient == "viewer" {
					route.BrowserConn = nil
					route.Viewers = map[string]*websocket.Conn{"viewer": browser}
					wantDetach.ControllerID, wantDetach.ViewerID = "", "viewer"
				}
				s.PTY.Set("session", route)
				frame := []byte(`{"type":"pty.output","session_id":"session","data":"` + strings.Repeat("x", 64<<10) + `"}`)
				if recipient == "viewer" {
					frame = []byte(`{"type":"pty.output","session_id":"session","viewer_id":"viewer","data":"` + strings.Repeat("x", 64<<10) + `"}`)
				}
				if recipient == "tunnel" || recipient == "coordination" {
					if !s.registerTunnelRequest("wing", "request", browser, recipient == "coordination") {
						t.Fatal("registration failed")
					}
					s.forwardTunnelToBrowser("wing", "request", frame, false)
				} else {
					s.forwardPTYToBrowser("session", "wing", frame)
				}
				// Observe the reservation, including coordination's unmetered wait,
				// before overflowing; the writer must actually be rate blocked.
				deadline := time.Now().Add(time.Second)
				for limiter.Tokens() >= tokens-float64(len(frame))/2 {
					if time.Now().After(deadline) {
						t.Fatal("writer never entered shared rate wait")
					}
					time.Sleep(time.Millisecond)
				}
				count, size := browserOutboundMessages+1, 1
				if limit == "bytes" {
					count, size = 1, browserOutboundBytes+1
				}
				for range count {
					s.enqueueBrowser(browser, size, "session", func(context.Context, context.Context) {
						t.Error("overflowed job was executed")
					})
				}
				ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
				_, data, err := peer.Read(ctx)
				if err != nil {
					t.Fatalf("browser did not receive slow-connection error during shared rate wait: %v", err)
				}
				var got ws.ErrorMsg
				if err := json.Unmarshal(data, &got); err != nil || got.Type != ws.TypeError || got.SessionID != "session" || got.Message != "detached: slow connection" {
					t.Fatalf("wrong slow-connection error: %s (%v)", data, err)
				}
				if _, data, err := peer.Read(ctx); err == nil || ctx.Err() != nil {
					t.Fatalf("want close after exactly one error, got %s (%v)", data, err)
				}
				_, data, err = wingPeer.Read(ctx)
				if err != nil {
					t.Fatalf("wing did not receive overflow detach during shared rate wait: %v", err)
				}
				var detached ws.PTYDetach
				if err := json.Unmarshal(data, &detached); err != nil || detached != wantDetach {
					t.Fatalf("wrong wing detach: %s (%v)", data, err)
				}
				if routes := s.PTY.browserDetachRoutes(browser, ""); len(routes) != 0 {
					t.Fatalf("overflow left attached routes: %v", routes)
				}
				s.tunnelMu.Lock()
				pending := len(s.tunnelRequests)
				s.tunnelMu.Unlock()
				if pending != 0 {
					t.Fatal("overflow retained tunnel requests")
				}
			})
		}
	}
}
