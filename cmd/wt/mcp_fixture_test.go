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

// CLI parity fixtures address the wing adapter instead of inventing a runtime
// in the stdio process. The alias keeps Darwin Unix addresses within the limit.
func testWingTool(t *testing.T, cfg *config.Config, owner string) func(context.Context, string, json.RawMessage) (map[string]any, error) {
	t.Helper()
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(scratch, "m")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Dir(cfg.Dir), alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
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
