package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// Create a symlinked parent inside each fixture without depending on the
// checkout's path length or macOS's /var spelling. Egg addresses use this
// short alias; state and provider data remain in the test-owned target.
func shortFixtureAlias(t *testing.T, target string) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wt-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	alias := filepath.Join(root, "a")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if err := egg.ValidateSocketPath(filepath.Join(alias, "s", "eggs", "0123456789abcdef", "egg.sock")); err != nil {
		t.Fatal(err)
	}
	return alias
}

// CLI parity fixtures address the wing adapter instead of inventing a runtime
// in the stdio process. The alias keeps Darwin Unix addresses within the limit.
func testWingTool(t *testing.T, cfg *config.Config, owner string) func(context.Context, string, json.RawMessage) (map[string]any, error) {
	t.Helper()
	alias := shortFixtureAlias(t, filepath.Dir(cfg.Dir))
	copyCfg := *cfg
	copyCfg.Dir = filepath.Join(alias, filepath.Base(cfg.Dir))
	copyCfg.WingID = "fixture-wing"
	wc := &config.WingConfig{WingID: copyCfg.WingID}
	service := &wingsession.Service{Config: &copyCfg, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	listener, err := localmcp.ListenLocalWingControl(t.Context(), version, service, owner, localmcp.NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := controlsocket.Dial(t.Context(), copyCfg.Dir, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		result, denied, err := client.Call(ctx, name, args)
		if err == nil && denied {
			err = fmt.Errorf("%v", result["error"])
		}
		return result, err
	}
}
