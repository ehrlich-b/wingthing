package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

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

func TestAgentRunSurvivesStdioClientExit(t *testing.T) {
	service, _ := localSocketPolicyFixture(t)
	started := make(chan *store.Task, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	admission := NewMCPAdmissionState()
	listener, err := controlsocket.Listen(t.Context(), "state", "fixture-wing", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		server, err := resolveLocalWingClient("test", service, "owner", admission, hello)
		if err != nil {
			return controlsocket.Welcome{}, nil, err
		}
		server.runAgentTask = func(ctx context.Context, _ *config.Config, db *store.Store, task *store.Task, _ taskrun.TaskRunOptions) error {
			defer close(finished)
			started <- task
			<-release
			if err := ctx.Err(); err != nil {
				return err
			}
			output := "complete fixture answer"
			if err := db.SetTaskOutput(task.ID, output); err != nil {
				return err
			}
			return db.UpdateTaskStatus(task.ID, "done")
		}
		return controlsocket.Welcome{Principal: server.Principal, Actor: server.Actor, Grants: server.Grants}, server.handleDirectRequest, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	input, sender := io.Pipe()
	output, receiver := io.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- ServeLocalWingClient(t.Context(), "test", "state", "", "", false, input, receiver)
	}()
	defer output.Close()
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_run","arguments":{"agent":"claude","prompt":"fixture"}}}` + "\n"
	if _, err := io.WriteString(sender, request); err != nil {
		t.Fatal(err)
	}
	var response localMCPResponse
	if err := json.NewDecoder(output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil || response.Result.(map[string]any)["isError"] == true {
		t.Fatalf("run refused: %+v", response)
	}
	task := <-started
	if response.Error != nil || task.Principal != wingsession.UserPrincipal("owner") {
		t.Fatalf("run response: %+v", response)
	}
	_ = sender.Close()
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	close(release)
	<-finished
	db, err := store.Open(service.Config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := db.GetTask(task.ID)
	if err != nil || result.Status != "done" || result.Output == nil || *result.Output != "complete fixture answer" {
		t.Fatalf("accepted work died with client: %+v %v", result, err)
	}
	var audit bytes.Buffer
	server := testWingServer(t, &Server{Version: "test", Cfg: service.Config, Principal: "foreign", Logs: &audit})
	if _, _, err := server.ownedAgentRun(task.ID); err == nil {
		t.Fatal("foreign owner observed run")
	}
}

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
