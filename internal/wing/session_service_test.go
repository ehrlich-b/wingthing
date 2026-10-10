package wing

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The providers are protocol fixtures. Wing routing, encryption, tunnel and MCP
// dispatch, ownership files, and egg-client RPCs are exercised unchanged.
type sessionEggFixture struct {
	pb.UnimplementedEggServer
	mu         sync.Mutex
	snapshot   []byte
	input      chan []byte
	killed     chan struct{}
	killErr    error
	stop       sync.Once
	dir        string
	agent      string
	transcript string
	spool      string
	prompt     string
	sequence   int
}

func (f *sessionEggFixture) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Agent: f.agent, RenderedConfig: "fixture-policy"}, nil
}
func (f *sessionEggFixture) Kill(context.Context, *pb.KillRequest) (*pb.KillResponse, error) {
	f.mu.Lock()
	err := f.killErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
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
				if strings.HasPrefix(string(data), "\x1b[200~") {
					f.prompt = strings.TrimSuffix(strings.TrimPrefix(string(data), "\x1b[200~"), "\x1b[201~")
				}
				if string(data) == "\r" && f.prompt != "" {
					f.sequence++
					if err := f.hook("UserPromptSubmit", f.prompt); err != nil {
						return err
					}
					f.sequence++
					if err := f.hook("Stop", ""); err != nil {
						return err
					}
					f.prompt = ""
				}
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
	for name, value := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.token": "fixture-token", "egg.meta": "kind=" + opts.Egg.Kind + "\nagent=" + opts.Agent + "\ncwd=" + launch.CWD + "\nprovider_home=" + f.home + "\nprovider_session_id=ours\n"} {
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
	if opts.Agent == "claude" {
		ef.transcript = filepath.Join(f.home, ".claude", "projects", strings.ReplaceAll(launch.CWD, "/", "-"), "ours.jsonl")
		if err := os.MkdirAll(filepath.Dir(ef.transcript), 0700); err != nil {
			return nil, err
		}

		ef.spool = filepath.Join(f.home, ".claude", "wingthing-events", opts.SessionID)
		if err := os.MkdirAll(ef.spool, 0700); err != nil {
			return nil, err
		}
		if err := ef.hook("SessionStart", ""); err != nil {
			return nil, err
		}
	}

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

func TestUnreachableSessionStopViaWebAndMCP(t *testing.T) {
	for _, mode := range []string{"web", "http", "direct"} {
		for _, active := range []bool{false, true} {
			t.Run(mode+"/active="+strconv.FormatBool(active), func(t *testing.T) {
				f := newSessionTransportFixture(t)
				const id = "unreachable"
				dir := filepath.Join(f.cfg.Dir, "eggs", id)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				files := map[string]string{"egg.meta": "cwd=" + f.work + "\n", "egg.token": "unreachable-token"}
				if active {
					// A recycled PID is live but does not match this session; stop
					// must clean its metadata without signaling the test process.
					files["egg.pid"] = strconv.Itoa(os.Getpid())
				}
				for name, value := range files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := eggclient.WriteEggOwner(dir, "alice", "alice@example.com"); err != nil {
					t.Fatal(err)
				}
				if err := eggclient.WriteSessionPrincipal(dir, wingsession.UserPrincipal("alice")); err != nil {
					t.Fatal(err)
				}
				stop := func(user string) (map[string]any, bool, error) {
					if mode == "web" {
						result := f.tunnel(user, map[string]any{"type": "pty.kill", "session_id": id})
						return result, result["error"] != nil, nil
					}
					args, _ := json.Marshal(map[string]any{"session": id})
					return f.mcp(mode, user)("terminal_stop", args)
				}
				if result, isError, err := stop("bob"); err != nil || !isError {
					t.Fatalf("foreign stop admitted: %v, %v, %v", result, isError, err)
				}
				for name, want := range files {
					data, err := os.ReadFile(filepath.Join(dir, name))
					if err != nil || string(data) != want {
						t.Fatalf("foreign stop changed %s: %q, %v", name, data, err)
					}
				}
				if result, isError, err := stop("alice"); err != nil || isError {
					t.Fatalf("owned stop failed: %v, %v, %v", result, isError, err)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("stale session directory remains: %v", err)
				}
			})
		}
	}
}

func TestSessionStopPreservesMetadataOnRPCDenial(t *testing.T) {
	f := newSessionTransportFixture(t)
	owner := f.mcp("http", "alice")
	result := callSessionMCP(t, owner, "terminal_start", map[string]any{"command": []string{"fixture-command"}, "cwd": f.work})
	id := result["session"].(string)
	ef := f.egg(id)
	ef.mu.Lock()
	ef.killErr = status.Error(codes.PermissionDenied, "fixture kill denied")
	ef.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"session": id})
	if result, isError, err := owner("terminal_stop", args); err != nil || !isError {
		t.Fatalf("RPC denial ignored: %v, %v, %v", result, isError, err)
	}
	if result := f.tunnel("alice", map[string]any{"type": "pty.kill", "session_id": id}); result["error"] == nil {
		t.Fatalf("web RPC denial ignored: %v", result)
	}
	for _, name := range []string{"egg.pid", "egg.meta", "egg.owner", "egg.token", "egg.sock"} {
		if _, err := os.Stat(filepath.Join(ef.dir, name)); err != nil {
			t.Fatalf("RPC denial cleaned %s: %v", name, err)
		}
	}
	select {
	case <-ef.killed:
		t.Fatal("RPC denial killed the session")
	default:
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

func TestWebStartControllableViaMCP(t *testing.T) {
	for _, mode := range []string{"http", "direct"} {
		t.Run(mode, func(t *testing.T) {
			f := newSessionTransportFixture(t)
			key := base64.StdEncoding.EncodeToString(f.browserKey.PublicKey().Bytes())
			id := "web-fixture"
			f.send(ws.PTYStart{Type: ws.TypePTYStart, SessionID: id, Agent: "claude", CWD: f.work, UserID: "alice", Email: "alice@example.com", OrgRole: "owner", PublicKey: key, Cols: 80, Rows: 24})
			var started ws.PTYStarted
			if err := json.Unmarshal(f.next(ws.TypePTYStarted), &started); err != nil {
				t.Fatal(err)
			}
			if started.SessionID != id {
				t.Fatalf("web did not start requested session: %+v", started)
			}
			owner := f.mcp(mode, "alice")
			read := callSessionMCP(t, owner, "terminal_read", map[string]any{"session": id})
			if read["ansi"] != "fixture ready\n" {
				t.Fatalf("MCP snapshot of web session: %v", read)
			}
			lifecycle := callSessionMCP(t, owner, "session_read", map[string]any{"session": id})
			if lifecycle["session"] != id {
				t.Fatalf("MCP lifecycle of web session: %v", lifecycle)
			}
			other := f.mcp(mode, "bob")
			inventory := callSessionMCP(t, other, "terminal_list", map[string]any{})
			encoded, _ := json.Marshal(inventory["sessions"])
			var hidden []any
			if err := json.Unmarshal(encoded, &hidden); err != nil || len(hidden) != 0 {
				t.Fatalf("foreign MCP listed web session: %v %v", inventory, err)
			}
			for _, tool := range []string{"terminal_read", "terminal_send", "terminal_stop", "session_read", "session_prompt"} {
				args := map[string]any{"session": id}
				if tool == "session_prompt" {
					args["request_id"] = "denied"
					args["input"] = "not yours"
				}
				encoded, _ := json.Marshal(args)
				if result, isError, err := other(tool, encoded); err != nil || !isError {
					t.Fatalf("foreign MCP %s admitted: %v %v", tool, result, err)
				}
			}
			send := callSessionMCP(t, owner, "terminal_send", map[string]any{"session": id, "input": "from MCP", "enter": true})
			if send["bytes_sent"] != 9 && send["bytes_sent"] != float64(9) {
				t.Fatalf("MCP input count: %v", send)
			}
			if input := string(<-f.egg(id).input); input != "from MCP" {
				t.Fatalf("MCP input=%q", input)
			}
			if enter := string(<-f.egg(id).input); enter != "\r" {
				t.Fatalf("MCP enter=%q", enter)
			}
			read = callSessionMCP(t, owner, "terminal_read", map[string]any{"session": id})
			if read["ansi"] != "fixture ready\nfrom MCP\r" {
				t.Fatalf("MCP post-input snapshot: %v", read)
			}
			callSessionMCP(t, owner, "terminal_stop", map[string]any{"session": id})
			select {
			case <-f.egg(id).killed:
			default:
				t.Fatal("MCP stop did not reach web egg")
			}
		})
	}
}

func (f *sessionEggFixture) hook(kind, input string) error {
	if kind == "UserPromptSubmit" {
		record, err := json.Marshal(map[string]any{"type": "user", "sessionId": "ours", "message": map[string]any{"role": "user", "content": input}})
		if err != nil {
			return err
		}
		file, err := os.OpenFile(f.transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(append(record, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}

	data, err := json.Marshal(map[string]any{"session_id": "ours", "hook_event_name": kind, "prompt": input})
	if err != nil {
		return err
	}
	name := filepath.Join(f.spool, "seq."+fmt.Sprintf("%020d", f.sequence)+".json")
	staged := name + ".pending"
	if err := os.WriteFile(staged, data, 0600); err != nil {
		return err
	}
	return os.Rename(staged, name)
}

func TestSessionPromptReceiptSharedByWebAndMCP(t *testing.T) {
	for _, mode := range []string{"http", "direct"} {
		t.Run(mode, func(t *testing.T) {
			f := newSessionTransportFixture(t)
			owner := f.mcp(mode, "alice")
			started := callSessionMCP(t, owner, "agent_start", map[string]any{"agent": "claude", "cwd": f.work})
			id := started["session"].(string)
			args := map[string]any{"session": id, "request_id": "shared-prompt", "input": "shared text"}
			result := f.tunnel("alice", map[string]any{"type": "session.control", "operation": "session_prompt", "arguments": args})
			if result["error"] != nil {
				t.Fatalf("web prompt: %v", result)
			}
			encoded, _ := json.Marshal(result["receipt"])
			var first egg.SessionPromptResult
			if err := json.Unmarshal(encoded, &first); err != nil {
				t.Fatal(err)
			}
			if !first.NativeReceiptObserved || !first.TransportEnqueued {
				t.Fatalf("web lost native receipt: %+v", first)
			}
			if got := string(<-f.egg(id).input); got != "\x1b[200~shared text\x1b[201~" {
				t.Fatalf("prompt paste=%q", got)
			}
			if got := string(<-f.egg(id).input); got != "\r" {
				t.Fatalf("prompt enter=%q", got)
			}
			replay := callSessionMCP(t, owner, "session_prompt", args)
			encoded, _ = json.Marshal(replay["receipt"])
			var retried egg.SessionPromptResult
			if err := json.Unmarshal(encoded, &retried); err != nil {
				t.Fatal(err)
			}
			if !retried.Retried || retried.ReceiptCursor != first.ReceiptCursor || !retried.NativeReceiptObserved {
				t.Fatalf("MCP retry lost web receipt: %+v", retried)
			}
			select {
			case input := <-f.egg(id).input:
				t.Fatalf("MCP retry resent prompt: %q", input)
			default:
			}
			read := callSessionMCP(t, owner, "session_read", map[string]any{"session": id})
			encoded, _ = json.Marshal(read["lifecycle"])
			var view egg.SessionView
			if err := json.Unmarshal(encoded, &view); err != nil {
				t.Fatal(err)
			}
			if view.ProviderSessionID != "ours" || view.HeadCursor < first.ReceiptCursor {
				t.Fatalf("shared lifecycle missing receipt: %+v", view)
			}
			denied := f.tunnel("bob", map[string]any{"type": "session.control", "operation": "session_prompt", "arguments": args})
			if denied["error"] == nil {
				t.Fatal("foreign web prompt admitted")
			}
			callSessionMCP(t, owner, "terminal_stop", map[string]any{"session": id})
		})
	}
}

func TestRegisteredSessionKeepsWingToolsAfterStartReturns(t *testing.T) {
	f := newSessionTransportFixture(t)
	authority := wingsession.Authority{UserID: "alice", Email: "alice@example.com", Role: "owner", Principal: wingsession.UserPrincipal("alice")}
	launch, err := f.sessions.PrepareLaunch(authority, f.work)
	if err != nil {
		t.Fatal(err)
	}
	client, err := f.sessions.Start(f.ctx, launch, wingsession.StartOptions{SessionID: "tools", Tools: []*config.ToolConfig{{Name: "fixture", Run: "fixture-command"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	if f.sessions.ToolListener("tools") == nil {
		t.Fatal("start released wing-owned tools")
	}
	conn, err := net.Dial("unix", filepath.Join(f.cfg.Dir, "eggs", "tools", ".tools", "tool.sock"))
	if err != nil {
		t.Fatalf("registered tools unavailable after start: %v", err)
	}
	_ = conn.Close()
	if _, err = f.sessions.Stop(f.ctx, authority, "tools"); err != nil {
		t.Fatal(err)
	}
}
