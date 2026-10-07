package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
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
	for _, tool := range RoostNativeMCPTools("test", cfg, true) {
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

func TestRoostTransportsExplainCapturedAdministratorPolicy(t *testing.T) {
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
	admin := "base: none\nfs: [deny:/, ro:/usr, rw:.]\nnetwork: [corp.example]\n"
	malicious := "base: none\nfs: [deny:/, ro:/usr, rw:., ro:/opt/wingthing/support]\nnetwork: ['*']\n"
	if err := os.WriteFile(filepath.Join(workspace, "egg.yaml"), []byte(malicious), 0600); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(admin), 0600); err != nil {
			t.Fatal(err)
		}
		var explain mcppkg.NativeTool
		for _, tool := range RoostNativeMCPTools("test", cfg, shared) {
			if tool.Name == "sandbox_explain" {
				explain = tool
			}
		}
		client, ctx := connectDirectMCPTestClientWithPolicySource(t, cfg, home, shared, webrtcpkg.PeerIdentity{UserID: "eng", Email: "eng@example.com", OrgRole: "member"}, func() (*config.WingConfig, []config.AllowKey) { return wc.Clone(), nil })
		if _, isError, err := client.Call(ctx, "wingthing_capabilities", json.RawMessage(`{}`)); err != nil || isError {
			t.Fatalf("direct runtime not ready: %v, %v", isError, err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(malicious), 0600); err != nil {
			t.Fatal(err)
		}
		args := json.RawMessage(`{"cwd":` + strconv.Quote(workspace) + `}`)
		for _, transport := range []string{"native", "direct"} {
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
			if err != nil || strings.Contains(string(data), "/opt/wingthing/support") || !strings.Contains(string(data), "corp.example") {
				t.Fatalf("%s explained caller policy instead of runtime (shared=%v): %s, %v", transport, shared, data, err)
			}
		}
	}
}

func TestRoostLaunchesIgnoreCallerWorkspacePolicy(t *testing.T) {
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
	admin := "base: none\nfs: [deny:/, ro:/usr, rw:.]\nenv: [HOME]\nnetwork: [corp.example]\n"
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
		server := newRoostNativeMCPServer("test", cfg, shared, NewMCPAdmissionState(), mcppkg.Principal{UserID: "eng", Email: "eng@example.com"}, []string{filepath.Dir(workspace)})
		policy, err := server.loadLaunchConfig(workspace)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(policy.FS, "\n"), "/opt/wingthing/support") || policy.IsAllEnv() || len(policy.Network.Domains) != 1 || policy.Network.Domains[0] != "corp.example" {
			t.Fatalf("roost admitted caller's workspace policy (shared=%v): %#v", shared, policy)
		}
		// Keep the runtime policy stable even if the on-disk global default changes.
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(malicious), 0600); err != nil {
			t.Fatal(err)
		}
		current, err := server.loadLaunchConfig(workspace)
		if err != nil || strings.Contains(strings.Join(current.FS, "\n"), "/opt/wingthing/support") {
			t.Fatalf("launch reloaded caller-writable policy: %#v, %v", current, err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Dir, "egg.yaml"), []byte(admin), 0600); err != nil {
			t.Fatal(err)
		}
		// Both interactive tools resolve through the same runtime loader before
		// admission. Stop at admission so the fixture never launches a real egg.
		server.MaxSpawnsPerHour = 1
		server.admission.spawnTimes[server.clientPrincipal()] = []time.Time{time.Now()}
		for _, tool := range []string{"terminal_start", "agent_start"} {
			var loaded bool
			load := server.launchConfig
			server.launchConfig = func(cwd string) (*egg.EggConfig, error) {
				loaded = true
				return load(cwd)
			}
			args := `{"cwd":` + strconv.Quote(workspace) + `}`
			if tool == "agent_start" {
				args = `{"agent":"claude","cwd":` + strconv.Quote(workspace) + `}`
			}
			_, isError, protocolErr := server.callTool(context.Background(), tool, json.RawMessage(args))
			if !loaded || !isError || protocolErr != nil {
				t.Fatalf("%s bypassed runtime launch policy: loaded=%v error=%v protocol=%v", tool, loaded, isError, protocolErr)
			}
			server.launchConfig = load
		}
		server.MaxSpawnsPerHour = 60
		delete(server.admission.spawnTimes, server.clientPrincipal())
		started := make(chan *store.Task, 4)
		server.runAgentTask = func(_ context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, options taskrun.TaskRunOptions) error {
			started <- task
			if options.SharedHost != shared || !strings.HasPrefix(options.UserHome, cfg.Dir) {
				return fmt.Errorf("lost shared/org task identity: %+v", options)
			}
			return taskStore.UpdateTaskStatus(task.ID, "done")
		}
		result, err := server.toolAgentRun(json.RawMessage(`{"prompt":"inspect","agent":"claude","cwd":` + strconv.Quote(workspace) + `}`))
		if err != nil {
			t.Fatal(err)
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := server.waitForAgentRunTerminal(waitCtx, result["run_id"].(string)); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		// Cancellation keeps the baseline runner from invoking an installed
		// provider if this test is replayed before the runtime policy fix.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _ = server.toolSwarmRun(ctx, json.RawMessage(`{"cwd":`+strconv.Quote(workspace)+`,"nodes":[{"id":"one","prompt":"inspect","agent":"claude"}]}`))
		for range 2 {
			select {
			case task := <-started:
				if task.EggConfigYAML == "" || strings.Contains(task.EggConfigYAML, "/opt/wingthing/support") || !strings.Contains(task.EggConfigYAML, "corp.example") {
					t.Fatalf("headless %s rediscovered caller policy: %s", task.Type, task.EggConfigYAML)
				}
			case <-time.After(time.Second):
				t.Fatal("headless launch did not use the owner-scoped runner")
			}
		}
	}
}
