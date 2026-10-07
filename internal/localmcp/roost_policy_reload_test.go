package localmcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
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
