package relay

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestPendingControllerExpiryAndDetachBothChannels(t *testing.T) {
	oldChannel := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			config.ReleaseChannel = channel
			server, httpServer := testServer(t)
			token, userID := createTestToken(t, server.Store, "pending")
			wing, wingPeer := detachWebSocketPair(t)
			server.Wings.wings["wing"] = &ConnectedWing{WingID: "wing", UserID: userID, Conn: wing}
			var seconds atomic.Int64
			server.PTY.now = func() time.Time { return time.Unix(1000+seconds.Load(), 0) }
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dial := func() *websocket.Conn {
				t.Helper()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/pty?token="+token+"&wing_id=wing", nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.CloseNow() })
				return conn
			}
			a, b := dial(), dial()
			attach := ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: "session", WingID: "wing"}
			if err := wsjson.Write(ctx, a, attach); err != nil {
				t.Fatal(err)
			}
			var first ws.PTYAttach
			if err := wsjson.Read(ctx, wingPeer, &first); err != nil {
				t.Fatal(err)
			}
			seconds.Store(5)
			if err := wsjson.Write(ctx, b, attach); err != nil {
				t.Fatal(err)
			}
			var busy ws.ErrorMsg
			if err := wsjson.Read(ctx, b, &busy); err != nil {
				t.Fatal(err)
			}
			if busy.Type != ws.TypeError || busy.SessionID != "session" || busy.Message != "another attach is in progress" {
				t.Fatalf("5s refusal: %+v", busy)
			}
			seconds.Store(31)
			if err := wsjson.Write(ctx, b, attach); err != nil {
				t.Fatal(err)
			}
			var second ws.PTYAttach
			if err := wsjson.Read(ctx, wingPeer, &second); err != nil {
				t.Fatal(err)
			}
			if second.ControllerID == "" || second.ControllerID == first.ControllerID {
				t.Fatalf("attempt IDs were reused: first=%+v second=%+v", first, second)
			}
			// A late authorization must never promote B using A's abandoned request.
			late, _ := json.Marshal(ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: "session", ControllerID: first.ControllerID})
			server.forwardPTYToBrowser("session", "wing", late)
			// Expired authorization failures and challenges must also be fenced.
			for _, message := range []any{
				ws.ErrorMsg{Type: ws.TypeError, SessionID: "session", ControllerID: first.ControllerID, Message: "invalid passkey"},
				ws.PasskeyChallenge{Type: ws.TypePasskeyChallenge, SessionID: "session", ControllerID: first.ControllerID, Challenge: "late"},
			} {
				data, _ := json.Marshal(message)
				server.forwardPTYToBrowser("session", "wing", data)
			}
			route := server.PTY.Get("session")
			route.mu.Lock()
			retained := route.BrowserConn == nil && route.PendingControllerID == second.ControllerID
			route.mu.Unlock()
			if !retained {
				t.Fatal("late A reply authorized B")
			}
			challenge, _ := json.Marshal(ws.PasskeyChallenge{Type: ws.TypePasskeyChallenge, SessionID: "session", ControllerID: second.ControllerID, Challenge: "current"})
			server.forwardPTYToBrowser("session", "wing", challenge)
			var gotChallenge ws.PasskeyChallenge
			if err := wsjson.Read(ctx, b, &gotChallenge); err != nil {
				t.Fatal(err)
			}
			if gotChallenge.Challenge != "current" {
				t.Fatalf("late response leaked to B: %+v", gotChallenge)
			}
			route.mu.Lock()
			retained = route.PendingControllerID == second.ControllerID
			route.mu.Unlock()
			if !retained {
				t.Fatal("challenge cleared B's pending attach")
			}
			if err := wsjson.Write(ctx, b, ws.PTYDetach{Type: ws.TypePTYDetach, SessionID: "session"}); err != nil {
				t.Fatal(err)
			}
			// The same socket's pong fences detach processing without polling or sleeps.
			if err := wsjson.Write(ctx, b, map[string]string{"type": ws.TypePTYPing}); err != nil {
				t.Fatal(err)
			}
			var pong ws.Envelope
			if err := wsjson.Read(ctx, b, &pong); err != nil {
				t.Fatal(err)
			}
			if pong.Type != ws.TypePTYPong {
				t.Fatalf("detach fence: %+v", pong)
			}
			route.mu.Lock()
			cleared := route.PendingController == nil && route.PendingControllerID == "" && route.PendingUserID == "" && route.PendingSince.IsZero() && route.pendingTimer == nil
			route.mu.Unlock()
			if !cleared {
				t.Fatal("detach retained pending controller state")
			}
			if err := wsjson.Write(ctx, a, attach); err != nil {
				t.Fatal(err)
			}
			var afterDetach ws.PTYAttach
			// Preview also forwards the exact pending detach to the wing.
			if channel == "preview" {
				var detached ws.PTYDetach
				if err := wsjson.Read(ctx, wingPeer, &detached); err != nil {
					t.Fatal(err)
				}
				if detached.ControllerID != second.ControllerID || detached.Type != ws.TypePTYDetach {
					t.Fatalf("wrong pending detach: %+v", detached)
				}
			}
			if err := wsjson.Read(ctx, wingPeer, &afterDetach); err != nil {
				t.Fatal(err)
			}
			if afterDetach.Type != ws.TypePTYAttach {
				t.Fatalf("attach after detach failed: %+v", afterDetach)
			}
			route.mu.Lock()
			lastPending := route.PendingController
			route.mu.Unlock()
			server.PTY.ClearBrowser(lastPending)
			t.Log("A abandoned; B refused at 5s, admitted at 31s; late A ignored; detach permits immediate retry")
		})
	}
}

func TestPTYInputRefusalKeepsPendingAttach(t *testing.T) {
	controller, controllerPeer := detachWebSocketPair(t)
	pending, _ := detachWebSocketPair(t)
	server := &Server{LocalMode: true, PTY: NewPTYRoutes(), browserConns: make(map[*websocket.Conn]*browserConnection)}
	server.trackBrowser(controller, "owner")
	server.trackBrowser(pending, "owner")
	route := &PTYRoute{WingID: "wing", BrowserConn: controller, ControllerID: "active"}
	server.PTY.Set("session", route)
	if !server.PTY.SetPendingController("session", "wing", "owner", pending, "next") {
		t.Fatal("reserve pending attach")
	}
	t.Cleanup(func() { server.PTY.ClearBrowser(pending) })
	data, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, SessionID: "session", Code: "session_input_busy", Operation: ws.TypePTYInput, Message: "session input is busy; message was not delivered"})
	server.forwardPTYToBrowser("session", "wing", data)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var refusal ws.ErrorMsg
	if err := wsjson.Read(ctx, controllerPeer, &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "session_input_busy" {
		t.Fatalf("input controller did not receive refusal: %+v", refusal)
	}
	route.mu.Lock()
	retained := route.PendingController == pending && route.PendingControllerID == "next"
	route.mu.Unlock()
	if !retained {
		t.Fatal("input refusal cleared unrelated pending attach")
	}
}

func TestPTYInputRefusalFollowsSenderAcrossTakeover(t *testing.T) {
	oldChannel := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			config.ReleaseChannel = channel
			server, httpServer := testServer(t)
			token, userID := createTestToken(t, server.Store, "input-sender")
			wing, wingPeer := detachWebSocketPair(t)
			server.Wings.wings["wing"] = &ConnectedWing{WingID: "wing", UserID: userID, Conn: wing}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dial := func() *websocket.Conn {
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/pty?token="+token+"&wing_id=wing", nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.CloseNow() })
				return conn
			}
			a, b := dial(), dial()
			sessionID := ""
			attach := func(conn *websocket.Conn) ws.PTYAttach {
				t.Helper()
				if err := wsjson.Write(ctx, conn, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: sessionID}); err != nil {
					t.Fatal(err)
				}
				var attempt ws.PTYAttach
				if err := wsjson.Read(ctx, wingPeer, &attempt); err != nil {
					t.Fatal(err)
				}
				return attempt
			}
			promote := func(conn *websocket.Conn, attempt ws.PTYAttach) {
				t.Helper()
				data, _ := json.Marshal(ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, ControllerID: attempt.ControllerID})
				server.forwardPTYToBrowser(sessionID, "wing", data)
				var started ws.PTYStarted
				if err := wsjson.Read(ctx, conn, &started); err != nil {
					t.Fatal(err)
				}
				if started.Type != ws.TypePTYStarted {
					t.Fatalf("promotion: %+v", started)
				}
			}
			if err := wsjson.Write(ctx, a, ws.PTYStart{Type: ws.TypePTYStart, ControllerID: "forged"}); err != nil {
				t.Fatal(err)
			}
			var start ws.PTYStart
			if err := wsjson.Read(ctx, wingPeer, &start); err != nil {
				t.Fatal(err)
			}
			if start.ControllerID == "" || start.ControllerID == "forged" {
				t.Fatalf("start has no relay-bound controller: %+v", start)
			}
			sessionID = start.SessionID
			first := ws.PTYAttach{ControllerID: start.ControllerID}
			promote(a, first)
			second := attach(b)
			// Browser messages may omit controller identity or forge routing fields.
			if err := wsjson.Write(ctx, a, map[string]string{"type": ws.TypePTYInput, "session_id": sessionID, "data": "encrypted", "controller_id": second.ControllerID, "request_id": "forged"}); err != nil {
				t.Fatal(err)
			}
			var input struct {
				ControllerID string `json:"controller_id"`
				RequestID    string `json:"request_id"`
			}
			if err := wsjson.Read(ctx, wingPeer, &input); err != nil {
				t.Fatal(err)
			}
			if input.ControllerID != first.ControllerID || input.RequestID == "" || input.RequestID == "forged" {
				t.Fatalf("unbound input: %+v", input)
			}
			// Simulate the wing waiting on a full input queue while B takes over.
			promote(b, second)
			var takeover ws.ErrorMsg
			if err := wsjson.Read(ctx, a, &takeover); err != nil {
				t.Fatal(err)
			}
			if takeover.ControllerID != first.ControllerID || !strings.Contains(takeover.Message, "taken over") {
				t.Fatalf("missing takeover notification: %+v", takeover)
			}
			data, _ := json.Marshal(ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, ControllerID: input.ControllerID, RequestID: input.RequestID, Operation: ws.TypePTYInput, Code: "session_input_busy", Message: "session input is busy; message was not delivered"})
			server.forwardPTYToBrowser(sessionID, "wing", data)
			for {
				var refusal ws.ErrorMsg
				if err := wsjson.Read(ctx, a, &refusal); err != nil {
					t.Fatal(err)
				}
				if refusal.Code == "session_input_busy" {
					break
				}
				if !strings.Contains(refusal.Message, "taken over") {
					t.Fatalf("unexpected response: %+v", refusal)
				}
			}
			// Every new keystroke on the replaced socket must also be refused.
			if err := wsjson.Write(ctx, a, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sessionID, Data: "stale"}); err != nil {
				t.Fatal(err)
			}
			var stale ws.ErrorMsg
			if err := wsjson.Read(ctx, a, &stale); err != nil {
				t.Fatal(err)
			}
			if stale.Type != ws.TypeError || stale.Operation != ws.TypePTYInput || stale.SessionID != sessionID || stale.Code != "controller_replaced" {
				t.Fatalf("post-takeover input lost without refusal: %+v", stale)
			}
			if err := wsjson.Write(ctx, b, ws.PTYInput{Type: ws.TypePTYInput, SessionID: sessionID, Data: "current"}); err != nil {
				t.Fatal(err)
			}
			var current ws.PTYInput
			if err := wsjson.Read(ctx, wingPeer, &current); err != nil {
				t.Fatal(err)
			}
			if current.Data != "current" || current.ControllerID != second.ControllerID {
				t.Fatalf("stale input forwarded to wing: %+v", current)
			}
			// Once the original socket disappears, never fall back to its replacement.
			server.untrackBrowser(server.browserForReplyID(input.RequestID))
			server.forwardPTYToBrowser(sessionID, "wing", data)
			if err := wsjson.Write(ctx, b, ws.Envelope{Type: ws.TypePTYPing}); err != nil {
				t.Fatal(err)
			}
			var pong ws.Envelope
			if err := wsjson.Read(ctx, b, &pong); err != nil {
				t.Fatal(err)
			}
			if pong.Type != ws.TypePTYPong {
				t.Fatalf("A's refusal leaked to B: %+v", pong)
			}
		})
	}
}
