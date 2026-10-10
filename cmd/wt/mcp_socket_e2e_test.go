package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wing"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"google.golang.org/grpc"
)

// Re-executes this checkout's command adapter, with no installed wt or provider.
func TestWTStdioProcessFixture(t *testing.T) {
	if os.Getenv("WT_STDIO_FIXTURE") != "1" {
		return
	}
	cmd := mcpCmd()
	cmd.SetArgs([]string{"stdio"})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type localSocketEggFixture struct{ pb.UnimplementedEggServer }

func (f *localSocketEggFixture) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Agent: "claude", RenderedConfig: "fixture"}, nil
}
func (f *localSocketEggFixture) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("fixture egg is alive")}}); err != nil {
		return err
	}
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func TestStdioStartedSessionSurvivesClientExitAndIsWebVisible(t *testing.T) {
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	alias, err := os.MkdirTemp(scratch, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	t.Chdir(root)
	state := filepath.Join(alias, "s")
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WT_MCP_CLIENT", "")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	work := config.CanonicalProviderPath(root)
	cfg := &config.Config{Dir: state, WingID: "fixture-wing", DefaultAgent: "claude"}
	wc := &config.WingConfig{WingID: cfg.WingID, Paths: config.PathList{{Path: work}}}
	registered := make(chan string, 1)
	spawned := make(chan wingsession.StartOptions, 1)
	service := &wingsession.Service{Config: cfg, Home: work, Inventory: wing.ListAliveEggSessions, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }, Register: func(id string) error { registered <- id; return nil }}
	service.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
		if launch.Identity.UserID != "web-owner" || opts.Egg.Principal != wingsession.UserPrincipal("web-owner") || launch.CWD != work {
			return nil, fmt.Errorf("wrong launch authority: %+v %+v", launch, opts)
		}
		dir := filepath.Join(cfg.Dir, "eggs", opts.SessionID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		for name, value := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.token": "fixture-token", "egg.meta": "kind=agent\nagent=claude\ncwd=" + launch.CWD + "\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
				return nil, err
			}
		}
		if err := eggclient.WriteEggOwner(dir, launch.Identity.UserID, ""); err != nil {
			return nil, err
		}
		if err := eggclient.WriteSessionPrincipal(dir, opts.Egg.Principal); err != nil {
			return nil, err
		}
		listener, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
		if err != nil {
			return nil, err
		}
		rpc := grpc.NewServer()
		pb.RegisterEggServer(rpc, &localSocketEggFixture{})
		go func() { _ = rpc.Serve(listener) }()
		t.Cleanup(rpc.Stop)
		spawned <- opts
		return egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	}
	listener, err := localmcp.ListenLocalWingControl(t.Context(), "test", service, "web-owner", localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process := exec.Command(exe, "-test.run=^TestWTStdioProcessFixture$")
	process.Env = append(os.Environ(), "WT_STDIO_FIXTURE=1")
	if binary := os.Getenv("WT_TEST_BINARY"); binary != "" {
		process = exec.Command(binary, "mcp", "stdio")
		process.Env = os.Environ()
	}
	process.Stderr = os.Stderr
	input, err := process.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		_ = input.Close()
		if !exited {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_start","arguments":{"agent":"claude","label":"fixture"}}}` + "\n"
	if _, err := io.WriteString(input, request); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatalf("no MCP response: %v", scanner.Err())
	}
	var response struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.IsError {
		t.Fatalf("MCP start failed: %s", scanner.Bytes())
	}
	id := response.Result.Structured["session"].(string)
	opts := <-spawned
	if opts.SessionID != id || <-registered != id {
		t.Fatal("start acknowledged before wing registration")
	}
	_ = input.Close()
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	exited = true
	visible, err := service.ListWeb(t.Context(), wingsession.Authority{UserID: "web-owner", Role: "owner", Browser: true})
	if err != nil || len(visible) != 1 || visible[0].SessionID != id || visible[0].UserID != "web-owner" {
		t.Fatalf("not web visible after exit: %+v %v", visible, err)
	}
	local, err := service.List(t.Context(), wingsession.Authority{UserID: "web-owner", Principal: wingsession.UserPrincipal("web-owner")})
	if err != nil || len(local) != 1 || local[0].ID != id {
		t.Fatalf("egg died with stdio: %+v %v", local, err)
	}
	c, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, denied, err := c.Call(t.Context(), "terminal_read", json.RawMessage(`{"session":"`+id+`"}`))
	if err != nil || denied || !strings.Contains(fmt.Sprint(result["ansi"]), "fixture egg is alive") {
		t.Fatalf("reconnected egg RPC: %+v %v", result, err)
	}
	foreign, err := service.List(t.Context(), wingsession.Authority{UserID: "web-owner", Principal: "named-client"})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("logical owner isolation: %+v %v", foreign, err)
	}
}
