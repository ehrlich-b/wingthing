package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestClientAttachesSessionsCreatedAfterRegistration(t *testing.T) {
	for _, test := range []struct {
		name        string
		available   bool
		hostedRelay string
		wantCalls   int32
		wantError   string
	}{
		{name: "late local session", available: true, wantCalls: 1},
		{name: "expired local session", wantCalls: 2, wantError: "session not found or no longer running"},
		{name: "relay denied before reclaim", available: true, hostedRelay: HostedRelayDeny, wantError: "hosted relay"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			responses := make(chan map[string]any, 2)
			forwarded := make(chan PTYAttach, 2)
			var reclaimCalls atomic.Int32
			client := &Client{Token: "fixture", WingID: "wing", HostedRelay: test.hostedRelay}
			client.OnPTYReclaim = func(ctx context.Context, sessionID string) {
				reclaimCalls.Add(1)
				if sessionID != "late-session" {
					t.Errorf("reclaimed unexpected session %q", sessionID)
					return
				}
				if !test.available {
					return
				}
				write, input, cleanup, registered := client.RegisterPTYSession(ctx, sessionID)
				if !registered {
					t.Error("late session was already registered")
					return
				}
				go func() {
					defer cleanup()
					for {
						select {
						case <-ctx.Done():
							return
						case data := <-input:
							var attach PTYAttach
							if err := json.Unmarshal(data, &attach); err != nil {
								t.Error(err)
								return
							}
							forwarded <- attach
							if err := write(PTYStarted{Type: TypePTYStarted, SessionID: sessionID, ViewerID: attach.ViewerID}); err != nil {
								t.Error(err)
								return
							}
						}
					}
				}()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				if _, _, err := conn.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				if err := wsjson.Write(ctx, conn, RegisteredMsg{Type: TypeRegistered, WingID: "wing"}); err != nil {
					t.Error(err)
					return
				}
				for _, viewer := range []string{"first", "reconnect"} {
					attach := PTYAttach{Type: TypePTYAttach, SessionID: "late-session", ViewerID: viewer, UserID: "alice", OrgRole: "member", PublicKey: "browser-key", AuthToken: "passkey-proof"}
					if err := wsjson.Write(ctx, conn, attach); err != nil {
						t.Error(err)
						return
					}
					var response map[string]any
					if err := wsjson.Read(ctx, conn, &response); err != nil {
						t.Errorf("attach produced no response: %v", err)
						return
					}
					responses <- response
				}
				<-ctx.Done()
			}))
			defer server.Close()
			client.RoostURL = strings.Replace(server.URL, "http://", "ws://", 1)
			done := make(chan struct{})
			go func() { _, _ = client.connectAndServe(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			for _, viewer := range []string{"first", "reconnect"} {
				select {
				case response := <-responses:
					if response["session_id"] != "late-session" || response["viewer_id"] != viewer {
						t.Fatalf("uncorrelated attach response: %#v", response)
					}
					if test.wantError != "" {
						message, _ := response["message"].(string)
						if response["type"] != TypeError || !strings.Contains(message, test.wantError) {
							t.Fatalf("attach error = %#v", response)
						}
					} else {
						if response["type"] != TypePTYStarted {
							t.Fatalf("attach response = %#v", response)
						}
						attach := <-forwarded
						if attach.UserID != "alice" || attach.OrgRole != "member" || attach.PublicKey != "browser-key" || attach.AuthToken != "passkey-proof" || attach.ViewerID != viewer {
							t.Fatalf("reclaim changed the authorization envelope: %#v", attach)
						}
					}
				case <-ctx.Done():
					t.Fatal("attach hung without output or an explicit error")
				}
			}
			if got := reclaimCalls.Load(); got != test.wantCalls {
				t.Fatalf("reclaim calls = %d, want %d", got, test.wantCalls)
			}
		})
	}
}
