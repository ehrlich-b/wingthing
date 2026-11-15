package relay

import (
	"encoding/json"
	"testing"

	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestReviewerLateAttachRefusalAfterRouteRecreated(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		for _, operation := range []string{ws.TypePTYInput, ws.TypePTYAttach} {
			t.Run(channel+"/"+operation, func(t *testing.T) {
				config.ReleaseChannel = channel
				s, ctx, a, b, wing, start, attach := initialControllerPair(t)
				route := s.PTY.Get(start.SessionID)
				route.mu.Lock()
				aSocket, bSocket := route.BrowserConn, route.PendingController
				route.mu.Unlock()
				// Both operations use the socket/session/wing capability bound at ingress.
				origin := attach.RequestID
				initialForward(t, s, start.SessionID, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID})
				var env ws.Envelope
				if err := wsjson.Read(ctx, a, &env); err != nil || env.Type != ws.TypePTYExited {
					t.Fatalf("A exit: %+v, %v", env, err)
				}
				if err := wsjson.Read(ctx, b, &env); err != nil || env.Type != ws.TypePTYExited || s.PTY.Get(start.SessionID) != nil {
					t.Fatalf("B exit did not retire route: %+v, %v", env, err)
				}
				// A's browser-shaped attach recreates the route before B's refusal.
				if err := wsjson.Write(ctx, a, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: start.SessionID}); err != nil {
					t.Fatal(err)
				}
				var replacement ws.PTYAttach
				if err := wsjson.Read(ctx, wing, &replacement); err != nil {
					t.Fatal(err)
				}
				recreated := s.PTY.Get(start.SessionID)
				if recreated == nil || recreated == route || replacement.ControllerID == attach.ControllerID {
					t.Fatal("new attach did not recreate the route")
				}
				refusal := ws.ErrorMsg{Type: ws.TypeError, Operation: operation, SessionID: start.SessionID, ControllerID: attach.ControllerID, RequestID: origin, Code: "session_not_found", Message: "session not found; message was not delivered"}
				// Invalid capabilities cannot use the new live route either.
				data, _ := json.Marshal(refusal)
				s.forwardPTYToBrowser(start.SessionID, "stranger", data)
				wrong := refusal
				wrong.RequestID = s.browserReplyID(bSocket)
				initialForward(t, s, start.SessionID, wrong)
				initialForward(t, s, start.SessionID, refusal)
				initialFence(t, s, bSocket)
				var got ws.ErrorMsg
				if err := wsjson.Read(ctx, b, &got); err != nil {
					t.Fatal(err)
				}
				if got != refusal {
					t.Fatalf("late attach refusal lost after route recreated: got %+v, want %+v", got, refusal)
				}
				if err := wsjson.Read(ctx, b, &env); err != nil || env.Type != ws.TypePTYPong {
					t.Fatalf("unauthenticated or duplicate refusal: %+v, %v", env, err)
				}
				s.untrackBrowser(bSocket)
				initialForward(t, s, start.SessionID, refusal)
				initialFence(t, s, aSocket)
				if err := wsjson.Read(ctx, a, &env); err != nil || env.Type != ws.TypePTYPong {
					t.Fatalf("old refusal reached replacement: %+v, %v", env, err)
				}
				recreated.mu.Lock()
				retained := recreated.PendingController == aSocket && recreated.PendingControllerID == replacement.ControllerID
				recreated.mu.Unlock()
				if !retained {
					t.Fatal("old refusal changed replacement authorization")
				}
				// A's own addressed refusal must still finish its pending attach.
				current := refusal
				current.Operation, current.ControllerID, current.RequestID = ws.TypePTYAttach, replacement.ControllerID, replacement.RequestID
				initialForward(t, s, start.SessionID, current)
				if err := wsjson.Read(ctx, a, &got); err != nil || got != current {
					t.Fatalf("current attach refusal: %+v, %v", got, err)
				}
				recreated.mu.Lock()
				cleared := recreated.PendingController == nil && recreated.pendingTimer == nil
				recreated.mu.Unlock()
				if !cleared || s.PTY.Get(start.SessionID) != nil {
					t.Fatal("current refusal retained provisional attach")
				}
			})
		}
	}
}
