package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestPTYInputRefusalWingDispatch(t *testing.T) {
	oldChannel := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = oldChannel })
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			config.ReleaseChannel = channel
			server, httpServer := testServer(t)
			token, _ := createTestToken(t, server.Store, "wing")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			wingPeer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/wing?token="+token, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = wingPeer.CloseNow() })
			if err := wsjson.Write(ctx, wingPeer, ws.WingRegister{Type: ws.TypeWingRegister, WingID: "wing"}); err != nil {
				t.Fatal(err)
			}
			var registered ws.RegisteredMsg
			if err := wsjson.Read(ctx, wingPeer, &registered); err != nil {
				t.Fatal(err)
			}
			if registered.Type != ws.TypeRegistered {
				t.Fatalf("registration: %+v", registered)
			}
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
				if err := wsjson.Write(ctx, wingPeer, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: sessionID, ControllerID: attempt.ControllerID}); err != nil {
					t.Fatal(err)
				}
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
			// Even a colliding tunnel request must neither capture PTY errors nor
			// be consumed by them. The operation identifies the reply protocol.
			route := server.PTY.Get(sessionID)
			route.mu.Lock()
			pending := route.PendingController
			route.mu.Unlock()
			if !server.registerTunnelRequest("wing", input.RequestID, pending, true) {
				t.Fatal("register colliding tunnel request")
			}
			// Queue and consumer failures must reach A while B's attach stays pending.
			for _, code := range []string{"session_input_busy", "session_not_found", "input_write_failed"} {
				failure := ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, ControllerID: input.ControllerID, RequestID: input.RequestID, Operation: ws.TypePTYInput, Code: code, Message: "message was not delivered"}
				if err := wsjson.Write(ctx, wingPeer, failure); err != nil {
					t.Fatal(err)
				}
				// An output fence turns a dropped refusal into a behavioral failure.
				if err := wsjson.Write(ctx, wingPeer, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: "fence"}); err != nil {
					t.Fatal(err)
				}
				var got ws.ErrorMsg
				if err := wsjson.Read(ctx, a, &got); err != nil {
					t.Fatal(err)
				}
				if got != failure {
					t.Fatalf("refusal lost at wing dispatch: got %+v, want %+v", got, failure)
				}
				var fence ws.PTYOutput
				if err := wsjson.Read(ctx, a, &fence); err != nil {
					t.Fatal(err)
				}
				if fence.Type != ws.TypePTYOutput || fence.Data != "fence" {
					t.Fatalf("refusal duplicated: %+v", fence)
				}
				route := server.PTY.Get(sessionID)
				route.mu.Lock()
				retained := route.PendingControllerID == second.ControllerID && route.BrowserConn != nil
				route.mu.Unlock()
				if !retained {
					t.Fatal("input refusal consumed B's pending authorization")
				}
			}
			// Tunnel errors still use the tunnel route, even with a session ID.
			tunnelError := ws.ErrorMsg{Type: ws.TypeError, SessionID: sessionID, RequestID: input.RequestID, Operation: ws.TypeTunnelRequest, Message: "tunnel failed"}
			if err := wsjson.Write(ctx, wingPeer, tunnelError); err != nil {
				t.Fatal(err)
			}
			var gotTunnel ws.ErrorMsg
			if err := wsjson.Read(ctx, b, &gotTunnel); err != nil {
				t.Fatal(err)
			}
			if gotTunnel != tunnelError {
				t.Fatalf("PTY refusal captured the tunnel route: %+v", gotTunnel)
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
			if err := wingPeer.Write(ctx, websocket.MessageText, data); err != nil {
				t.Fatal(err)
			}
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
			if err := wingPeer.Write(ctx, websocket.MessageText, data); err != nil {
				t.Fatal(err)
			}
			// Fence through the same wing dispatcher, rather than another socket.
			if err := wsjson.Write(ctx, wingPeer, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: sessionID, Data: "final fence"}); err != nil {
				t.Fatal(err)
			}
			var pong ws.PTYOutput
			if err := wsjson.Read(ctx, b, &pong); err != nil {
				t.Fatal(err)
			}
			if pong.Type != ws.TypePTYOutput || pong.Data != "final fence" {
				t.Fatalf("A's refusal leaked to B: %+v", pong)
			}
		})
	}
}
