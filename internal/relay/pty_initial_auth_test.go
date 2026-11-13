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

// Use real browser sockets and the production ingress. A is still starting
// when B's attach is accepted; neither B nor its supplied IDs can answer for A.
func initialControllerPair(t *testing.T) (*Server, context.Context, *websocket.Conn, *websocket.Conn, *websocket.Conn, ws.PTYStart, ws.PTYAttach) {
	t.Helper()
	s, httpServer := testServer(t)
	token, userID := createTestToken(t, s.Store, "initial-auth")
	wing, peer := detachWebSocketPair(t)
	s.Wings.wings["wing"] = &ConnectedWing{WingID: "wing", UserID: userID, Conn: wing}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	dial := func() *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/pty?token="+token+"&wing_id=wing", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.CloseNow() })
		return c
	}
	a, b := dial(), dial()
	if err := wsjson.Write(ctx, a, ws.PTYStart{Type: ws.TypePTYStart}); err != nil {
		t.Fatal(err)
	}
	var start ws.PTYStart
	if err := wsjson.Read(ctx, peer, &start); err != nil {
		t.Fatal(err)
	}
	if start.ControllerID == "" {
		t.Fatal("start missing authenticated controller ID")
	}
	if err := wsjson.Write(ctx, b, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: start.SessionID}); err != nil {
		t.Fatal(err)
	}
	var attach ws.PTYAttach
	if err := wsjson.Read(ctx, peer, &attach); err != nil {
		t.Fatal(err)
	}
	return s, ctx, a, b, peer, start, attach
}

func initialForward(t *testing.T, s *Server, session string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	s.forwardPTYToBrowser(session, "wing", data)
}

// Queue the fence with the payloads it orders. Production browser delivery is
// asynchronous so a direct socket write could overtake an accepted refusal.
func initialFence(t *testing.T, s *Server, conn *websocket.Conn) {
	t.Helper()
	s.enqueueBrowser(conn, len(ws.TypePTYPong), "", func(ctx, rateCtx context.Context) {
		if err := wsjson.Write(ctx, conn, ws.Envelope{Type: ws.TypePTYPong}); err != nil {
			t.Errorf("write browser fence: %v", err)
		}
	})
}

// A fence on each outgoing socket makes misrouting and dropped messages
// observable without sleeps or a read timeout as the expected assertion.
func initialFences(t *testing.T, ctx context.Context, s *Server, session string) {
	t.Helper()
	route := s.PTY.Get(session)
	route.mu.Lock()
	a, b := route.BrowserConn, route.PendingController
	route.mu.Unlock()
	for _, c := range []*websocket.Conn{a, b} {
		initialFence(t, s, c)
	}
}

func TestIndependentInitialChallengeRouting(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		for _, scoped := range []bool{false, true} {
			name := channel + "/legacy"
			if scoped {
				name = channel + "/addressed"
			}
			t.Run(name, func(t *testing.T) {
				config.ReleaseChannel = channel
				s, ctx, a, b, wing, start, attach := initialControllerPair(t)
				controller := ""
				if scoped {
					controller = start.ControllerID
				}
				initialForward(t, s, start.SessionID, ws.PasskeyChallenge{Type: ws.TypePasskeyChallenge, SessionID: start.SessionID, ControllerID: controller, Challenge: "A-initial"})
				initialFences(t, ctx, s, start.SessionID)
				var env ws.Envelope
				if err := wsjson.Read(ctx, b, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYPong {
					t.Fatalf("B received A initial challenge: %+v", env)
				}
				var challenge ws.PasskeyChallenge
				if err := wsjson.Read(ctx, a, &challenge); err != nil {
					t.Fatal(err)
				}
				if challenge.Challenge != "A-initial" {
					t.Fatalf("A lost initial challenge: %+v", challenge)
				}
				if err := wsjson.Read(ctx, a, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYPong {
					t.Fatalf("A challenge duplicated: %+v", env)
				}
				respond := func(c *websocket.Conn, id, signature string) {
					t.Helper()
					if err := wsjson.Write(ctx, c, ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: start.SessionID, ControllerID: id, Signature: signature}); err != nil {
						t.Fatal(err)
					}
					if err := wsjson.Write(ctx, c, ws.Envelope{Type: ws.TypePTYPing}); err != nil {
						t.Fatal(err)
					}
					if err := wsjson.Read(ctx, c, &env); err != nil {
						t.Fatal(err)
					}
					if env.Type != ws.TypePTYPong {
						t.Fatalf("response fence: %+v", env)
					}
				}
				// B forges A's ID; A omits its ID, like the browser. Only A's
				// response may reach the wing, with relay-bound identity.
				respond(b, start.ControllerID, "from-B")
				respond(a, "", "from-A")
				var response ws.PasskeyResponse
				if err := wsjson.Read(ctx, wing, &response); err != nil {
					t.Fatal(err)
				}
				if response.Signature != "from-A" || response.ControllerID != start.ControllerID {
					t.Fatalf("unchallenged socket answered A: %+v", response)
				}
				// Now challenge B. A's old response (even spoofing B) and a
				// duplicate B response cannot be used for this authorization.
				initialForward(t, s, start.SessionID, ws.PasskeyChallenge{Type: ws.TypePasskeyChallenge, SessionID: start.SessionID, ControllerID: attach.ControllerID, Challenge: "B-reattach"})
				if err := wsjson.Read(ctx, b, &challenge); err != nil {
					t.Fatal(err)
				}
				if challenge.Challenge != "B-reattach" {
					t.Fatalf("B lost challenge: %+v", challenge)
				}
				respond(a, attach.ControllerID, "stale-A")
				respond(b, start.ControllerID, "from-B")
				respond(b, attach.ControllerID, "duplicate-B")
				if err := wsjson.Read(ctx, wing, &response); err != nil {
					t.Fatal(err)
				}
				if response.Signature != "from-B" || response.ControllerID != attach.ControllerID {
					t.Fatalf("wrong reattach responder: %+v", response)
				}
				// A controller input is a fence through the same ingress/wing
				// socket. No rejected response may precede it.
				if err := wsjson.Write(ctx, a, ws.PTYInput{Type: ws.TypePTYInput, SessionID: start.SessionID, Data: "fence"}); err != nil {
					t.Fatal(err)
				}
				var input ws.PTYInput
				if err := wsjson.Read(ctx, wing, &input); err != nil {
					t.Fatal(err)
				}
				if input.Type != ws.TypePTYInput || input.Data != "fence" {
					t.Fatalf("extra passkey response reached wing: %+v", input)
				}
			})
		}
	}
}

func TestInitialStartedWhileReattachPending(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			config.ReleaseChannel = channel
			s, ctx, a, b, _, start, attach := initialControllerPair(t)
			initialForward(t, s, start.SessionID, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: start.SessionID, ControllerID: start.ControllerID})
			initialFences(t, ctx, s, start.SessionID)
			var started ws.PTYStarted
			if err := wsjson.Read(ctx, a, &started); err != nil {
				t.Fatal(err)
			}
			if started.Type != ws.TypePTYStarted || started.ControllerID != start.ControllerID {
				t.Fatalf("A lost its start result while B pending: %+v", started)
			}
			var env ws.Envelope
			if err := wsjson.Read(ctx, b, &env); err != nil {
				t.Fatal(err)
			}
			if env.Type != ws.TypePTYPong {
				t.Fatalf("B received A start result: %+v", env)
			}
			if err := wsjson.Read(ctx, a, &env); err != nil {
				t.Fatal(err)
			}
			if env.Type != ws.TypePTYPong {
				t.Fatalf("A start duplicated: %+v", env)
			}
			route := s.PTY.Get(start.SessionID)
			route.mu.Lock()
			retained := route.ControllerID == start.ControllerID && route.PendingControllerID == attach.ControllerID && !route.Provisional
			route.mu.Unlock()
			if !retained {
				t.Fatal("A start consumed B's pending authorization")
			}
		})
	}
}
