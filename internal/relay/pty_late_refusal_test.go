package relay

import (
	"encoding/json"
	"testing"

	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// The reply is already in flight when exit retires the session. Its authenticated
// origin must remain usable without recreating the session or selecting B.
func TestReviewerRefusalAfterExit(t *testing.T) {
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
				input := ws.PTYInput{}
				if err := wsjson.Write(ctx, a, ws.PTYInput{Type: ws.TypePTYInput, SessionID: start.SessionID, Data: "encrypted"}); err != nil {
					t.Fatal(err)
				}
				if err := wsjson.Read(ctx, wing, &input); err != nil {
					t.Fatal(err)
				}
				if input.RequestID == "" {
					t.Fatal("input origin missing")
				}
				origin := input.RequestID
				controller := start.ControllerID
				target, other := a, b
				socket, otherSocket := aSocket, bSocket
				if operation == ws.TypePTYAttach {
					// Decode the actual production attach without requiring a new protocol
					// field at compile time on the historical negative oracle.
					data, _ := json.Marshal(attach)
					var metadata struct {
						RequestID string `json:"request_id"`
					}
					_ = json.Unmarshal(data, &metadata)
					origin, controller = metadata.RequestID, attach.ControllerID
					target, other, socket, otherSocket = b, a, bSocket, aSocket
				}
				initialForward(t, s, start.SessionID, ws.PTYExited{Type: ws.TypePTYExited, SessionID: start.SessionID})
				var env ws.Envelope
				if err := wsjson.Read(ctx, a, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYExited {
					t.Fatalf("exit: %+v", env)
				}
				if err := wsjson.Read(ctx, b, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYExited || s.PTY.Get(start.SessionID) != nil {
					t.Fatal("exit did not retire route")
				}
				refusal := ws.ErrorMsg{Type: ws.TypeError, Operation: operation, SessionID: start.SessionID, ControllerID: controller, RequestID: origin, Code: "session_not_found", Message: "session not found; message was not delivered"}
				// A different wing/session, an unscoped token and ordinary output cannot
				// use a retired route. The legitimate reply then fences these negatives.
				data, _ := json.Marshal(refusal)
				s.forwardPTYToBrowser(start.SessionID, "stranger", data)
				wrong := refusal
				wrong.SessionID = "unrelated"
				data, _ = json.Marshal(wrong)
				s.forwardPTYToBrowser(wrong.SessionID, "wing", data)
				wrong = refusal
				wrong.RequestID = s.browserReplyID(socket)
				data, _ = json.Marshal(wrong)
				s.forwardPTYToBrowser(start.SessionID, "wing", data)
				initialForward(t, s, start.SessionID, ws.PTYOutput{Type: ws.TypePTYOutput, SessionID: start.SessionID, Data: "late output"})
				initialForward(t, s, start.SessionID, refusal)
				initialFence(t, s, socket)
				initialFence(t, s, otherSocket)
				var got ws.ErrorMsg
				if err := wsjson.Read(ctx, target, &got); err != nil {
					t.Fatal(err)
				}
				if got != refusal {
					t.Fatalf("waiting input refusal lost after exit: got %+v, want %+v", got, refusal)
				}
				if err := wsjson.Read(ctx, target, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYPong {
					t.Fatalf("unauthenticated late reply: %+v", env)
				}
				if err := wsjson.Read(ctx, other, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYPong {
					t.Fatalf("refusal reached replacement: %+v", env)
				}
				s.untrackBrowser(socket)
				initialForward(t, s, start.SessionID, refusal)
				initialFence(t, s, otherSocket)
				if err := wsjson.Read(ctx, other, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type != ws.TypePTYPong {
					t.Fatalf("closed origin fell back: %+v", env)
				}
				if s.browserForReplyID(origin) != nil {
					t.Fatal("closed origin retained")
				}
				s.untrackBrowser(otherSocket)
				_ = s.browserForReplyID(input.RequestID)
				s.browserMu.Lock()
				remaining := len(s.PTY.browserReplies)
				s.browserMu.Unlock()
				if remaining != 0 {
					t.Fatalf("closed origins retained: %d", remaining)
				}
			})
		}
	}
}
