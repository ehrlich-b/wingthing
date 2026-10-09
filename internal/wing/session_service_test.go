package wing

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/tunnel"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
	"google.golang.org/grpc"
)

// The providers are protocol fixtures. Wing routing, encryption, tunnel and MCP
// dispatch, ownership files, and egg-client RPCs are exercised unchanged.
type sessionEggFixture struct {
	pb.UnimplementedEggServer
	mu       sync.Mutex
	snapshot []byte
	input    chan []byte
	killed   chan struct{}
	stop     sync.Once
	dir      string
	agent    string
}

func (f *sessionEggFixture) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Agent: f.agent, RenderedConfig: "fixture-policy"}, nil
}
func (f *sessionEggFixture) Kill(context.Context, *pb.KillRequest) (*pb.KillResponse, error) {
	f.stop.Do(func() { _ = os.Remove(filepath.Join(f.dir, "egg.pid")); close(f.killed) })
	return &pb.KillResponse{}, nil
}
func (f *sessionEggFixture) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.mu.Lock()
	snapshot := append([]byte(nil), f.snapshot...)
	f.mu.Unlock()
	if err = stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: snapshot}}); err != nil {
		return err
	}
	incoming := make(chan *pb.SessionMsg)
	failure := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				failure <- err
				return
			}
			select {
			case incoming <- msg:
			case <-stream.Context().Done():
				return
			}
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-f.killed:
			return stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_ExitCode{ExitCode: 0}})
		case err := <-failure:
			if err == io.EOF {
				return nil
			}
			return err
		case msg := <-incoming:
			switch payload := msg.Payload.(type) {
			case *pb.SessionMsg_Input:
				data := append([]byte(nil), payload.Input...)
				f.mu.Lock()
				f.snapshot = append(f.snapshot, data...)
				f.mu.Unlock()
				f.input <- data
			case *pb.SessionMsg_Detach:
				return nil
			}
		}
	}
}

type sessionTransportFixture struct {
	t                 *testing.T
	ctx               context.Context
	cfg               *config.Config
	wc                *config.WingConfig
	home, work        string
	sessions          *wingsession.Service
	client            *ws.Client
	relay             *websocket.Conn
	replies           chan json.RawMessage
	browserKey        *ecdh.PrivateKey
	wingKey           *ecdh.PrivateKey
	ptyKey, tunnelKey cipher.AEAD
	eggs              map[string]*sessionEggFixture
	mu                sync.Mutex
	request           int
}

func newSessionTransportFixture(t *testing.T) *sessionTransportFixture {
	t.Helper()
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	// Short alias keeps Darwin's Unix socket path within sockaddr_un. All state
	// lives in t.TempDir; the checkout alias is removed with the fixture.
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(scratch, "s")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	t.Setenv("WINGTHING_DIR", alias)
	cfg := &config.Config{Dir: alias, WingID: "fixture-wing", DefaultAgent: "claude"}
	if _, err = auth.EnsureKeyPair(alias); err != nil {
		t.Fatal(err)
	}
	wingKey, err := auth.LoadPrivateKey(alias)
	if err != nil {
		t.Fatal(err)
	}
	browserKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wingPub := base64.StdEncoding.EncodeToString(wingKey.PublicKey().Bytes())
	ptyKey, err := auth.DeriveSharedKey(browserKey, wingPub, "wt-pty")
	if err != nil {
		t.Fatal(err)
	}
	tunnelKey, err := auth.DeriveSharedKey(browserKey, wingPub, "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	f := &sessionTransportFixture{t: t, ctx: ctx, cfg: cfg, home: home, work: work, wc: &config.WingConfig{Paths: config.PathList{{Path: work}}}, browserKey: browserKey, wingKey: wingKey, ptyKey: ptyKey, tunnelKey: tunnelKey, replies: make(chan json.RawMessage, 64), eggs: map[string]*sessionEggFixture{}}
	policy := egg.DefaultEggConfig()
	cache := auth.NewAuthCache()
	f.sessions = &wingsession.Service{Config: cfg, Home: home, AuthCache: cache, Inventory: ListAliveEggSessions, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: f.wc.Clone(), Egg: policy} }, Spawn: f.spawn}
	connected := make(chan struct{})
	clientDone := make(chan struct{})
	relayReady := make(chan *websocket.Conn, 1)
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		var reg ws.WingRegister
		if err = wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		if err = wsjson.Write(ctx, conn, ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: cfg.WingID}); err != nil {
			return
		}
		relayReady <- conn
		for {
			var msg json.RawMessage
			if err = wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			select {
			case f.replies <- msg:
			case <-ctx.Done():
				return
			}
		}
	}))
	client := &ws.Client{RoostURL: strings.Replace(relayServer.URL, "http://", "ws://", 1), WingID: cfg.WingID, Token: "fixture-token", OnRegistered: func(ws.RegisteredMsg) { close(connected) }}
	f.client = client
	configureSessionRegistration(f.sessions, ctx, client, func() auth.PasskeyPolicy { return auth.PasskeyPolicy{} }, func() []*config.ToolConfig { return nil })
	client.OnPTY = func(ctx context.Context, start ws.PTYStart, write ws.PTYWriteFunc, input <-chan []byte) {
		keys := []config.AllowKey{}
		handlePTYSession("test", ctx, cfg, f.wc.Clone(), start, write, input, policy, false, false, &keys, cache, auth.PasskeyPolicy{}, 0, 0, nil, nil, nil, false, f.sessions)
	}
	go func() { _ = client.Run(ctx); close(clientDone) }()
	t.Cleanup(func() { cancel(); <-clientDone; relayServer.Close() })
	f.relay = <-relayReady
	<-connected
	return f
}

func (f *sessionTransportFixture) spawn(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
	dir := filepath.Join(f.cfg.Dir, "eggs", opts.SessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	for name, value := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.token": "fixture-token", "egg.meta": "kind=" + opts.Egg.Kind + "\nagent=" + opts.Agent + "\ncwd=" + launch.CWD + "\nprovider_home=" + f.home + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			return nil, err
		}
	}
	if err := eggclient.WriteEggOwner(dir, launch.Identity.UserID, launch.Identity.Email); err != nil {
		return nil, err
	}
	if err := eggclient.WriteSessionPrincipal(dir, opts.Egg.Principal); err != nil {
		return nil, err
	}
	ef := &sessionEggFixture{snapshot: []byte("fixture ready\n"), input: make(chan []byte, 16), killed: make(chan struct{}), dir: dir, agent: opts.Agent}
	listener, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer()
	pb.RegisterEggServer(server, ef)
	go func() { _ = server.Serve(listener) }()
	f.t.Cleanup(server.Stop)
	f.mu.Lock()
	f.eggs[opts.SessionID] = ef
	f.mu.Unlock()
	return egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
}
func (f *sessionTransportFixture) egg(id string) *sessionEggFixture {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.eggs[id]
}
func (f *sessionTransportFixture) send(v any) {
	f.t.Helper()
	if err := wsjson.Write(f.ctx, f.relay, v); err != nil {
		f.t.Fatal(err)
	}
}
func (f *sessionTransportFixture) next(kind string) json.RawMessage {
	f.t.Helper()
	for {
		select {
		case msg := <-f.replies:
			var env ws.Envelope
			if err := json.Unmarshal(msg, &env); err != nil {
				f.t.Fatal(err)
			}
			if env.Type == kind {
				return msg
			}
		case <-f.ctx.Done():
			f.t.Fatal(f.ctx.Err())
		}
	}
}
func (f *sessionTransportFixture) tunnel(user string, inner map[string]any) map[string]any {
	f.t.Helper()
	f.request++
	data, err := json.Marshal(inner)
	if err != nil {
		f.t.Fatal(err)
	}
	payload, err := auth.Encrypt(f.tunnelKey, data)
	if err != nil {
		f.t.Fatal(err)
	}
	keys := []config.AllowKey{}
	defaultEgg := egg.DefaultEggConfig()
	var mu, eggMu sync.Mutex
	var response ws.TunnelResponse
	req := ws.TunnelRequest{Type: ws.TypeTunnelRequest, RequestID: strconv.Itoa(f.request), WingID: f.cfg.WingID, SenderPub: base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes()), SenderUserID: user, SenderEmail: user + "@example.com", SenderOrgRole: "member", Payload: payload}
	tunnel.HandleTunnelRequest(tunnel.References{Version: "test", WingCfg: f.wc, WingCfgMu: &mu, AllowedKeys: &keys, WingEggMu: &eggMu, WingEggCfg: &defaultEgg, Sessions: f.sessions, ListAliveEggSessions: ListAliveEggSessions}, f.ctx, f.cfg, req, func(v any) error {
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, &response)
	}, f.sessions.AuthCache, auth.NewChallengeCache(), auth.PasskeyPolicy{}, f.wingKey, f.home, false, false, f.client, nil, nil, false)
	plain, err := auth.Decrypt(f.tunnelKey, response.Payload)
	if err != nil {
		f.t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(plain, &result); err != nil {
		f.t.Fatal(err)
	}
	return result
}

type sessionMCPCall func(string, json.RawMessage) (map[string]any, bool, error)

func (f *sessionTransportFixture) mcp(mode, user string) sessionMCPCall {
	f.t.Helper()
	if mode == "http" {
		tools := localmcp.RoostNativeMCPToolsWithSessions("test", f.cfg, false, func() *wingsession.Service { return f.sessions }, func() (*config.WingConfig, *egg.EggConfig) { p := f.sessions.Policy(); return p.Wing, p.Egg })
		return func(name string, args json.RawMessage) (map[string]any, bool, error) {
			for _, tool := range tools {
				if tool.Name == name {
					return tool.Call(f.ctx, mcppkg.Principal{UserID: user, Email: user + "@example.com", ClientID: "fixture"}, args)
				}
			}
			f.t.Fatalf("missing tool %s", name)
			return nil, true, nil
		}
	}
	manager := webrtcpkg.NewPeerManager(nil)
	f.t.Cleanup(manager.Close)
	admission := localmcp.NewMCPAdmissionState()
	admission.Sessions = f.sessions
	manager.OnDC(func(_ string, _ string, ident webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
		localmcp.ServeDirectMCPChannelWithPolicySource("test", f.cfg, f.home, false, admission, ident, dc, func() (*config.WingConfig, []config.AllowKey) { return f.wc.Clone(), nil }, func() *egg.EggConfig { return f.sessions.Policy().Egg })
	})
	client, err := webrtcpkg.NewControlClient("fixture", nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = client.Close() })
	offer, err := client.Offer(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	answer, err := manager.HandleOffer("fixture-"+user, user, user+"@example.com", "owner", nil, offer)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = client.AcceptAnswer(answer); err != nil {
		f.t.Fatal(err)
	}
	if err = client.WaitReady(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return func(name string, args json.RawMessage) (map[string]any, bool, error) {
		return client.Call(f.ctx, name, args)
	}
}
func callSessionMCP(t *testing.T, call sessionMCPCall, name string, args any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, isError, err := call(name, encoded)
	if err != nil || isError {
		t.Fatalf("%s: result=%v isError=%v err=%v", name, result, isError, err)
	}
	return result
}

func TestMCPStartVisibleAndAttachableViaWebAPI(t *testing.T) {
	for _, mode := range []string{"http", "direct"} {
		t.Run(mode, func(t *testing.T) {
			f := newSessionTransportFixture(t)
			owner := f.mcp(mode, "alice")
			result := callSessionMCP(t, owner, "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
			id := result["session"].(string)
			other := f.mcp(mode, "bob")
			foreignMCP := callSessionMCP(t, other, "terminal_list", map[string]any{})
			encoded, _ := json.Marshal(foreignMCP["sessions"])
			var hidden []any
			if err := json.Unmarshal(encoded, &hidden); err != nil || len(hidden) != 0 {
				t.Fatalf("foreign MCP inventory: %v %v", foreignMCP, err)
			}
			for _, tool := range []string{"terminal_read", "terminal_send", "terminal_stop"} {
				args, _ := json.Marshal(map[string]any{"session": id})
				if result, isError, err := other(tool, args); err != nil || !isError {
					t.Fatalf("foreign MCP %s admitted: %v %v", tool, result, err)
				}
			}

			if !f.client.HasPTYSession(id) {
				t.Fatal("start acknowledged before wing input registration")
			}
			inventory := f.tunnel("alice", map[string]any{"type": "sessions.list"})
			sessions := inventory["sessions"].([]any)
			if len(sessions) != 1 || sessions[0].(map[string]any)["session_id"] != id {
				t.Fatalf("MCP session absent from web: %v", inventory)
			}
			foreign := f.tunnel("bob", map[string]any{"type": "sessions.list"})
			if len(foreign["sessions"].([]any)) != 0 {
				t.Fatalf("foreign web inventory: %v", foreign)
			}
			key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
			f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, PublicKey: key, UserID: "bob", OrgRole: "member", Email: "bob@example.com", ViewerID: "foreign"})
			var denied ws.ErrorMsg
			if err := json.Unmarshal(f.next(ws.TypeError), &denied); err != nil {
				t.Fatal(err)
			}
			if denied.SessionID != id {
				t.Fatalf("wrong attach denial: %+v", denied)
			}
			stopped := f.tunnel("bob", map[string]any{"type": "pty.kill", "session_id": id})
			if stopped["error"] == nil {
				t.Fatal("foreign web stop admitted")
			}
			f.send(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: id, PublicKey: key, UserID: "alice", OrgRole: "member", Email: "alice@example.com", ControllerID: "browser-owner"})
			var started ws.PTYStarted
			if err := json.Unmarshal(f.next(ws.TypePTYStarted), &started); err != nil {
				t.Fatal(err)
			}
			if started.SessionID != id {
				t.Fatalf("wrong attachment: %+v", started)
			}
			encrypted, err := auth.Encrypt(f.ptyKey, []byte("from web\r"))
			if err != nil {
				t.Fatal(err)
			}
			f.send(ws.PTYInput{Type: ws.TypePTYInput, SessionID: id, Data: encrypted})
			if got := string(<-f.egg(id).input); got != "from web\r" {
				t.Fatalf("web input=%q", got)
			}
			callSessionMCP(t, owner, "terminal_stop", map[string]any{"session": id})
		})
	}
}

func TestSessionStartRequiresWingRegistrationAndRollsBackFailure(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(strconv.FormatBool(missing), func(t *testing.T) {
			f := newSessionTransportFixture(t)
			if missing {
				f.sessions.Register = nil
			} else {
				f.sessions.Register = func(string) error { return io.ErrClosedPipe }
			}
			call := f.mcp("http", "alice")
			args, _ := json.Marshal(map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
			if result, isError, err := call("terminal_start", args); err != nil || !isError {
				t.Fatalf("unregistered launch accepted: %v %v", result, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if missing {
				if len(f.eggs) != 0 {
					t.Fatal("spawned without wing routing")
				}
				return
			}
			if len(f.eggs) != 1 {
				t.Fatalf("expected failed registration of one egg, got %d", len(f.eggs))
			}
			for _, ef := range f.eggs {
				select {
				case <-ef.killed:
				default:
					t.Fatal("registration failure left an untracked egg")
				}
			}
		})
	}
}
