package wing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Exercise the real wing consumer and relay cleanup, ending at both browser
// sockets. Fences hold authentication or an input refusal while more work is
// admitted, so an exit must wait for the queued outcomes before removing routing.
func TestQueuedRefusalsPrecedeSessionExit(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, channel := range []string{"stable", "preview"} {
		for _, mode := range []string{"auth", "web", "reclaimed"} {
			t.Run(channel+"/"+mode, func(t *testing.T) {
				config.ReleaseChannel = channel
				f := newSessionTransportFixture(t)
				store, err := relay.OpenRelay(":memory:")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if err = store.CreateUser("alice"); err != nil {
					t.Fatal(err)
				}
				if err = store.CreateDeviceToken("exit-token", "alice", f.cfg.WingID, nil); err != nil {
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
				ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
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
				wing := dial("/ws/wing?token=exit-token")
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
				send(wing, ws.WingRegister{Type: ws.TypeWingRegister, WingID: f.cfg.WingID})
				var registered ws.RegisteredMsg
				read(wing, &registered)
				if registered.Type != ws.TypeRegistered {
					t.Fatalf("registration: %+v", registered)
				}
				a, b := dial("/ws/pty?token=exit-token&wing_id="+f.cfg.WingID), dial("/ws/pty?token=exit-token&wing_id="+f.cfg.WingID)
				publicKey := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
				var start ws.PTYStart
				if mode == "reclaimed" {
					id := callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})["session"].(string)
					// Stop the fixture's independent reclaimed consumer; this test owns admission.
					f.stopRegistration()
					send(a, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, PublicKey: publicKey, Takeover: true})
					var attach ws.PTYAttach
					read(wing, &attach)
					start = ws.PTYStart{SessionID: id, ControllerID: attach.ControllerID}
					// Retain the first attach for the reclaimed consumer below.
					start.PublicKey = publicKey
				} else {
					send(a, ws.PTYStart{Type: ws.TypePTYStart, Agent: "claude", CWD: f.work, PublicKey: publicKey})
					read(wing, &start)
				}
				registry := &ws.PTYRegistry{}
				input, retire, ok := registry.Register(start.SessionID)
				if !ok {
					t.Fatal("register")
				}
				defer retire()
				client := &ws.Client{PTYRoutes: registry}
				admit := func(data []byte) {
					t.Helper()
					if denied := client.PushPTYInput(start.SessionID, data); denied != nil {
						t.Fatalf("admission: %+v", denied)
					}
				}
				forward := func() { t.Helper(); var data json.RawMessage; read(wing, &data); admit(data) }
				held, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				consumerCtx := ws.WithPTYInputRetirement(ctx, func() { retire(); unblock() })
				var holdOnce sync.Once
				write := func(v any) error {
					shouldHold := false
					switch msg := v.(type) {
					case ws.PasskeyChallenge:
						shouldHold = mode == "auth"
					case ws.ErrorMsg:
						shouldHold = mode != "auth" && msg.Operation == ws.TypePTYInput && msg.Code == "input_decrypt_failed"
					}
					if err := wsjson.Write(ctx, wing, v); err != nil {
						return err
					}
					if shouldHold {
						holdOnce.Do(func() { close(held); <-release })
					}
					return nil
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					if mode == "reclaimed" {
						// Existing egg transport, actual reclaimed input and output workers.
						ec, err := egg.Dial(f.cfg.Dir+"/eggs/"+start.SessionID+"/egg.sock", f.cfg.Dir+"/eggs/"+start.SessionID+"/egg.token")
						if err != nil {
							t.Error(err)
							return
						}
						defer ec.Close()
						handleReclaimedPTY(consumerCtx, f.cfg, ec, start.SessionID, f.cfg.Dir+"/eggs/"+start.SessionID, write, input, f.wc.Clone(), nil, f.sessions.AuthCache, auth.PasskeyPolicy{}, 0, nil, f.sessions)
					} else {
						keys := []config.AllowKey{}
						if mode == "auth" {
							keys = []config.AllowKey{{UserID: "alice", Key: "invalid-fixture-key"}}
						}
						handlePTYSession("test", consumerCtx, f.cfg, f.wc.Clone(), start, write, input, egg.DefaultEggConfig(), false, false, &keys, f.sessions.AuthCache, auth.PasskeyPolicy{}, 0, 0, nil, nil, nil, false, f.sessions)
					}
				}()
				defer func() {
					cancel()
					unblock()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("consumer did not stop")
					}
				}()
				if mode == "reclaimed" {
					data, _ := json.Marshal(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: start.SessionID, ControllerID: start.ControllerID, PublicKey: publicKey, UserID: "alice", OrgRole: "owner", Takeover: true})
					admit(data)
				}
				var env ws.Envelope
				if mode == "auth" {
					read(a, &env)
					if env.Type != ws.TypePasskeyChallenge {
						t.Fatalf("challenge: %+v", env)
					}
					originAwait(t, held)
					send(a, ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: start.SessionID})
					forward()
				} else {
					read(a, &env)
					if env.Type != ws.TypePTYStarted {
						t.Fatalf("start: %+v", env)
					}
					// An explicitly refused frame holds the sole consumer inside its writer.
					// Retirement releases it only after output shutdown has begun.
					send(a, ws.PTYInput{Type: ws.TypePTYInput, SessionID: start.SessionID, Data: "invalid"})
					forward()
					originAwait(t, held)
					for {
						read(a, &env)
						if env.Type != ws.TypePTYOutput {
							break
						}
					}
					if env.Type != ws.TypeError {
						t.Fatalf("hold refusal: %+v", env)
					}

				}
				const count = 3
				for range count {
					send(a, ws.PTYInput{Type: ws.TypePTYInput, SessionID: start.SessionID, Data: "encrypted-before-exit"})
					forward()
				}
				send(b, ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: start.SessionID, PublicKey: publicKey})
				forward()
				if mode == "auth" {
					unblock()
				} else {
					f.egg(start.SessionID).stop.Do(func() { close(f.egg(start.SessionID).killed) })
				}
				for range count {
					var denied ws.ErrorMsg
					for {
						read(a, &denied)
						if denied.Type != ws.TypePTYOutput {
							break
						}
					}
					if denied.Type != ws.TypeError || denied.Operation != ws.TypePTYInput || denied.SessionID != start.SessionID || denied.ControllerID != start.ControllerID || denied.RequestID == "" || denied.Code == "" {
						t.Fatalf("queued input refusal lost before exit: %+v", denied)
					}
				}
				var denied ws.ErrorMsg
				read(b, &denied)
				if denied.Type != ws.TypeError || denied.Operation != ws.TypePTYAttach || denied.SessionID != start.SessionID || denied.ControllerID == start.ControllerID {
					t.Fatalf("queued attach refusal lost before exit: %+v", denied)
				}
				// Output workers may finish replay while the input queue drains.
				for {
					read(a, &env)
					if env.Type != ws.TypePTYOutput {
						break
					}
				}
				if env.Type != ws.TypePTYExited {
					t.Fatalf("exit missing after refusals: %+v", env)
				}
				send(b, ws.Envelope{Type: ws.TypePTYPing})
				read(b, &env)
				if env.Type != ws.TypePTYPong {
					t.Fatalf("A's outcomes leaked to B: %+v", env)
				}

				originAwait(t, done)
				t.Logf("%d queued input refusals and pending attach refusal reached original browsers before exit", count)
			})
		}
	}
}
