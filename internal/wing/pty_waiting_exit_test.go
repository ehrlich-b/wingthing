package wing

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestWaitingQueueSenderRefusalAfterExit(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			config.ReleaseChannel = channel
			store, err := relay.OpenRelay(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err = store.CreateUser("alice"); err != nil {
				t.Fatal(err)
			}
			if err = store.CreateDeviceToken("waiting-token", "alice", "wing", nil); err != nil {
				t.Fatal(err)
			}
			server := relay.NewServer(store, relay.ServerConfig{})
			key, _, err := relay.GenerateECKey()
			if err != nil {
				t.Fatal(err)
			}
			server.SetJWTKey(key)
			httpServer := httptest.NewServer(server)
			t.Cleanup(httpServer.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			dial := func(path string) *websocket.Conn {
				t.Helper()
				c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.CloseNow() })
				return c
			}
			send := func(c *websocket.Conn, v any) {
				t.Helper()
				if err := wsjson.Write(ctx, c, v); err != nil {
					t.Fatal(err)
				}
			}
			read := func(c *websocket.Conn, v any) {
				t.Helper()
				if err := wsjson.Read(ctx, c, v); err != nil {
					t.Fatal(err)
				}
			}
			wing := dial("/ws/wing?token=waiting-token")
			send(wing, ws.WingRegister{Type: ws.TypeWingRegister, WingID: "wing"})
			var registered ws.RegisteredMsg
			read(wing, &registered)
			if registered.Type != ws.TypeRegistered {
				t.Fatalf("register: %+v", registered)
			}
			browser := dial("/ws/pty?token=waiting-token&wing_id=wing")
			const id = "waiting-exit"
			send(browser, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id})
			var attach ws.PTYAttach
			read(wing, &attach)
			send(wing, ws.PTYStarted{Type: ws.TypePTYStarted, SessionID: id, ControllerID: attach.ControllerID})
			var started ws.PTYStarted
			read(browser, &started)
			if started.Type != ws.TypePTYStarted {
				t.Fatalf("start: %+v", started)
			}
			send(browser, ws.PTYInput{Type: ws.TypePTYInput, SessionID: id, Data: "encrypted-before-exit"})
			var input json.RawMessage
			read(wing, &input)
			// A tunnel response fences the same production wing dispatcher even after
			// pty.exited has removed all session routes.
			send(browser, ws.TunnelRequest{Type: ws.TypeTunnelRequest, WingID: "wing", RequestID: "exit-fence"})
			var tunnel ws.TunnelRequest
			read(wing, &tunnel)
			if tunnel.RequestID != "exit-fence" {
				t.Fatalf("tunnel: %+v", tunnel)
			}
			var events []any
			synctest.Test(t, func(t *testing.T) {
				registry := &ws.PTYRegistry{}
				queue, retire, ok := registry.Register(id)
				if !ok {
					t.Fatal("register")
				}
				defer retire()
				client := &ws.Client{PTYRoutes: registry}
				for range 64 {
					if denied := client.PushPTYInput(id, input); denied != nil {
						t.Fatalf("fill: %+v", denied)
					}
				}
				var mu sync.Mutex
				record := func(v any) error { mu.Lock(); events = append(events, v); mu.Unlock(); return nil }
				write, flush := delayPTYExit(record)
				exitSent, done := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(done)
					denied := client.PushPTYInput(id, input)
					<-exitSent // Model an in-flight socket write losing the race to exit.
					if denied == nil {
						t.Error("retired full queue accepted waiting input")
						return
					}
					if denied.Code != "session_not_found" {
						t.Errorf("sender did not wake on retirement: %+v", denied)
					}
					_ = write(*denied)
				}()
				// Virtual time stays at zero: the sender is durably blocked in the full
				// queue wait, rather than having used its 250 ms busy-queue timeout.
				before := time.Now()
				synctest.Wait()
				if !time.Now().Equal(before) {
					t.Fatal("sender was not fenced before its timeout")
				}
				_ = write(ws.PTYExited{Type: ws.TypePTYExited, SessionID: id})
				retirePTYInput(ws.WithPTYInputRetirement(context.Background(), retire), id, queue, write)
				flush()
				close(exitSent)
				<-done
				if len(events) != 66 {
					t.Fatalf("outcomes: %d want 66", len(events))
				}
				if _, ok := events[64].(ws.PTYExited); !ok {
					t.Fatalf("exit fence: %+v", events[64])
				}
			})
			// Replay each recorded outcome to a reading browser. Sending all 67
			// frames before reading would intentionally exceed the relay's bounded
			// slow-browser queue, which is a different behavior from input retirement.
			// The full input queue and its blocked sender were exercised above.
			for index, event := range events {
				send(wing, event)
				if index == 64 {
					var exited ws.PTYExited
					read(browser, &exited)
					if expected := event.(ws.PTYExited); exited != expected {
						t.Fatalf("exit: %+v, want %+v", exited, expected)
					}
					continue
				}
				var refusal ws.ErrorMsg
				read(browser, &refusal)
				expected := event.(ws.ErrorMsg)
				code := "session_not_ready"
				if index == 65 {
					code = "session_not_found"
				}
				if refusal != expected || refusal.Type != ws.TypeError || refusal.Operation != ws.TypePTYInput || refusal.SessionID != id || refusal.Code != code {
					t.Fatalf("refusal %d: %+v, want %+v", index, refusal, expected)
				}
			}
			send(wing, ws.ErrorMsg{Type: ws.TypeError, RequestID: tunnel.RequestID, Operation: ws.TypeTunnelRequest, Message: "exit-fence"})
			var fence ws.ErrorMsg
			read(browser, &fence)
			if fence.RequestID != tunnel.RequestID || fence.Message != "exit-fence" {
				t.Fatalf("dispatch fence: %+v", fence)
			}
			t.Log("64 drained refusals and the full-queue sender's late refusal reached their original socket")
		})
	}
}
