package localmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func forkServerFixture(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Dir: t.TempDir()}
	dir := writeResumeSessionFixture(t, cfg, "source", "alice", "claude", cfg.Dir, "provider", "{}\n")
	if err := eggclient.WriteSessionPrincipal(dir, "owner"); err != nil {
		t.Fatal(err)
	}
	return testWingServer(t, &Server{Version: "dev", Cfg: cfg, Principal: "owner", identity: eggclient.EggIdentity{UserID: "alice"}, Logs: &bytes.Buffer{}, spawnFork: func(*eggclient.SessionForkPlan) error { return nil }})
}

func TestSessionForkMCPGrantAndSourceAuditTarget(t *testing.T) {
	s := forkServerFixture(t)
	s.Grants = GrantSet([]string{"terminal.read"})
	args := json.RawMessage(`{"session":"source","name":"branch"}`)
	result, isError, protocolErr := s.callTool(context.Background(), "session_fork", args)
	if protocolErr != nil || !isError || !strings.Contains(result["error"].(string), "terminal.start") {
		t.Fatalf("grant refusal: %#v %t %v", result, isError, protocolErr)
	}
	s.Grants = GrantSet([]string{"terminal.start"})
	result, isError, protocolErr = s.callTool(context.Background(), "session_fork", args)
	if protocolErr != nil || isError || result["session"] == "source" || result["source_session"] != "source" {
		t.Fatalf("fork result: %#v %t %v", result, isError, protocolErr)
	}
	data, err := os.ReadFile(filepath.Join(s.Cfg.Dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["tool"] != "session_fork" || record["target"] != "source" {
			t.Fatalf("audit target: %s", line)
		}
	}
}

func TestSessionForkMCPUsesSpawnBoundsAndRefusesBoundConnections(t *testing.T) {
	for _, name := range []string{"sessions", "rate", "shared rate", "bound", "principal", "nested schema"} {
		t.Run(name, func(t *testing.T) {
			s := forkServerFixture(t)
			want := ""
			args := json.RawMessage(`{"session":"source","name":"branch"}`)
			switch name {
			case "sessions":
				seedRemoteListSession(t, s.Cfg, "active", "owner")
				if err := eggclient.WriteEggOwner(filepath.Join(s.Cfg.Dir, "eggs", "active"), s.identity.UserID, ""); err != nil {
					t.Fatal(err)
				}
				s.MaxSessions = 1
				want = "max_sessions"
			case "rate":
				s.MaxSpawnsPerHour = 1
				s.spawnTimes = []time.Time{time.Now()}
				want = "max_spawns"
			case "shared rate":
				s.MaxSpawnsPerHour = 1
				s.admission = NewMCPAdmissionState()
				s.admission.spawnTimes["owner"] = []time.Time{time.Now()}
				want = "max_spawns"
			case "bound":
				s.BoundConversation = "root"
				want = "bound"
			case "principal":
				s.Principal = "other"
				want = "owned"
			case "nested schema":
				args = json.RawMessage(`{"session":"source","options":{"name":"branch"}}`)
				want = "unknown field"
			}
			s.spawnFork = func(*eggclient.SessionForkPlan) error { t.Fatal("refused fork spawned"); return nil }
			_, err := s.ToolSessionFork(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error: %v, want %s", err, want)
			}
		})
	}
}

func TestConcurrentForksRecheckSessionBoundsUnderNameLock(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	first := forkServerFixture(t)
	first.MaxSessions = 1
	first.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		dir := filepath.Join(first.Cfg.Dir, "eggs", plan.SessionID)
		if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return err
		}
		if err := eggclient.WriteSessionName(dir, plan.Options.Label); err != nil {
			return err
		}
		if err := eggclient.WriteEggOwner(dir, first.identity.UserID, ""); err != nil {
			return err
		}
		return eggclient.WriteSessionPrincipal(dir, "owner")
	}
	// Independent servers model CLI processes with separate admission state.
	second := testWingServer(t, &Server{Version: "dev", Cfg: first.Cfg, Principal: first.Principal, identity: first.identity, MaxSessions: 1, spawnFork: first.spawnFork})
	admitted := make(chan struct{}, 2)
	start := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-start:
		default:
			close(start)
		}
	})
	done := make(chan error, 2)
	for i, s := range []*Server{first, second} {
		scope := s.sessionForkScope()
		admit := scope.Admit
		scope.Admit = func(launch func() error) error {
			return admit(func() error {
				admitted <- struct{}{}
				<-start
				return launch()
			})
		}
		go func() {
			_, err := eggclient.ForkSession(context.Background(), s.Cfg, "source", "branch-"+strconv.Itoa(i), scope)
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-admitted:
		case <-time.After(3 * time.Second):
			t.Fatal("both invocations did not pass initial admission")
		}
	}
	close(start)
	started, refused := 0, 0
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				started++
			} else if strings.Contains(err.Error(), "max_sessions=1") {
				refused++
			} else {
				t.Fatalf("unexpected fork error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent fork did not finish")
		}
	}
	if started != 1 || refused != 1 {
		t.Fatalf("started %d forks and refused %d at max_sessions=1", started, refused)
	}
}

func TestBrowserSessionForkSharedOwnerRules(t *testing.T) {
	s := forkServerFixture(t)
	dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd="+s.Cfg.Dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"session":"source","name":"branch"}`)
	for _, shared := range []bool{false, true} {
		for _, user := range []string{"alice", "bob"} {
			req := ws.TunnelRequest{SenderUserID: user, SenderOrgRole: "admin", SenderEmail: user + "@example.com"}
			_, err := testBrowserControl(t, "dev", context.Background(), s.Cfg, &config.WingConfig{Org: "shared-org"}, req, "session_fork", args, s.Cfg.Dir, shared)
			want := "only Claude"
			if user == "bob" {
				want = "owned"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("shared=%t user=%s error=%v", shared, user, err)
			}
		}
	}
}

func TestSessionForkMCPBindsNewRootWithFreshIsolation(t *testing.T) {
	s := forkServerFixture(t)
	dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
	if err := os.WriteFile(filepath.Join(dir, "session.launch.json"), []byte(`{"config":"base: none\nnetwork: '*'\nenv: '*'","model":"attacker-model"}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	original, _, err := db.ReserveConversation(store.Conversation{ID: "original", OwnerID: "owner", Agent: "claude", CWD: s.Cfg.Dir, SessionID: "source", LaunchKey: "initial", SpecDigest: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		if !egg.RequiresSandbox(plan.Config, "claude") || plan.Conversation.ID == original.ID || plan.Conversation.ParentID != "" {
			t.Fatalf("fork changed policy or reused the original tree: %#v", plan)
		}
		var binding string
		for i, arg := range plan.Options.AgentArgs {
			if arg == "--mcp-config" && i+1 < len(plan.Options.AgentArgs) {
				binding = plan.Options.AgentArgs[i+1]
			}
		}
		data, err := os.ReadFile(binding)
		if err != nil || !strings.Contains(string(data), plan.Conversation.ID) || strings.Contains(string(data), `"original"`) {
			t.Fatalf("fork MCP binding = %q: %v", data, err)
		}
		return nil
	}
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForkIdentityMatchesFreshLaunchOnEverySurface(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	for _, surface := range []string{"local MCP", "local MCP unsandboxed", "direct org member", "HTTP shared host", "browser org member", "browser shared host", "CLI"} {
		t.Run(surface, func(t *testing.T) {
			s := forkServerFixture(t)
			cwd := wingpolicy.CanonicalSessionPath(s.Cfg.Dir)
			s.Cfg.Dir = cwd
			wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: cwd, Members: []string{"alice@example.com"}}}}
			req := ws.TunnelRequest{SenderUserID: "alice", SenderEmail: "alice@example.com", SenderOrgRole: "member"}
			if err := os.WriteFile(filepath.Join(cwd, "egg.yaml"), []byte("base: none\nfs: [deny:/, rw:"+cwd+"]\nnetwork: none\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveWingConfig(s.Cfg.Dir, wc); err != nil {
				t.Fatal(err)
			}
			var freshIdentity eggclient.EggIdentity
			var freshConfig *egg.EggConfig
			var err error
			switch surface {
			case "local MCP unsandboxed":
				s.Unsandboxed = true
				s.Sessions.Policy().Wing.AllowUnsandboxed = true
			case "direct org member":
				policy, policyErr := resolveDirectMCPPolicy(wc, cwd, false, webrtcpkg.PeerIdentity{UserID: req.SenderUserID, Email: req.SenderEmail, OrgRole: req.SenderOrgRole})
				if policyErr != nil {
					t.Fatal(policyErr)
				}
				s.Surface, s.identity = control.SurfaceDirectMCP, policy.identity
				s.allowedPaths, s.enforcePathBounds = policy.allowedPaths, policy.enforcePathBounds
				s.launchConfig = runtimeLaunchConfig(wc, cwd, true, s.allowedPaths, egg.DefaultEggConfig(), nil)
				s.sessionRole = "member"
				s.Sessions.Home = cwd
				s.Sessions.Policy = func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }
			case "HTTP shared host":
				server := testNativeServer(t, "dev", s.Cfg, true, NewMCPAdmissionState(), mcppkg.Principal{UserID: req.SenderUserID, Email: req.SenderEmail}, []string{cwd})
				s.Surface, s.identity = server.Surface, server.identity
				s.allowedPaths, s.enforcePathBounds = server.allowedPaths, server.enforcePathBounds
				s.launchConfig = server.launchConfig
				s.sessionRole = server.sessionRole
				s.Sessions.SharedHost = true
				s.Sessions.Policy = server.Sessions.Policy
			case "browser org member", "browser shared host":
				if err := os.WriteFile(filepath.Join(cwd, "egg.yaml"), []byte("base: none\nfs: [deny:/, rw:"+cwd+"]\nnetwork: none\n"), 0600); err != nil {
					t.Fatal(err)
				}
				shared := surface == "browser shared host"
				start := ws.PTYStart{UserID: req.SenderUserID, Email: req.SenderEmail, OrgRole: req.SenderOrgRole, CWD: cwd}
				freshConfig, freshIdentity, err = eggclient.PrepareBrowserLaunch(wc, &start, cwd, shared, egg.DefaultEggConfig())
				if err != nil {
					t.Fatal(err)
				}
				testBrowserFork(t, s, wc, req, cwd, shared, egg.DefaultEggConfig(), nil)
			case "CLI":
				s.Actor, s.MCPClient = "cli:session-fork", "default"
			}
			if freshConfig == nil {
				freshConfig, err = s.loadLaunchConfig(cwd)
				freshIdentity = s.identity
				if s.sessionLaunch != nil {
					freshIdentity = s.sessionLaunch.Identity
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			wantShared := surface == "direct org member" || surface == "HTTP shared host" || surface == "browser shared host"
			if freshIdentity.SharedHost != wantShared || freshIdentity.SealedFS != wantShared {
				t.Fatalf("wrong fresh credential boundary: %#v", freshIdentity)
			}
			// A Direct MCP source is sealed even on a stable, non-shared org
			// wing. Its browser fork must instead match a new browser PTY.
			dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
			if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+cwd+"\nprovider_home=/agent-chosen\nshared_host=true\norg_wing=false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
				if !reflect.DeepEqual(plan.Identity, freshIdentity) || !reflect.DeepEqual(plan.Config, freshConfig) {
					t.Fatalf("fork identity/policy differs from fresh launch: %#v, want %#v / %#v", plan, freshIdentity, freshConfig)
				}
				if eggclient.EffectiveSessionHome(s.Cfg, plan.Identity) != eggclient.EffectiveSessionHome(s.Cfg, freshIdentity) {
					t.Fatal("fork changed provider home")
				}
				return nil
			}
			if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBrowserForkRefusesCWDThatFreshPTYWouldRedirect(t *testing.T) {
	s := forkServerFixture(t)
	root := wingpolicy.CanonicalSessionPath(s.Cfg.Dir)
	subdir := filepath.Join(root, "agent-written")
	if err := os.MkdirAll(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "egg.yaml"), []byte("base: none\nnetwork: '*'\nenv: '*'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), []byte("fs: [deny:/, rw:"+root+"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Paths: config.PathList{{Path: root}}}
	req := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	testBrowserFork(t, s, wc, req, root, false, egg.DefaultEggConfig(), nil)
	start := ws.PTYStart{UserID: "alice", OrgRole: "member", CWD: subdir}
	if _, _, err := eggclient.PrepareBrowserLaunch(wc, &start, root, false, egg.DefaultEggConfig()); err != nil || start.CWD != root {
		t.Fatalf("fresh PTY should use configured root: %#v, %v", start, err)
	}
	dir := filepath.Join(root, "eggs", "source")
	for file, content := range map[string]string{
		"egg.meta":  "agent=claude\ncwd=" + subdir + "\n",
		"chat.meta": "agent=claude\ncwd=" + subdir + "\nagent_session_id=provider\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.spawnFork = func(*eggclient.SessionForkPlan) error { t.Fatal("redirected fork spawned"); return nil }
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err == nil || !strings.Contains(err.Error(), "browser launch paths") {
		t.Fatalf("fork should refuse source cwd: %v", err)
	}
}

func TestSessionForkCLIClientIsIndependentOfAuditActor(t *testing.T) {
	s := forkServerFixture(t)
	s.Actor, s.MCPClient = "cli:session-fork", "coordinator"
	if err := os.WriteFile(filepath.Join(s.Cfg.Dir, "clients.yaml"), []byte("require_client: true\nclients:\n  coordinator:\n    owner: owner\n    grants: [terminal.start]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, _, err := db.ReserveConversation(store.Conversation{ID: "original", OwnerID: "owner", Agent: "claude", CWD: s.Cfg.Dir, SessionID: "source", LaunchKey: "initial", SpecDigest: "initial"}); err != nil {
		t.Fatal(err)
	}
	s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		for i, arg := range plan.Options.AgentArgs {
			if arg == "--mcp-config" && i+1 < len(plan.Options.AgentArgs) {
				data, err := os.ReadFile(plan.Options.AgentArgs[i+1])
				if err != nil {
					return err
				}
				var binding struct {
					Servers map[string]struct {
						Args []string `json:"args"`
					} `json:"mcpServers"`
				}
				if err := json.Unmarshal(data, &binding); err != nil {
					return err
				}
				client := binding.Servers["wingthing"].Args[3]
				clients, err := LoadLocalMCPClientsConfig(s.Cfg)
				if err != nil || eggclient.ValidateSessionName(client) != nil || client != "coordinator" || clients.Clients[client].Owner != "owner" || s.clientActor() != "cli:session-fork" {
					t.Fatalf("invalid fork binding: %s, %v", data, err)
				}
				return nil
			}
		}
		t.Fatal("fork omitted MCP binding")
		return nil
	}
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserForkNeverAdoptsSourceMCPPrincipal(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	s := forkServerFixture(t)
	dir := filepath.Join(s.Cfg.Dir, "eggs", "source")
	if err := eggclient.WriteSessionPrincipal(dir, "administrator"); err != nil {
		t.Fatal(err)
	}
	// A legacy snapshot that refuses direct binding keeps this regression
	// test from starting a provider even if the old adapter adopts the owner.
	if err := os.WriteFile(filepath.Join(dir, "session.launch.json"), []byte(`{"config":"fs: [deny:/, ro:/]"}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(s.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, _, err := db.ReserveConversation(store.Conversation{ID: "original", OwnerID: "administrator", Agent: "claude", CWD: s.Cfg.Dir, SessionID: "source", LaunchKey: "initial", SpecDigest: "initial"}); err != nil {
		t.Fatal(err)
	}
	req := ws.TunnelRequest{SenderUserID: "alice", SenderEmail: "alice@example.com", SenderOrgRole: "owner"}
	_, err = testBrowserControl(t, "dev", context.Background(), s.Cfg, &config.WingConfig{}, req, "session_fork", json.RawMessage(`{"session":"source","name":"branch"}`), s.Cfg.Dir, false)
	if err == nil || !strings.Contains(err.Error(), "conversation not found or not owned") {
		t.Fatalf("browser adopted an egg-directory MCP owner: %v", err)
	}
}

func TestBrowserForkUsesCurrentWingDefault(t *testing.T) {
	s := forkServerFixture(t)
	cwd := wingpolicy.CanonicalSessionPath(s.Cfg.Dir)
	wc := &config.WingConfig{IdleTimeout: "15m", Audit: true}
	req := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "owner"}
	wingDefault := &egg.EggConfig{FS: []string{"deny:/", "rw:" + cwd}, Shell: "/bin/current-wing-shell", Trace: true}
	testBrowserFork(t, s, wc, req, cwd, false, wingDefault, nil)
	s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		if plan.Config.Shell != wingDefault.Shell || !plan.Config.Audit || !plan.Config.Trace || !reflect.DeepEqual(plan.Config.FS, wingDefault.FS) || !s.forkTrace || s.forkIdleTimeout != 15*time.Minute {
			t.Fatalf("fork omitted current wing launch settings: %#v", plan)
		}
		return nil
	}
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err != nil {
		t.Fatal(err)
	}
	if wingDefault.Audit {
		t.Fatal("fork mutated the shared wing default")
	}
}

func TestBrowserForkInitializesCurrentToolsAndCleansUpFailedSpawn(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	t.Setenv("HOME", t.TempDir())
	root, err := os.MkdirTemp("/tmp", "wt-fork-tools-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cfg := &config.Config{Dir: root}
	dir := writeResumeSessionFixture(t, cfg, "source", "alice", "claude", root, "provider", "{}\n")
	if err := eggclient.WriteSessionPrincipal(dir, "owner"); err != nil {
		t.Fatal(err)
	}
	s := testWingServer(t, &Server{Version: "dev", Cfg: cfg, Principal: "owner"})
	tools := []*config.ToolConfig{{Name: "current-tool", Run: "printf configured"}}
	testBrowserFork(t, s, &config.WingConfig{}, ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "owner"}, root, false, egg.DefaultEggConfig(), tools)
	// The fork retains the same immutable snapshot as a fresh browser PTY.
	tools[0] = &config.ToolConfig{Name: "later-tool", Run: "printf later"}
	var socket string
	s.spawnFork = func(plan *eggclient.SessionForkPlan) error {
		socket = plan.Options.ToolSocketPath
		if !reflect.DeepEqual(plan.Options.ToolNames, []string{"current-tool"}) || socket != filepath.Join(root, "eggs", plan.SessionID, ".tools", "tool.sock") {
			t.Fatalf("browser fork omitted current tool options: %#v", plan.Options)
		}
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			return err
		}
		if err := json.NewEncoder(conn).Encode(egg.ToolRequest{Tool: "current-tool"}); err != nil {
			return err
		}
		if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
			return err
		}
		data, err := io.ReadAll(conn)
		if err != nil {
			return err
		}
		var response egg.ToolResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return err
		}
		if response.Stdout != "configured" || response.ExitCode != 0 || response.Error != "" {
			t.Fatalf("fork tool did not execute: %#v", response)
		}
		return errors.New("fixture spawn failed")
	}
	if _, err := s.ToolSessionFork(context.Background(), json.RawMessage(`{"session":"source","name":"branch"}`)); err == nil || !strings.Contains(err.Error(), "fixture spawn failed") {
		t.Fatalf("fork spawn failure: %v", err)
	}
	if conn, err := net.Dial("unix", socket); err == nil {
		_ = conn.Close()
		t.Fatal("failed spawn left a tool listener running")
	}
}
