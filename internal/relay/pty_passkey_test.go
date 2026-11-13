package relay

import (
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"testing"
	"time"
)

func TestPasskeySocketBindingLifecycle(t *testing.T) {
	for _, mode := range []string{"reply", "expiry", "pending-expiry", "detach", "disconnect", "takeover"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, a, b, _, start, attach := initialControllerPair(t)
			id, browser := start.ControllerID, a
			if mode == "pending-expiry" || mode == "detach" || mode == "disconnect" {
				id, browser = attach.ControllerID, b
			}
			route := s.PTY.Get(start.SessionID)
			now := s.PTY.clockNow()
			// No timer or browser handler consults this clock concurrently in
			// this fixture: responses below call the registry directly.
			s.PTY.now = func() time.Time { return now }
			initialForward(t, s, start.SessionID, ws.PasskeyChallenge{Type: ws.TypePasskeyChallenge, SessionID: start.SessionID, ControllerID: id, Challenge: "bound"})
			var challenge ws.PasskeyChallenge
			if err := wsjson.Read(ctx, browser, &challenge); err != nil {
				t.Fatal(err)
			}
			route.mu.Lock()
			conn := route.BrowserConn
			if browser == b {
				conn = route.PendingController
			}
			route.mu.Unlock()
			switch mode {
			case "expiry":
				now = now.Add(61 * time.Second)
			case "pending-expiry":
				now = now.Add(31 * time.Second)
			case "detach":
				if err := wsjson.Write(ctx, b, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: start.SessionID}); err != nil {
					t.Fatal(err)
				}
				if err := wsjson.Write(ctx, b, ws.Envelope{Type: ws.TypePTYPing}); err != nil {
					t.Fatal(err)
				}
				var pong ws.Envelope
				if err := wsjson.Read(ctx, b, &pong); err != nil {
					t.Fatal(err)
				}
				if pong.Type != ws.TypePTYPong {
					t.Fatalf("detach fence: %+v", pong)
				}
			case "disconnect":
				s.PTY.ClearBrowser(conn)
			case "takeover":
				initialForward(t, s, start.SessionID, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: start.SessionID, ControllerID: attach.ControllerID})
			}
			response := ws.PasskeyResponse{SessionID: start.SessionID, ControllerID: "spoofed"}
			_, ok := s.PTY.takePasskeyResponse(&response, conn)
			if ok != (mode == "reply") {
				t.Fatalf("%s response accepted=%v", mode, ok)
			}
			if ok && response.ControllerID != id {
				t.Fatalf("unbound response: %+v", response)
			}
			route.mu.Lock()
			remaining := len(route.passkeyChallenges)
			route.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("%s retained %d challenge routes", mode, remaining)
			}
		})
	}
}
