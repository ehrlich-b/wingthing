package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestRoostMCPPathsDoNotReloadLegacyWritablePolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &config.Config{Dir: filepath.Join(home, "state")}
	workspace := filepath.Join(home, "workspace")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{cfg.Dir, workspace, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Paths: config.PathList{{Path: workspace}}}); err != nil {
		t.Fatal(err)
	}
	var list mcppkg.NativeTool
	for _, tool := range testNativeTools(t, "test", cfg, true) {
		if tool.Name == "terminal_list" {
			list = tool
		}
	}
	// Keep an unlinked writable descriptor, as a legacy egg can do.
	policy := filepath.Join(cfg.Dir, "wing.yaml")
	writer, err := os.OpenFile(policy, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	legacy := filepath.Join(cfg.Dir, "eggs", "legacy")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"egg.pid": strconv.Itoa(os.Getpid()), "egg.meta": "cwd=" + outside + "\n"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := eggclient.WriteEggOwner(legacy, "alice", "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionPrincipal(legacy, roostSessionPrincipal("alice")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("paths: ["+outside+"]\n"), 0); err != nil {
		t.Fatal(err)
	}
	result, isError, err := list.Call(context.Background(), mcppkg.Principal{UserID: "alice", Email: "alice@example.com"}, json.RawMessage(`{}`))
	if err != nil || isError {
		t.Fatalf("captured policy unavailable: %v, %v", result, err)
	}
	if sessions := result["sessions"].([]eggclient.LocalSession); len(sessions) != 0 {
		t.Fatalf("native MCP reloaded injected path authorization: %+v", sessions)
	}
}

func TestRoostTransportsSelectedRootACL(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	cfg := &config.Config{Dir: filepath.Join(home, "state"), DefaultAgent: "claude"}
	root := filepath.Join(home, "work")
	child := filepath.Join(home, "Restricted")
	sub := filepath.Join(child, "sub")
	for _, dir := range []string{cfg.Dir, root, sub} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{root, child} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), roostRolePolicy(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Paths: config.PathList{
		{Path: root, Members: []string{"alice@example.com"}},
		{Path: child, Members: []string{"bob@example.com"}},
	}}
	principal := mcppkg.Principal{UserID: "alice", Email: "alice@example.com"}
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%v", shared), func(t *testing.T) {
			wc.Org = "org"
			if shared {
				wc.Org = ""
			}
			if err := config.SaveWingConfig(cfg.Dir, wc); err != nil {
				t.Fatal(err)
			}
			source := func() (*config.WingConfig, *egg.EggConfig) { return wc, egg.DefaultEggConfig() }
			var explain mcppkg.NativeTool
			for _, tool := range testNativeTools(t, "test", cfg, shared, source) {
				if tool.Name == "sandbox_explain" {
					explain = tool
				}
			}
			client, ctx := connectDirectMCPTestClientWithPolicySource(t, cfg, home, shared, webrtcpkg.PeerIdentity{UserID: principal.UserID, Email: principal.Email, OrgRole: "member"}, func() (*config.WingConfig, []config.AllowKey) { return wc.Clone(), nil })
			args := json.RawMessage(`{"cwd":` + strconv.Quote(sub) + `}`)
			result, isError, err := explain.Call(ctx, principal, args)
			if !isError || !strings.Contains(fmt.Sprint(result, err), child) {
				t.Errorf("native MCP admitted Bob's child policy: %v, %v", result, err)
			}
			result, isError, err = client.Call(ctx, "sandbox_explain", args)
			if !isError || !strings.Contains(fmt.Sprint(result, err), child) {
				t.Errorf("direct MCP admitted Bob's child policy: %v, %v", result, err)
			}
			server := testNativeServer(t, "test", cfg, shared, NewMCPAdmissionState(), principal, []string{root}, source)
			newRunWingFixture(t, server)
			// Prevent provider execution even when replayed without the ACL fix.
			server.MaxSpawnsPerHour = 1
			server.admission.spawnTimes[server.clientPrincipal()] = []time.Time{time.Now()}
			if _, err := server.toolAgentRun(json.RawMessage(`{"prompt":"inspect","agent":"claude","cwd":` + strconv.Quote(sub) + `}`)); err == nil || !strings.Contains(err.Error(), child) {
				t.Errorf("headless agent did not reject selected-root ACL: %v", err)
			}
			testBrowserFork(t, server, wc, ws.TunnelRequest{SenderUserID: principal.UserID, SenderEmail: principal.Email, SenderOrgRole: "member"}, home, shared, egg.DefaultEggConfig(), nil)
			if _, err := server.loadLaunchConfig(sub); err == nil || !strings.Contains(err.Error(), child) {
				t.Errorf("browser fork admitted Bob's child policy: %v", err)
			}
			// An omitted cwd selects Bob's first accessible root, rather than
			// the roost process directory or Alice's first configured root.
			bob := mcppkg.Principal{UserID: "bob", Email: "bob@example.com"}
			assertChildPolicy := func(result map[string]any, isError bool, err error) {
				t.Helper()
				data, marshalErr := json.Marshal(result)
				if err != nil || isError || marshalErr != nil || !strings.Contains(string(data), `"source":`+strconv.Quote(child)) {
					t.Fatalf("omitted cwd did not select Bob's policy: %s, %v", data, err)
				}
			}
			result, isError, err = explain.Call(ctx, bob, json.RawMessage(`{}`))
			assertChildPolicy(result, isError, err)
			bobClient, bobCtx := connectDirectMCPTestClientWithPolicySource(t, cfg, home, shared, webrtcpkg.PeerIdentity{UserID: bob.UserID, Email: bob.Email, OrgRole: "member"}, func() (*config.WingConfig, []config.AllowKey) { return wc.Clone(), nil })
			result, isError, err = bobClient.Call(bobCtx, "sandbox_explain", json.RawMessage(`{}`))
			assertChildPolicy(result, isError, err)
			bobServer := testNativeServer(t, "test", cfg, shared, NewMCPAdmissionState(), bob, []string{child}, source)
			bobRuns := newRunWingFixture(t, bobServer)
			expected, err := bobServer.loadLaunchConfig(child)
			if err != nil {
				t.Fatal(err)
			}
			expectedYAML, err := expected.TaskYAML()
			if err != nil {
				t.Fatal(err)
			}
			// Local orchestration handlers share this authenticated launch policy.
			bobServer.Surface, bobServer.Grants = control.SurfaceLocalMCP, nil
			for _, tool := range []string{"agent_run"} {
				args := `{"prompt":"inspect","agent":"claude"}`
				result, isError, protocolErr := bobServer.callTool(context.Background(), tool, json.RawMessage(args))
				if protocolErr != nil || isError {
					t.Fatalf("omitted cwd bypassed %s loader: %v, %v", tool, result, protocolErr)
				}
				launch := <-bobRuns.launches
				actualYAML, err := launch.Config.TaskYAML()
				if err != nil || launch.CWD != child || actualYAML != expectedYAML {
					t.Fatalf("omitted cwd selected wrong launch: cwd=%s config=%s, %v", launch.CWD, actualYAML, err)
				}
				id := result["run_id"].(string)
				if got := <-bobRuns.submitted; got != id {
					t.Fatalf("submitted %s, want %s", got, id)
				}
				bobRuns.finish(t, id, "fixture complete", "")
				bobRuns.wait(t, id)
			}
		})
	}
}

func TestRoostTransportsRejectUnsafeRoots(t *testing.T) {
	for _, kind := range []string{"nested", "symlink", "symlink ancestor"} {
		t.Run(kind, func(t *testing.T) {
			home := config.CanonicalProviderPath(t.TempDir())
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
			cfg := &config.Config{Dir: filepath.Join(home, "state"), DefaultAgent: "claude"}
			root := filepath.Join(home, "work")
			child := filepath.Join(root, "Restricted")
			for _, dir := range []string{cfg.Dir, root} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "egg.yaml"), roostRolePolicy(root), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "nested" {
				if err := os.MkdirAll(child, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(home, "bob", "sub")
				if err := os.MkdirAll(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(target), child); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink ancestor" {
					child = filepath.Join(child, "sub")
				}
			}
			wc := &config.WingConfig{Paths: config.PathList{{Path: root}, {Path: child}}, Admins: []string{"admin@example.com"}}
			checkErr := func(err error) {
				t.Helper()
				want := "symlink"
				if kind == "nested" {
					want = "nested roost paths"
				}
				if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), child) || kind == "nested" && !strings.Contains(err.Error(), root) {
					t.Errorf("unsafe configured roots were admitted: %v", err)
				}
			}
			for _, shared := range []bool{false, true} {
				wc.Org = "org"
				if shared {
					wc.Org = ""
				}
				if err := config.SaveWingConfig(cfg.Dir, wc); err != nil {
					t.Fatal(err)
				}
				snapshot := wc.Clone()
				source := func() (*config.WingConfig, *egg.EggConfig) { return snapshot, egg.DefaultEggConfig() }
				for _, role := range []string{"member", "admin"} {
					principal := mcppkg.Principal{UserID: role, Email: role + "@example.com"}
					var explain mcppkg.NativeTool
					for _, tool := range testNativeTools(t, "test", cfg, shared, source) {
						if tool.Name == "sandbox_explain" {
							explain = tool
						}
					}
					client, ctx := connectDirectMCPTestClientWithPolicySource(t, cfg, home, shared, webrtcpkg.PeerIdentity{UserID: principal.UserID, Email: principal.Email, OrgRole: role}, func() (*config.WingConfig, []config.AllowKey) { return snapshot.Clone(), nil })
					args := json.RawMessage(`{"cwd":` + strconv.Quote(root) + `}`)
					result, isError, err := explain.Call(ctx, principal, args)
					if !isError {
						t.Errorf("native MCP admitted unsafe roots: %v, %v", result, err)
					}
					checkErr(fmt.Errorf("%v %v", result, err))
					result, isError, err = client.Call(ctx, "sandbox_explain", args)
					if !isError {
						t.Errorf("direct MCP admitted unsafe roots: %v, %v", result, err)
					}
					checkErr(fmt.Errorf("%v %v", result, err))
					server := testNativeServer(t, "test", cfg, shared, NewMCPAdmissionState(), principal, []string{root}, source)
					newRunWingFixture(t, server)
					// Refuse execution even when replayed before root validation.
					load := server.launchConfig
					server.launchConfig = func(cwd string) (*egg.EggConfig, error) {
						if _, err := load(cwd); err != nil {
							return nil, err
						}
						return nil, fmt.Errorf("stop before provider execution")
					}
					_, err = server.toolAgentRun(json.RawMessage(`{"prompt":"inspect","agent":"claude","cwd":` + strconv.Quote(root) + `}`))
					checkErr(err)
					testBrowserFork(t, server, wc, ws.TunnelRequest{SenderUserID: principal.UserID, SenderEmail: principal.Email, SenderOrgRole: role}, home, shared, egg.DefaultEggConfig(), nil)
					_, err = server.loadLaunchConfig(root)
					checkErr(err)
				}
			}
		})
	}
}

func TestRoostTransportsExplainRoleRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	cfg := &config.Config{Dir: filepath.Join(home, "state"), DefaultAgent: "claude"}
	workspace := filepath.Join(home, "eng", "sub")
	for _, dir := range []string{cfg.Dir, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	wc := &config.WingConfig{Org: "org", Paths: config.PathList{{Path: filepath.Dir(workspace), Members: []string{"eng@example.com"}}}}
	if err := config.SaveWingConfig(cfg.Dir, wc); err != nil {
		t.Fatal(err)
	}
	admin := "base: none\nfs: [deny:/, ro:/usr, rw:.]\nnetwork: [wing.example]\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(workspace), "egg.yaml"), roostRolePolicy(filepath.Dir(workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	malicious := "base: none\nfs: [deny:/, ro:/usr, rw:., ro:/opt/wingthing/support]\nnetwork: ['*']\n"
	if err := os.WriteFile(filepath.Join(workspace, "egg.yaml"), []byte(malicious), 0600); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		wc.Org = "org"
		if shared {
			wc.Org = ""
		}
		if err := config.SaveWingConfig(cfg.Dir, wc); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(admin), 0600); err != nil {
			t.Fatal(err)
		}
		var explain mcppkg.NativeTool
		for _, tool := range testNativeTools(t, "test", cfg, shared) {
			if tool.Name == "sandbox_explain" {
				explain = tool
			}
		}
		// Earlier iterations' servers keep running; give each its own snapshot.
		snapshot := wc.Clone()
		client, ctx := connectDirectMCPTestClientWithPolicySource(t, cfg, home, shared, webrtcpkg.PeerIdentity{UserID: "eng", Email: "eng@example.com", OrgRole: "member"}, func() (*config.WingConfig, []config.AllowKey) { return snapshot.Clone(), nil })
		if _, isError, err := client.Call(ctx, "wingthing_capabilities", json.RawMessage(`{}`)); err != nil || isError {
			t.Fatalf("direct runtime not ready: %v, %v", isError, err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(malicious), 0600); err != nil {
			t.Fatal(err)
		}
		args := json.RawMessage(`{"cwd":` + strconv.Quote(workspace) + `}`)
		for _, transport := range []string{"native", "direct"} {
			t.Run(fmt.Sprintf("%s/shared=%v", transport, shared), func(t *testing.T) {
				var result map[string]any
				var isError bool
				var err error
				if transport == "native" {
					result, isError, err = explain.Call(ctx, mcppkg.Principal{UserID: "eng", Email: "eng@example.com"}, args)
				} else {
					result, isError, err = client.Call(ctx, "sandbox_explain", args)
				}
				if err != nil || isError {
					t.Fatalf("%s shared=%v: %v, %v, %v", transport, shared, result, isError, err)
				}
				data, err := json.Marshal(result)
				if err != nil || strings.Contains(string(data), `"source":"/opt/wingthing/support"`) || !strings.Contains(string(data), "corp.example") {
					t.Fatalf("%s explained caller policy instead of runtime (shared=%v): %s, %v", transport, shared, data, err)
				}
			})
		}
	}
}

func TestRoostLaunchesUseRoleRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	cfg := &config.Config{Dir: filepath.Join(home, "state"), DefaultAgent: "claude"}
	workspace := filepath.Join(home, "eng", "sub")
	for _, dir := range []string{cfg.Dir, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Org: "org", Paths: config.PathList{{Path: filepath.Dir(workspace), Members: []string{"eng@example.com"}}}}); err != nil {
		t.Fatal(err)
	}
	admin := "base: none\nfs: [deny:/, ro:/usr, rw:.]\nenv: [HOME]\nnetwork: [wing.example]\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(workspace), "egg.yaml"), roostRolePolicy(filepath.Dir(workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	malicious := "base: none\nfs: [deny:/, ro:/usr, rw:., ro:/opt/wingthing/support]\nenv: ['*']\nnetwork: ['*']\n"
	for _, path := range []string{filepath.Join(cfg.Dir, "egg.yaml"), filepath.Join(workspace, "egg.yaml")} {
		data := admin
		if filepath.Dir(path) == workspace {
			data = malicious
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, shared := range []bool{false, true} {
		org := "org"
		if shared {
			org = ""
		}
		if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Org: org, Paths: config.PathList{{Path: filepath.Dir(workspace), Members: []string{"eng@example.com"}}}}); err != nil {
			t.Fatal(err)
		}
		server := testNativeServer(t, "test", cfg, shared, NewMCPAdmissionState(), mcppkg.Principal{UserID: "eng", Email: "eng@example.com"}, []string{filepath.Dir(workspace)})
		policy, err := server.loadLaunchConfig(workspace)
		if err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, policy)
		rootPolicy, err := server.loadLaunchConfig(filepath.Dir(workspace))
		if err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, rootPolicy)
		// Keep the runtime policy stable even if the on-disk global default changes.
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(malicious), 0600); err != nil {
			t.Fatal(err)
		}
		current, err := server.loadLaunchConfig(workspace)
		if err != nil {
			t.Fatalf("launch reloaded caller-writable policy: %#v, %v", current, err)
		}
		assertRoostRolePolicy(t, current)
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(admin), 0600); err != nil {
			t.Fatal(err)
		}
		wc, err := config.LoadWingConfig(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		server.Sessions = &wingsession.Service{Config: cfg, Home: home, SharedHost: shared, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
		// Both interactive tools resolve through the same runtime loader before
		// admission. Stop at admission so the fixture never launches a real egg.
		server.MaxSpawnsPerHour = 1
		server.admission.spawnTimes[server.clientPrincipal()] = []time.Time{time.Now()}
		for _, tool := range []string{"terminal_start", "agent_start"} {
			var loaded bool
			load := server.Sessions.Policy
			server.Sessions.Policy = func() wingsession.Policy { loaded = true; return load() }

			args := `{"cwd":` + strconv.Quote(workspace) + `}`
			if tool == "agent_start" {
				args = `{"agent":"claude","cwd":` + strconv.Quote(workspace) + `}`
			}
			_, isError, protocolErr := server.callTool(context.Background(), tool, json.RawMessage(args))
			if !loaded || !isError || protocolErr != nil {
				t.Fatalf("%s bypassed runtime launch policy: loaded=%v error=%v protocol=%v", tool, loaded, isError, protocolErr)
			}
			assertRoostRolePolicy(t, server.sessionLaunch.Config)
			server.Sessions.Policy = load
		}
		server.MaxSpawnsPerHour = 60
		delete(server.admission.spawnTimes, server.clientPrincipal())
		f := newRunWingFixture(t, server)
		result, err := server.toolAgentRun(json.RawMessage(`{"prompt":"inspect","agent":"claude","cwd":` + strconv.Quote(workspace) + `}`))
		if err != nil {
			t.Fatal(err)
		}
		id := result["run_id"].(string)
		<-f.submitted
		launch := <-f.launches
		if launch.Identity.SharedHost != shared || (!launch.Identity.OrgWing && !shared) || launch.Identity.UserID != "eng" {
			t.Fatalf("lost shared/org egg identity: %+v", launch.Identity)
		}
		assertRoostRolePolicy(t, launch.Config)
		db, err := store.Open(cfg.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		task, err := db.GetTask(id)
		db.Close()
		if err != nil || task == nil {
			t.Fatalf("missing task: %v", err)
		}
		frozen, err := egg.LoadTaskEggConfigFromYAML(task.EggConfigYAML)
		if err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, frozen)
		if err := os.WriteFile(filepath.Join(filepath.Dir(workspace), "egg.yaml"), []byte(malicious), 0600); err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, frozen)
		f.finish(t, id, "done", "")
		f.wait(t, id)
		if err := os.WriteFile(filepath.Join(filepath.Dir(workspace), "egg.yaml"), roostRolePolicy(filepath.Dir(workspace)), 0600); err != nil {
			t.Fatal(err)
		}

	}
}

func TestRoostContinuationUsesRoleRootPolicy(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%v", shared), func(t *testing.T) {
			s, _, c, _ := continuationFixture(t)
			config.ReleaseChannel = "stable"
			s.Unsandboxed = false
			adminPath := filepath.Join(s.Cfg.Dir, "admin.yaml")
			if err := os.WriteFile(adminPath, []byte("base: none\nfs: [deny:/, rw:.]\nnetwork: [corp.example]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			org := "org"
			if shared {
				org = ""
			}
			root := c.CWD
			c.CWD = filepath.Join(root, "sub")
			if err := os.MkdirAll(c.CWD, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "egg.yaml"), roostRolePolicy(root), 0600); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveWingConfig(s.Cfg.Dir, &config.WingConfig{Org: org, EggConfig: adminPath, Paths: config.PathList{{Path: root}}}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(c.CWD, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\nnetwork: ['*']\n"), 0600); err != nil {
				t.Fatal(err)
			}
			server := testNativeServer(t, "test", s.Cfg, shared, NewMCPAdmissionState(), mcppkg.Principal{UserID: "eng"}, []string{root})
			s.identity = server.identity
			loaded := false
			s.sessionRole = server.sessionRole
			s.Sessions = server.Sessions
			originalPolicy := s.Sessions.Policy
			s.Sessions.Policy = func() wingsession.Policy { loaded = true; return originalPolicy() }
			// Refuse at admission before restoring history or spawning an egg.
			s.MaxSpawnsPerHour = 1
			s.spawnTimes = []time.Time{time.Now()}
			err := s.launchHeadlessContinuation(c, &store.ConversationContinuation{}, continuationModel, "inspect")
			if !loaded || err == nil || !strings.Contains(err.Error(), "max_spawns_per_hour") {
				t.Fatalf("continuation bypassed runtime loader: loaded=%v, err=%v", loaded, err)
			}
		})
	}
}

func TestPersonalMCPLaunchUsesWebPathPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	cfg := &config.Config{Dir: filepath.Join(home, "state")}
	workspace := filepath.Join(home, "workspace", "sub")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "egg.yaml"), []byte("base: none\nnetwork: [workspace.example]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Paths: config.PathList{{Path: filepath.Dir(workspace)}}}); err != nil {
		t.Fatal(err)
	}
	server := testNativeServer(t, "test", cfg, false, NewMCPAdmissionState(), mcppkg.Principal{UserID: "owner"}, []string{filepath.Dir(workspace)})
	policy, err := server.loadLaunchConfig(workspace)
	if err == nil || !strings.Contains(err.Error(), "current launch paths") {
		t.Fatalf("personal MCP accepted a CWD that web redirects: %#v, %v", policy, err)
	}
}

func roostRolePolicy(root string) []byte {
	return []byte("base: none\nfs: [deny:/, ro:/usr, rw:" + root + ", deny:/opt/wingthing/support, deny:/opt/wingthing/.ssh, deny-write:./egg.yaml]\nnetwork: [corp.example]\nenv: [HOME, WT_USER, WT_USER_EMAIL, WT_SESSION_ID, DISABLE_TELEMETRY=1]\nresources: {cpu: 7200s, max_fds: 2048}\nshell: /bin/bash\naudit: true\ndangerously_skip_permissions: true\n")
}

func assertRoostRolePolicy(t *testing.T, cfg *egg.EggConfig) {
	t.Helper()
	if !cfg.Audit || !cfg.DangerouslySkipPermissions || cfg.Shell != "/bin/bash" || cfg.Resources.CPU != "7200s" || cfg.Resources.MaxFDs != 2048 || cfg.IsAllEnv() || !eggclient.ContainsExactPath(cfg.Network.Domains, "corp.example") {
		t.Fatalf("role policy lost: %#v", cfg)
	}
	for _, env := range []string{"WT_USER", "WT_USER_EMAIL", "WT_SESSION_ID", "DISABLE_TELEMETRY=1"} {
		if !eggclient.ContainsExactPath(cfg.Env, env) {
			t.Fatalf("role env %s lost", env)
		}
	}
	sandbox := cfg.ToSandboxConfig("")
	for _, mount := range sandbox.Mounts {
		if mount.Source == "/opt/wingthing/support" {
			t.Fatalf("planted support mount admitted: %#v", cfg)
		}
	}
	for _, deny := range []string{"/opt/wingthing/support", "/opt/wingthing/.ssh"} {
		if !eggclient.ContainsExactPath(sandbox.Deny, deny) {
			t.Fatalf("role deny %s lost", deny)
		}
	}
}

func TestRoostMCPRolePolicyMissingAndAdminFallback(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	cfg := &config.Config{Dir: filepath.Join(home, "state")}
	root := filepath.Join(home, "eng")
	sub := filepath.Join(root, "sub")
	outside := filepath.Join(home, "outside")
	for _, dir := range []string{cfg.Dir, sub, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{sub, outside, home} {
		if err := os.WriteFile(filepath.Join(dir, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaultPolicy := &egg.EggConfig{Shell: "/bin/wing-default"}
	wantFS := []string{"deny-write:" + filepath.Join(root, "egg.yaml")}
	for dir := root; dir != "/"; dir = filepath.Dir(dir) {
		wantFS = append(wantFS, "deny-rename:"+dir)
	}
	for _, shared := range []bool{false, true} {
		org := "org"
		if shared {
			org = ""
		}
		wc := &config.WingConfig{Org: org, Admins: []string{"admin@example.com"}, Paths: config.PathList{{Path: root}}}
		source := func() (*config.WingConfig, *egg.EggConfig) { return wc, defaultPolicy }
		for _, email := range []string{"member@example.com", "admin@example.com"} {
			server := testNativeServer(t, "test", cfg, shared, NewMCPAdmissionState(), mcppkg.Principal{UserID: "user", Email: email}, []string{root}, source)
			for _, cwd := range []string{root, sub, outside} {
				policy, err := server.loadLaunchConfig(cwd)
				if email == "member@example.com" {
					if err == nil || cwd != outside && !strings.Contains(err.Error(), "ask the wing owner") {
						t.Fatalf("member fallback admitted caller policy at %s: %#v, %v", cwd, policy, err)
					}
				} else if err != nil || policy.Shell != defaultPolicy.Shell || !slices.Equal(policy.FS, wantFS) {
					t.Fatalf("admin fallback admitted caller policy at %s: %#v, %v", cwd, policy, err)
				}
			}
		}
	}
}

func TestRoostBrowserForkUsesRoleRootPolicyInSubdirectory(t *testing.T) {
	s := forkServerFixture(t)
	root := config.CanonicalProviderPath(s.Cfg.Dir)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), roostRolePolicy(root), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		org := "org"
		if shared {
			org = ""
		}
		wc := &config.WingConfig{Org: org, Paths: config.PathList{{Path: root}}}
		req := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
		testBrowserFork(t, s, wc, req, root, shared, egg.DefaultEggConfig(), nil)
		policy, err := s.loadLaunchConfig(sub)
		if err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, policy)
	}
}

func TestRoostHeadlessSubmissionUsesRoleRootPolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	cfg := &config.Config{Dir: filepath.Join(home, "state"), DefaultAgent: "claude"}
	root := filepath.Join(home, "eng")
	sub := filepath.Join(root, "sub")
	for _, dir := range []string{cfg.Dir, sub} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "egg.yaml"), roostRolePolicy(root), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "egg.yaml"), []byte("base: none\nfs: [ro:/opt/wingthing/support]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Org: "org", Paths: config.PathList{{Path: root}}}); err != nil {
		t.Fatal(err)
	}
	server := testNativeServer(t, "test", cfg, false, NewMCPAdmissionState(), mcppkg.Principal{UserID: "eng"}, []string{root})
	f := newRunWingFixture(t, server)
	result, err := server.toolAgentRun(json.RawMessage(`{"prompt":"pending","agent":"claude","cwd":` + strconv.Quote(sub) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	parent := result["run_id"].(string)
	<-f.submitted
	t.Run("agent_run", func(t *testing.T) {
		result, err := server.wingSubmitRun(wingsession.RunRequest{Prompt: "inspect", Agent: "claude", CWD: sub, ParentID: parent, Direction: "inspect"})
		if err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(cfg.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		task, err := db.GetTask(result["run_id"].(string))
		if err != nil || task == nil || task.EggConfigYAML == "" {
			t.Fatalf("missing run launch snapshot: %+v %v", task, err)
		}
		policy, err := egg.LoadTaskEggConfigFromYAML(task.EggConfigYAML)
		if err != nil {
			t.Fatal(err)
		}
		assertRoostRolePolicy(t, policy)
		select {
		case id := <-f.submitted:
			t.Fatalf("queued egg executed: %s", id)
		default:
		}
	})
	if _, err := server.toolAgentStop(runArgs(parent)); err != nil {
		t.Fatal(err)
	}
}
