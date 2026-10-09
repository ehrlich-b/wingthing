package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const contextInputDisabled = "Context tools are disabled after another user took control of this session"

type contextInputEgg struct {
	pb.UnimplementedEggServer
	onInput func([]byte)
	inputs  atomic.Int32
	reject  bool
}

func (f *contextInputEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if f.reject {
		return status.Error(codes.FailedPrecondition, "input is owned")
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("snapshot")}}); err != nil {
		return err
	}
	for {
		message, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch payload := message.Payload.(type) {
		case *pb.SessionMsg_Input:
			f.onInput(payload.Input)
			f.inputs.Add(1)
		case *pb.SessionMsg_Detach:
			return nil
		}
	}
}

func contextInputToolCall(t *testing.T, path string) egg.ToolResponse {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(egg.ToolRequest{Tool: "context"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var response egg.ToolResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func contextInputFixture(t *testing.T) (*config.Config, *contextInputEgg, string) {
	t.Helper()
	// Keep Unix socket names short and fixture state inside this checkout.
	root, err := os.MkdirTemp("", ".ctx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: root}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("subject_token") != "owner@example.com" {
				t.Errorf("Context impersonation lost the verified owner: %v", err)
			}
			_, _ = io.WriteString(w, `{"access_token":"fixture-token","expires_in":60}`)
		case "/mcp":
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"owner result"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	secret := filepath.Join(root, "context.secret")
	if err := os.WriteFile(secret, []byte("fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(root, &config.WingConfig{Context: &config.ContextConfig{URL: upstream.URL, ClientID: "fixture", SecretFile: secret, Scopes: []string{"jira"}}}); err != nil {
		t.Fatal(err)
	}
	opts := eggclient.SpawnEggOpts{}
	tools, err := eggclient.PrepareBrowserTools(cfg, "fixture", []*config.ToolConfig{{Name: "context", Context: "jira-search"}}, &opts, eggclient.EggIdentity{UserID: "owner", Email: "owner@example.com"})
	if err != nil || tools == nil {
		t.Fatalf("prepare tools: %v", err)
	}
	t.Cleanup(func() { _ = tools.Close() })
	dir := filepath.Join(root, "eggs", "fixture")
	for name, content := range map[string]string{
		"egg.pid": fmt.Sprint(os.Getpid()), "egg.token": "fixture-token", "egg.owner": "owner",
		"egg.meta":          "kind=agent\nagent=claude\ncwd=" + root + "\nprovider_home=" + root + "\nprovider_session_id=ours\n",
		"session.principal": "legacy-owner",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	spool := filepath.Join(root, ".claude", "wingthing-events", "fixture")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "session.json"), []byte(`{"session_id":"ours","hook_event_name":"SessionStart"}`), 0600); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	fixture := &contextInputEgg{}
	pb.RegisterEggServer(server, fixture)
	go func() { _ = server.Serve(socket) }()
	return cfg, fixture, opts.ToolSocketPath
}

func TestContextInputAdaptersRevokeBeforeNonOwnerInput(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, adapter := range []string{"tunnel", "mcp"} {
		for _, operation := range []string{"terminal_send", "session_prompt"} {
			t.Run(adapter+"/"+operation, func(t *testing.T) {
				cfg, fixture, toolPath := contextInputFixture(t)
				var disabled atomic.Bool
				assertContext := func() {
					t.Helper()
					response := contextInputToolCall(t, toolPath)
					if disabled.Load() {
						if response.Error != contextInputDisabled {
							t.Errorf("non-owner input retained Context authority: %+v", response)
						}
					} else if response.Error != "" || !strings.Contains(response.Stdout, "owner result") {
						t.Errorf("owner input lost Context authority: %+v", response)
					}
				}
				fixture.onInput = func([]byte) { assertContext() }
				for index, user := range []string{"owner", "admin", "owner"} {
					if user == "admin" {
						disabled.Store(true)
					}
					args := map[string]any{"session": "fixture", "input": fmt.Sprintf("input-%d", index)}
					if operation == "session_prompt" {
						args["request_id"], args["timeout_seconds"] = fmt.Sprintf("request-%d", index), 0.2
					}
					arguments, _ := json.Marshal(args)
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					var result map[string]any
					var err error
					if adapter == "tunnel" {
						result, err = BrowserSessionControl("dev", ctx, cfg, &config.WingConfig{Org: "fixture-org"}, ws.TunnelRequest{SenderUserID: user, SenderEmail: user + "@example.com", SenderOrgRole: "admin"}, operation, arguments, cfg.Dir, false)
					} else {
						// The shared typed adapter must retain the verified controller
						// even when routing under legacy logical session ownership.
						s := &Server{Version: "dev", Cfg: cfg, Principal: "legacy-owner", Logs: io.Discard, identity: eggclient.EggIdentity{UserID: user}}
						var isError bool
						var protocolErr *localMCPError
						result, isError, protocolErr = s.callTool(ctx, operation, arguments)
						if isError || protocolErr != nil {
							t.Fatalf("MCP input failed: %v %v", result, protocolErr)
						}
					}
					cancel()
					if err != nil {
						t.Fatal(err)
					}
					if operation == "session_prompt" && !result["receipt"].(egg.SessionPromptResult).TransportEnqueued {
						t.Fatalf("prompt never reached the egg: %v", result)
					}
					assertContext()
				}
				expected := int32(3)
				if operation == "session_prompt" {
					expected *= 2
				}
				if fixture.inputs.Load() != expected {
					t.Fatalf("input frames = %d, want %d", fixture.inputs.Load(), expected)
				}
			})
		}
	}
}

func TestContextInputLocalMCPRejectsOtherPrincipal(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	cfg, fixture, toolPath := contextInputFixture(t)
	fixture.onInput = func([]byte) { t.Error("foreign principal input reached the egg") }
	s := &Server{Version: "dev", Cfg: cfg, Principal: "other-principal", Logs: io.Discard, identity: eggclient.EggIdentity{UserID: "admin"}}
	for _, operation := range []string{"terminal_send", "session_prompt"} {
		args := map[string]any{"session": "fixture", "input": "foreign"}
		if operation == "session_prompt" {
			args["request_id"] = "foreign"
		}
		arguments, _ := json.Marshal(args)
		_, isError, protocolErr := s.callTool(context.Background(), operation, arguments)
		if !isError || protocolErr != nil {
			t.Fatalf("foreign principal allowed %s", operation)
		}
	}
	if fixture.inputs.Load() != 0 {
		t.Fatal("foreign principal sent input")
	}
	if response := contextInputToolCall(t, toolPath); response.Error != "" {
		t.Fatalf("rejected input revoked owner authority: %+v", response)
	}
	// A local client with the retained logical owner has no relay user ID.
	// Its accepted input must not be mistaken for a different verified user.
	s.Principal, s.identity = "legacy-owner", eggclient.EggIdentity{}
	fixture.onInput = func([]byte) {
		if response := contextInputToolCall(t, toolPath); response.Error != "" {
			t.Errorf("local owner lost Context authority: %+v", response)
		}
	}
	for _, operation := range []string{"terminal_send", "session_prompt"} {
		args := map[string]any{"session": "fixture", "input": "owner"}
		if operation == "session_prompt" {
			args["request_id"], args["timeout_seconds"] = "owner", 0.2
		}
		arguments, _ := json.Marshal(args)
		result, isError, protocolErr := s.callTool(context.Background(), operation, arguments)
		if isError || protocolErr != nil {
			t.Fatalf("local owner input failed: %v %v", result, protocolErr)
		}
	}
	if fixture.inputs.Load() != 3 {
		t.Fatalf("local owner input frames = %d, want 3", fixture.inputs.Load())
	}
}

func TestContextInputRejectedClaimsPreserveAuthority(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, operation := range []string{"terminal_send", "session_prompt"} {
		t.Run(operation, func(t *testing.T) {
			cfg, fixture, toolPath := contextInputFixture(t)
			fixture.reject = true
			fixture.onInput = func([]byte) { t.Error("rejected claim sent input") }
			args := map[string]any{"session": "fixture", "input": "admin"}
			if operation == "session_prompt" {
				args["request_id"], args["timeout_seconds"] = "admin", 0.2
			}
			arguments, _ := json.Marshal(args)
			result, err := BrowserSessionControl("dev", context.Background(), cfg, &config.WingConfig{Org: "fixture-org"}, ws.TunnelRequest{SenderUserID: "admin", SenderOrgRole: "admin"}, operation, arguments, cfg.Dir, false)
			if operation == "terminal_send" && err == nil {
				t.Fatal("rejected terminal claim succeeded")
			}
			if operation == "session_prompt" && (err != nil || !result["receipt"].(egg.SessionPromptResult).DefinitelyNotSent) {
				t.Fatalf("rejected prompt claim was not recorded as unsent: %v %v", result, err)
			}
			if fixture.inputs.Load() != 0 {
				t.Fatal("rejected claim sent input")
			}
			if response := contextInputToolCall(t, toolPath); response.Error != "" {
				t.Fatalf("rejected claim revoked owner authority: %+v", response)
			}
		})
	}
}
