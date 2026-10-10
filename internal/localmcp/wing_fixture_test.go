package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Handler fixtures supply a wing-owned service explicitly. The real transport
// fixture separately proves egg registration and disconnect independence.
func testWingServer(t *testing.T, s *Server) *Server {
	if t != nil {
		t.Helper()
	}
	if s.Sessions != nil || s.Cfg == nil {
		return s
	}
	if s.identity.UserID == "" {
		s.identity.UserID = "fixture-user"

	}
	if s.Principal == "" || s.Principal == "default" {
		s.Principal = wingsession.UserPrincipal(s.identity.UserID)
		s.legacyLocalDefault = true
	}
	if s.sessionRole == "" {
		s.sessionRole = "owner"
	}
	entries, _ := os.ReadDir(filepath.Join(s.Cfg.Dir, "eggs"))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.Cfg.Dir, "eggs", entry.Name())
		if eggclient.ReadEggOwner(dir) == "" {
			if err := eggclient.WriteEggOwner(dir, s.identity.UserID, s.identity.Email); err != nil {
				if t != nil {
					t.Fatal(err)
				} else {
					panic(err)
				}
			}
		}
	}
	wc, err := config.LoadWingConfig(s.Cfg.Dir)
	if err != nil {
		wc = &config.WingConfig{}
	}
	wc.AllowUnsandboxed = s.Unsandboxed
	defaultEgg, err := loadRuntimeEggDefault(s.Cfg.Dir, wc)
	if err != nil {
		defaultEgg = egg.DefaultEggConfig()
	}
	home, _ := os.UserHomeDir()
	s.Sessions = &wingsession.Service{Config: s.Cfg, Home: home, SharedHost: s.identity.SharedHost, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: defaultEgg} }, Register: func(string) error { return nil }}
	s.Sessions.Spawn = func(launch *wingsession.Launch, opts wingsession.StartOptions) (*egg.Client, error) {
		if s.spawnFork == nil {
			if err := egg.ValidateSocketPath(filepath.Join(s.Cfg.Dir, "eggs", opts.SessionID, "egg.sock")); err != nil {
				return nil, err
			}
			return nil, errors.New("fixture did not authorize egg spawn")
		}
		source, err := eggclient.ResolveOwnedLifecycleSession(s.Cfg, opts.Egg.ResumeSourceSessionID, s.ownsSession)
		if err != nil {
			return nil, err
		}
		db, err := store.Open(s.Cfg.DBPath())
		if err != nil {
			return nil, err
		}
		defer db.Close()
		c, _ := db.ConversationForSession(opts.SessionID)
		plan := &eggclient.SessionForkPlan{SessionID: opts.SessionID, Source: source, Config: launch.Config, Identity: launch.Identity, Options: opts.Egg, Conversation: c}
		return nil, s.spawnFork(plan)
	}
	if s.admission != nil {
		s.admission.Sessions = s.Sessions
	}
	return s
}

func testNativeServer(t *testing.T, version string, cfg *config.Config, shared bool, admission *AdmissionState, principal mcppkg.Principal, paths []string, sources ...func() (*config.WingConfig, *egg.EggConfig)) *Server {
	t.Helper()
	s := testWingServer(t, newRoostNativeMCPServer(version, cfg, shared, admission, principal, paths, sources...))
	if len(sources) > 0 {
		s.Sessions.Policy = func() wingsession.Policy { wc, ec := sources[0](); return wingsession.Policy{Wing: wc, Egg: ec} }
	}
	return s
}

func testNativeTools(t *testing.T, version string, cfg *config.Config, shared bool, sources ...func() (*config.WingConfig, *egg.EggConfig)) []mcppkg.NativeTool {
	t.Helper()
	home, _ := os.UserHomeDir()
	var source func() (*config.WingConfig, *egg.EggConfig)
	if len(sources) > 0 {
		source = sources[0]
	} else {
		wc, err := config.LoadWingConfig(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		ec, err := loadRuntimeEggDefault(cfg.Dir, wc)
		if err != nil {
			t.Fatal(err)
		}
		source = func() (*config.WingConfig, *egg.EggConfig) { return wc, ec }
	}
	service := &wingsession.Service{Config: cfg, Home: home, SharedHost: shared, Policy: func() wingsession.Policy { wc, ec := source(); return wingsession.Policy{Wing: wc, Egg: ec} }}
	return RoostNativeMCPToolsWithSessions(version, cfg, shared, func() *wingsession.Service { return service }, source)
}

func testBrowserFork(t *testing.T, s *Server, wc *config.WingConfig, req ws.TunnelRequest, home string, shared bool, defaultEgg *egg.EggConfig, tools []*config.ToolConfig) {
	t.Helper()
	configureBrowserFork(s, wc, req, home, shared, defaultEgg, tools)
	s.launchConfig = nil
	s.sessionRole = req.SenderOrgRole
	s.sessionBrowser = true
	s.Sessions.Home = home
	s.Sessions.SharedHost = shared
	s.Sessions.Policy = func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: defaultEgg, Audit: wc.Audit} }
}

func shortWingTestState(t *testing.T) string {
	t.Helper()
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
	state := filepath.Join(alias, "s")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	return state
}

func testBrowserControl(t *testing.T, version string, ctx context.Context, cfg *config.Config, wc *config.WingConfig, req ws.TunnelRequest, operation string, arguments json.RawMessage, home string, shared bool, launchConfig ...BrowserLaunchConfig) (map[string]any, error) {
	t.Helper()
	if len(launchConfig) == 0 {
		launchConfig = append(launchConfig, BrowserLaunchConfig{})
	}
	if launchConfig[0].Sessions == nil {
		launchConfig[0].Sessions = &wingsession.Service{Config: cfg, Home: home, SharedHost: shared, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	}
	return BrowserSessionControl(version, ctx, cfg, wc, req, operation, arguments, home, shared, launchConfig...)
}
