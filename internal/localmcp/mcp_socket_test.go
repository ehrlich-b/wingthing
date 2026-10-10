package localmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestBoundStdioUnavailableSocketExplainsMailbox(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission denial")
	}
	// A short endpoint under the checkout avoids the long-path runtime fallback;
	// removing directory search permission reproduces the sandbox's inability
	// to reach a live wing before initialization or linked-child reservation.
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(scratch, "bound-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var binds atomic.Int32
	listener, err := controlsocket.Listen(t.Context(), dir, "bound-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		binds.Add(1)
		return controlsocket.Welcome{}, nil, errors.New("unexpected bound dispatch")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	var output bytes.Buffer
	requests := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agent_start","arguments":{"agent":"claude","request_id":"child"}}}` + "\n")
	err = ServeLocalWingClient(t.Context(), "test", dir, "owner", "parent", false, requests, &output)
	if err == nil || !strings.Contains(err.Error(), "host mailbox") {
		t.Fatalf("bound MCP lost its launch before reservation without mailbox guidance: %v", err)
	}
	if binds.Load() != 0 || output.Len() != 0 {
		t.Fatal("unreachable bound stdio reached wing dispatch")
	}
}

func localSocketPolicyFixture(t *testing.T) (*wingsession.Service, *config.WingConfig) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("WINGTHING_DIR", "state")
	if err := os.Mkdir("state", 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: "state", WingID: "fixture-wing", DefaultAgent: "claude"}
	wc := &config.WingConfig{WingID: cfg.WingID}
	service := &wingsession.Service{Config: cfg, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	return service, wc
}

func TestLocalSocketSubdirectoryLaunchesRetainClientIsolation(t *testing.T) {
	service, wc := localSocketPolicyFixture(t)
	home, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	work := config.CanonicalProviderPath(filepath.Join(home, "work"))
	child := filepath.Join(work, "batch")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	service.Home = home
	wc.Paths = config.PathList{{Path: work}}
	admission := NewMCPAdmissionState()
	owner, err := resolveLocalWingClient("test", service, "owner", admission, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	f := newRunWingFixture(t, owner)
	if err := os.WriteFile("state/fixture.token", []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	spawn := service.Spawn
	service.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
		if _, err := spawn(launch, opts); err != nil {
			return nil, err
		}
		return egg.Dial("state/fixture.sock", "state/fixture.token")
	}
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", admission)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, clientName := range []string{"", "named"} {
		client, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{Client: clientName})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		for _, tool := range []string{"terminal_start", "agent_start", "agent_run"} {
			args, _ := json.Marshal(map[string]any{"cwd": child})
			if tool == "agent_start" {
				args, _ = json.Marshal(map[string]any{"cwd": child, "agent": "claude"})
			}
			if tool == "agent_run" {
				args, _ = json.Marshal(map[string]any{"cwd": child, "agent": "claude", "prompt": "fixture request"})
			}
			data, denied, err := client.Call(t.Context(), tool, args)
			if err != nil || denied || data["cwd"] != child {
				t.Fatalf("%s/%s subdirectory launch: %v, denied=%v, %v", clientName, tool, data, denied, err)
			}
			launch, opts := <-f.launches, <-f.spawned
			if launch.CWD != child || opts.Egg.Principal != client.Welcome.Principal {
				t.Fatalf("spawn lost cwd or principal: %+v, %+v", launch, opts)
			}
			session := eggclient.LocalSession{ID: opts.SessionID, CWD: child, Principal: opts.Egg.Principal}
			if owner.ownsSession(session) != (clientName == "") {
				t.Fatal("default client crossed named-client boundary")
			}
			named, err := resolveLocalWingClient("test", service, "owner", admission, controlsocket.Hello{Client: "named"})
			if err != nil || named.ownsSession(session) != (clientName == "named") {
				t.Fatalf("named-client ownership changed: %v", err)
			}
			if tool == "agent_run" {
				id := data["run_id"].(string)
				if <-f.submitted != id {
					t.Fatal("wrong run submitted")
				}
				f.finish(t, id, "done", "")
				if err := service.RunManager.Wait(t.Context(), wingsession.Authority{UserID: "owner", Principal: client.Welcome.Principal}, []string{id}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestLocalWingRenameConflictPreservesSentinel(t *testing.T) {
	service, wc := localSocketPolicyFixture(t)
	workspace, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wc.Paths = config.PathList{{Path: workspace}}
	for id, name := range map[string]string{"first": "shared", "second": "old"} {
		dir := filepath.Join(service.Config.Dir, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for file, content := range map[string]string{
			"egg.pid":  strconv.Itoa(os.Getpid()),
			"egg.meta": "cwd=" + workspace + "\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := eggclient.WriteEggOwner(dir, "owner", "owner@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteSessionPrincipal(dir, wingsession.UserPrincipal("owner")); err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteSessionName(dir, name); err != nil {
			t.Fatal(err)
		}
	}
	// The first session has already claimed the name: rejection is independent
	// of scheduling. Dial and Call wait for the handshake and response.
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := controlsocket.Dial(t.Context(), service.Config.Dir, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	arguments := json.RawMessage(`{"session":"second","name":"shared"}`)
	result, isError, err := client.Call(t.Context(), "terminal_rename", arguments)
	if !isError || !errors.Is(err, eggclient.ErrSessionNameInUse) {
		t.Fatalf("socket rename: result=%#v isError=%v err=%v; want name-conflict sentinel", result, isError, err)
	}
	if result["error_kind"] != string(control.ErrorSessionNameInUse) {
		t.Fatalf("socket lost error kind: %#v", result)
	}
	if _, err := CallLocalWingTool(t.Context(), service.Config.Dir, "", "terminal_rename", arguments); !errors.Is(err, eggclient.ErrSessionNameInUse) {
		t.Fatalf("CLI socket helper lost sentinel: %v", err)
	}
	server, err := resolveLocalWingClient("test", service, "owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	server.Surface = control.SurfaceDirectMCP
	direct := server.handleDirectRequest(t.Context(), control.DirectRequest{
		Version: control.ContractVersion, ID: "rename", Tool: "terminal_rename", Arguments: arguments,
	})
	if direct.ErrorKind != control.ErrorSessionNameInUse || !errors.Is(direct.Err(), eggclient.ErrSessionNameInUse) {
		t.Fatalf("direct wing response lost sentinel: %#v", direct)
	}
	proxy := &localWingProxy{version: "test", client: client}
	response, _ := proxy.handle(t.Context(), localMCPRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"terminal_rename","arguments":{"session":"second","name":"shared"}}`),
	})
	structured := response.Result.(map[string]any)["structuredContent"].(map[string]any)
	if !errors.Is(control.ToolError(structured), eggclient.ErrSessionNameInUse) {
		t.Fatalf("stdio adapter lost kind: %#v", response)
	}
	for _, tool := range RoostNativeMCPToolsWithSessions("test", service.Config, false,
		func() *wingsession.Service { return service },
		func() (*config.WingConfig, *egg.EggConfig) { return wc, egg.DefaultEggConfig() }) {
		if tool.Name == "terminal_rename" {
			_, isError, err := tool.Call(t.Context(), mcppkg.Principal{UserID: "owner", Email: "owner@example.com"}, arguments)
			if !isError || !errors.Is(err, eggclient.ErrSessionNameInUse) {
				t.Fatalf("HTTP native rename lost sentinel: isError=%v err=%v", isError, err)
			}
			return
		}
	}
	t.Fatal("HTTP terminal_rename tool missing")
}

func TestLocalWingPrincipalGrantParity(t *testing.T) {
	service, _ := localSocketPolicyFixture(t)
	admission := NewMCPAdmissionState()
	for _, configured := range []bool{false, true} {
		if configured {
			data := "clients:\n  default:\n    grants: [terminal.read, agent.run]\n    bounds: {max_sessions: 2, max_spawns_per_hour: 4}\n  claude:\n    owner: team\n    grants: [terminal.read]\n    bounds: {max_sessions: 3, max_spawns_per_hour: 5}\n  empty:\n    grants: []\n"
			if err := os.WriteFile(filepath.Join(service.Config.Dir, "clients.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"", "default", "claude", "unknown", "empty"} {
			server, err := resolveLocalWingClient("test", service, "owner-user", admission, controlsocket.Hello{Client: name})
			if configured && name == "unknown" {
				if err == nil {
					t.Fatal("unknown configured client admitted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			want := name
			if want == "" || want == "default" {
				want = wingsession.UserPrincipal("owner-user")
			}
			if configured && name == "claude" {
				want = "team"
			}
			if server.Principal != want || server.identity.UserID != "owner-user" || server.Sessions != service || server.Unsandboxed {
				t.Fatalf("authority mismatch: %+v", server)
			}
			if configured {
				requested := name
				if requested == "" {
					requested = "default"
				}
				clients, err := LoadLocalMCPClientsConfig(service.Config)
				if err != nil {
					t.Fatal(err)
				}
				entry := clients.Clients[requested]
				if !reflect.DeepEqual(server.Grants, GrantSet(entry.Grants)) || server.MaxSessions != entry.Bounds.MaxSessions || server.MaxSpawnsPerHour != entry.Bounds.MaxSpawnsPerHour {
					t.Fatalf("lost grants/bounds: %+v", server)
				}
			} else if server.Grants != nil || server.MaxSessions != 0 || server.MaxSpawnsPerHour != 0 {
				t.Fatal("changed unconfigured client rules")
			}
		}
	}
	if err := os.WriteFile("state/clients.yaml", []byte("require_client: true\nclients:\n  default: {grants: []}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLocalWingClient("test", service, "owner-user", admission, controlsocket.Hello{}); err == nil {
		t.Fatal("require_client accepted implicit default")
	}
	if _, err := resolveLocalWingClient("test", service, "owner-user", admission, controlsocket.Hello{Client: "default"}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalWingPolicyRefreshAndEmptyGrants(t *testing.T) {
	service, wc := localSocketPolicyFixture(t)
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{Client: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, denied, err := c.Call(t.Context(), "wingthing_capabilities", json.RawMessage(`{}`)); err != nil || denied {
		t.Fatalf("unconfigured authority: %v", err)
	}
	if err := os.WriteFile("state/clients.yaml", []byte("clients:\n  claude: {grants: []}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, denied, err := c.Call(t.Context(), "terminal_list", json.RawMessage(`{}`)); err != nil || !denied {
		t.Fatalf("stale permission lease: %v %v", denied, err)
	}
	empty, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{Client: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if empty.Welcome.Grants == nil || len(empty.Welcome.Grants) != 0 {
		t.Fatal("empty grants became unrestricted in handshake")
	}
	wc.Org = "org"
	if _, _, err := c.Call(t.Context(), "terminal_list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("local socket continued admitting org calls")
	}
}

func TestLocalSocketDoesNotServeSharedWings(t *testing.T) {
	service, wc := localSocketPolicyFixture(t)
	for _, org := range []bool{true, false} {
		wc.Org = ""
		service.SharedHost = !org
		if org {
			wc.Org = "org"
		}
		listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", NewMCPAdmissionState())
		if err != nil || listener != nil {
			t.Fatalf("shared endpoint: %v %v", listener, err)
		}
		if _, err := os.Stat("state/control.sock"); !os.IsNotExist(err) {
			t.Fatal("shared wing created socket")
		}
	}
}

func TestLocalDefaultSeesLegacySessionsWithoutCrossingOwners(t *testing.T) {
	service, _ := localSocketPolicyFixture(t)
	def, err := resolveLocalWingClient("test", service, "owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	named, err := resolveLocalWingClient("test", service, "owner", NewMCPAdmissionState(), controlsocket.Hello{Client: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []string{"", "default", wingsession.UserPrincipal("owner")} {
		session := eggclient.LocalSession{ID: "legacy", Principal: principal}
		if principal == wingsession.UserPrincipal("owner") {
			if err := os.MkdirAll("state/eggs/legacy", 0700); err != nil {
				t.Fatal(err)
			}
			if err := eggclient.WriteEggOwner("state/eggs/legacy", "owner", ""); err != nil {
				t.Fatal(err)
			}
		}
		if !def.ownsSession(session) || named.ownsSession(session) {
			t.Fatalf("legacy scope principal %q", principal)
		}
	}
	if err := eggclient.WriteEggOwner("state/eggs/legacy", "foreign", ""); err != nil {
		t.Fatal(err)
	}
	if def.ownsSession(eggclient.LocalSession{ID: "legacy", Principal: "default"}) {
		t.Fatal("default client crossed user owner")
	}
}

func TestLocalProxyShapesCWDWithoutAuthorityFields(t *testing.T) {
	service, _ := localSocketPolicyFixture(t)
	t.Setenv("WT_MCP_CLIENT", "untrusted-env")
	tool, _ := control.Lookup("agent_start")
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"agent":"claude"}`, `{"agent":"claude","cwd":""}`, `{"agent":"claude","cwd":"subdir"}`} {
		result, err := shapeLocalArguments(tool, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		_ = json.Unmarshal(result, &fields)
		want := current
		if strings.Contains(input, "subdir") {
			want = filepath.Join(current, "subdir")
		}
		if fields["cwd"] != want || len(fields) != 2 {
			t.Fatalf("shaped arguments: %s", result)
		}
	}
	server, err := resolveLocalWingClient("test", service, "owner", NewMCPAdmissionState(), controlsocket.Hello{})
	if err != nil || server.MCPClient != "default" {
		t.Fatalf("wing used inherited client env: %v", err)
	}
}

func TestAgentRunSurvivesStdioClientExit(t *testing.T) { exerciseMCPRunClientExit(t) }

// The outer boundary selects a launch profile; all admission still belongs to
// the wing, including revocation on an already-open connection.
func TestOuterBoundaryCannotOverridePolicy(t *testing.T) {
	service, wc := localSocketPolicyFixture(t)
	home, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home = config.CanonicalProviderPath(home)
	work := filepath.Join(home, "workspace")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{work, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	service.Home = home
	wc.Paths = config.PathList{{Path: work}}
	admission := NewMCPAdmissionState()
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", admission)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{Unsandboxed: true}); err == nil || !strings.Contains(err.Error(), "allow_unsandboxed: true") {
		t.Fatalf("missing opt-in: %v", err)
	}
	wc.AllowUnsandboxed = true
	if err := os.WriteFile("state/clients.yaml", []byte("clients:\n  default:\n    grants: [terminal.start, capabilities.read]\n    bounds: {max_spawns_per_hour: 1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := controlsocket.Dial(t.Context(), "state", controlsocket.Hello{Unsandboxed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Welcome.Isolation != "outer-boundary" {
		t.Fatal("wing did not declare launch profile")
	}
	spawned := make(chan *wingsession.Launch, 1)
	registered := make(chan string, 1)
	service.Register = func(id string) error { registered <- id; return nil }
	token := filepath.Join(home, "fixture.token")
	if err := os.WriteFile(token, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	service.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
		if launch.Identity.UserID != "owner" || opts.Egg.Principal != wingsession.UserPrincipal("owner") {
			return nil, fmt.Errorf("caller replaced wing authority")
		}
		spawned <- launch
		return egg.Dial(filepath.Join(home, "fixture.sock"), token)
	}
	request := func(cwd string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"cwd": cwd, "command": []string{"fixture-command"}})
		return b
	}
	result, denied, err := client.Call(t.Context(), "terminal_start", request(work))
	if err != nil || denied || result["isolation"] != "outer-boundary" {
		t.Fatalf("opted-in launch: %v %v %v", result, denied, err)
	}
	launch := <-spawned
	if egg.RequiresSandbox(launch.Config, "") || launch.CWD != work {
		t.Fatal("wing did not select the requested launch profile")
	}
	if id := <-registered; id != result["session"] {
		t.Fatal("acknowledged before wing registration")
	}
	for _, args := range []json.RawMessage{request(outside), json.RawMessage(`{"cwd":"` + work + `","principal":"other","allow_unsandboxed":true}`)} {
		if _, denied, err := client.Call(t.Context(), "terminal_start", args); err == nil && !denied {
			t.Fatal("outer boundary bypassed paths or accepted client policy")
		}
	}
	select {
	case <-spawned:
		t.Fatal("denied call spawned")
	default:
	}
	if err := os.WriteFile("state/clients.yaml", []byte("clients:\n  default:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, denied, err := client.Call(t.Context(), "terminal_start", request(work)); err != nil || !denied {
		t.Fatalf("outer boundary bypassed grants: %v %v", denied, err)
	}
	if err := os.WriteFile("state/clients.yaml", []byte("clients:\n  default:\n    grants: [terminal.start]\n    bounds: {max_spawns_per_hour: 1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, denied, err := client.Call(t.Context(), "terminal_start", request(work)); err != nil || !denied || !strings.Contains(fmt.Sprint(result["error"]), "max_spawns_per_hour") {
		t.Fatalf("outer boundary bypassed bounds: %v %v %v", result, denied, err)
	}
	active := filepath.Join("state", "eggs", "active")
	if err := os.MkdirAll(active, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"egg.pid": fmt.Sprint(os.Getpid()), "egg.meta": "kind=command\ncwd=" + work + "\n"} {
		if err := os.WriteFile(filepath.Join(active, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := eggclient.WriteEggOwner(active, "owner", ""); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(active, wingsession.UserPrincipal("owner")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("state/clients.yaml", []byte("clients:\n  default:\n    grants: [terminal.start]\n    bounds: {max_sessions: 1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, denied, err := client.Call(t.Context(), "terminal_start", request(work)); err != nil || !denied || !strings.Contains(fmt.Sprint(result["error"]), "max_sessions") {
		t.Fatalf("outer boundary bypassed session bounds: %v %v %v", result, denied, err)
	}
	wc.Locked = true
	if _, _, err := client.Call(t.Context(), "terminal_start", request(work)); err == nil {
		t.Fatal("outer boundary bypassed wing lock")
	}
	wc.Locked = false
	wc.AllowUnsandboxed = false
	if _, _, err := client.Call(t.Context(), "terminal_start", request(work)); err == nil || !strings.Contains(err.Error(), "allow_unsandboxed") {
		t.Fatalf("stale unsandboxed lease: %v", err)
	}
	select {
	case <-spawned:
		t.Fatal("revoked call spawned")
	default:
	}
}
