package wing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Fence the consumer at its started reply, close its actual egg transport, and
// admit all frames before releasing it. The first Send must fail; every already
// admitted frame still needs its own refusal, carrying its original route.
func TestQueuedInputsRefusedAfterEggWriteFailure(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	config.ReleaseChannel = "stable"
	for _, mode := range []string{"web", "reclaimed"} {
		t.Run(mode, func(t *testing.T) {
			f := newSessionTransportFixture(t)
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			wrap := func(write ws.PTYWriteFunc) ws.PTYWriteFunc {
				return func(v any) error {
					if _, ok := v.(ws.PTYStarted); ok {
						close(started)
						<-release
					}
					return write(v)
				}
			}
			handler := f.client.OnPTY
			f.client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
				handler(ctx, start, wrap(write), input)
			}
			// Reclaimed registration captures its output subscriber at configuration.
			f.stopRegistration()
			f.stopRegistration = configureSessionRegistration(f.sessions, f.ctx, f.client.PTYRoutes, wrap(f.client.PTYWriter(f.ctx)), func() auth.PasskeyPolicy { return auth.PasskeyPolicy{} }, func() []*config.ToolConfig { return nil })
			key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
			id := "exit-input"
			if mode == "web" {
				f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, ControllerID: "A", Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: key})
			} else {
				id = callSessionMCP(t, f.mcp("http", "alice"), "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})["session"].(string)
				f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "A", UserID: "alice", OrgRole: "owner", PublicKey: key, Takeover: true})
			}
			originAwait(t, started)
			binding, ok := browserDataChannelBinding(id, key, "alice")
			if !ok {
				t.Fatal("missing binding")
			}
			encrypted, err := auth.Encrypt(f.ptyKey, []byte("not-delivered"))
			if err != nil {
				t.Fatal(err)
			}
			const count = 7
			for i := range count {
				controller := "A"
				if i > 0 {
					controller = fmt.Sprintf("origin-%d", i)
				}
				data, _ := json.Marshal(ws.PTYInput{Type: ws.TypePTYInput, SessionID: id, ControllerID: controller, RequestID: fmt.Sprintf("queued-%d", i), Data: encrypted})
				if denied := f.client.PushPTYInput(id, data); denied != nil {
					t.Fatalf("not admitted: %+v", denied)
				}
			}

			// Direct frames share the same bounded queue but must be refused
			// on their own browser endpoint, never on the relay subscriber.
			var channels sync.Map
			admitted := make(chan struct{})
			directCount := 0
			direct := originChannel(t, id, binding, &channels, func(session string, data []byte) *ws.ErrorMsg {
				denied := f.client.PushPTYInput(session, data)
				directCount++
				if directCount == 2 {
					close(admitted)
				}
				return denied
			})
			t.Cleanup(func() { releaseDataChannelInputOrigins(direct.wing) })
			for range 2 {
				originSend(t, direct.browser, map[string]string{"type": ws.TypePTYInput, "session_id": id, "data": encrypted})
			}
			originAwait(t, admitted)
			if err := binding.client.Close(); err != nil {
				t.Fatal(err)
			}
			unblock()
			seen := map[string]bool{}
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for len(seen) < count {
				select {
				case raw := <-f.replies:
					var reply ws.ErrorMsg
					_ = json.Unmarshal(raw, &reply)
					if reply.Type != ws.TypeError {
						continue
					}
					var n int
					if _, err := fmt.Sscanf(reply.RequestID, "queued-%d", &n); err != nil || n < 0 || n >= count {
						t.Fatalf("wrong request route: %+v", reply)
					}
					controller := "A"
					if n > 0 {
						controller = fmt.Sprintf("origin-%d", n)
					}
					if seen[reply.RequestID] || reply.ControllerID != controller || reply.Operation != ws.TypePTYInput || reply.SessionID != id || reply.Code == "" {
						t.Fatalf("wrong refusal: %+v", reply)
					}
					seen[reply.RequestID] = true
				case <-deadline.C:
					t.Fatalf("queued input lost: refusals=%d want=%d", len(seen), count)
				}
			}

			for range 2 {
				var reply ws.ErrorMsg
				_ = json.Unmarshal(originReceive(t, direct.received), &reply)
				if reply.Type != ws.TypeError || reply.ControllerID != "A" || reply.Operation != ws.TypePTYInput || reply.SessionID != id || reply.Code != "session_not_ready" {
					t.Fatalf("direct refusal escaped its origin: %+v", reply)
				}
			}
			assertNoInputOrigins(t)
			select {
			case data := <-f.egg(id).input:
				t.Fatalf("failed input reached egg: %q", data)
			default:
			}
			if f.client.HasPTYSession(id) {
				t.Fatal("exited consumer still admits input")
			}
			t.Logf("egg write failed; %d admitted inputs individually refused on their own routes", count+2)
		})
	}
}

func TestReviewInitialAuthAttach(t *testing.T) {
	f := newSessionTransportFixture(t)
	keys := []config.AllowKey{{UserID: "alice", Key: "invalid-fixture-key"}}
	f.client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		handlePTYSession("test", ctx, f.cfg, f.wc.Clone(), start, write, input, egg.DefaultEggConfig(), false, false, &keys, f.sessions.AuthCache, auth.PasskeyPolicy{}, 0, 0, nil, nil, nil, false, f.sessions)
	}
	const id = "initial-auth"
	key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
	f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, ControllerID: "A", Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: key})
	f.next(ws.TypePasskeyChallenge)
	f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "B", PublicKey: key, UserID: "alice", OrgRole: "owner"})
	f.send(ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: id, ControllerID: "A"})
	select {
	case raw := <-f.replies:
		var reply ws.ErrorMsg
		_ = json.Unmarshal(raw, &reply)
		if reply.Type != ws.TypeError || reply.ControllerID != "B" || reply.SessionID != id || reply.Operation != ws.TypePTYAttach {
			t.Fatalf("B attach vanished during A authentication: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B attach received no result")
	}
	f.next(ws.TypePTYExited)
	t.Log("B attach explicitly refused during A's initial authentication")
}

// The initial authentication queue must reject responses for a different
// controller without letting them terminate the starter's session.
func TestInitialPasskeyChallengeKeepsController(t *testing.T) {
	f := newSessionTransportFixture(t)
	keys := []config.AllowKey{{UserID: "alice", Key: "invalid-fixture-key"}}
	f.client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		handlePTYSession("test", ctx, f.cfg, f.wc.Clone(), start, write, input, egg.DefaultEggConfig(), false, false, &keys, f.sessions.AuthCache, auth.PasskeyPolicy{}, 0, 0, nil, nil, nil, false, f.sessions)
	}
	const id = "initial-controller"
	key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
	f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, ControllerID: "A", Agent: "claude", CWD: f.work, UserID: "alice", OrgRole: "owner", PublicKey: key})
	var challenge ws.PasskeyChallenge
	if err := json.Unmarshal(f.next(ws.TypePasskeyChallenge), &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.ControllerID != "A" {
		t.Errorf("initial challenge lost starter identity: %+v", challenge)
	}
	f.send(ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: id, ControllerID: "B"})
	f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, ControllerID: "B", PublicKey: key, UserID: "alice", OrgRole: "owner"})
	f.send(ws.PasskeyResponse{Type: ws.TypePasskeyResponse, SessionID: id, ControllerID: "A"})
	var reply ws.Envelope
	raw := originReceive(t, f.replies)
	_ = json.Unmarshal(raw, &reply)
	if reply.Type != ws.TypeError {
		t.Fatalf("B's response ended A's authentication: %s", raw)
	}
	f.next(ws.TypePTYExited)
}
