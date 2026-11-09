package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestRoleScopedLifecycleSettingsComposeAndRepoke(t *testing.T) {
	for _, role := range []string{"", "coordinator"} {
		t.Run("role="+role, func(t *testing.T) {
			state, home := testsocket.Dir(t), testsocket.Dir(t)
			dir := filepath.Join(state, "eggs", "ours")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if role != "" {
				if err := sessionrole.Write(dir, sessionrole.Metadata{Role: role, Pinned: true}); err != nil {
					t.Fatal(err)
				}
			}
			args, err := prepareClaudeLifecycleArgs([]string{"--mcp-config", "/fixture/mcp.json", "--strict-mcp-config", "--settings", `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]}}`}, home, dir, "provider", home)
			if err != nil {
				t.Fatal(err)
			}
			if args[0] != "--mcp-config" || args[1] != "/fixture/mcp.json" || args[2] != "--strict-mcp-config" {
				t.Fatal(args)
			}
			data, err := os.ReadFile(args[len(args)-1])
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			if settings["theme"] != "dark" {
				t.Fatal("personal settings lost")
			}
			hooks := settings["hooks"].(map[string]any)["SessionStart"].([]any)
			want := 2
			if role != "" {
				want = 3
			}
			if len(hooks) != want {
				t.Fatalf("hooks=%v", hooks)
			}
			if strings.Contains(string(data), "you are the coordinator node") != (role != "") {
				t.Fatal("role leaked or absent")
			}
			if role != "" {
				hook := hooks[2].(map[string]any)
				if hook["matcher"] != "startup|resume|clear|compact" {
					t.Fatal(hook)
				}
				// Compaction does not create an egg or regenerate settings: the
				// installed provider executes this same hook again (vendor tier).
				if !strings.Contains(hook["hooks"].([]any)[0].(map[string]any)["command"].(string), "additionalContext") {
					t.Fatal(hook)
				}
			}
		})
	}
}

func TestRoleAmbientDisabledHooksRefuseOnlyRoleSessions(t *testing.T) {
	for _, source := range []string{"user", "project", "local"} {
		t.Run(source, func(t *testing.T) {
			home, work, state := testsocket.Dir(t), testsocket.Dir(t), testsocket.Dir(t)
			path := filepath.Join(work, ".claude", "settings.json")
			if source == "user" {
				path = filepath.Join(home, ".claude", "settings.json")
			}
			if source == "local" {
				path = filepath.Join(work, ".claude", "settings.local.json")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"disableAllHooks":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"", "coordinator"} {
				dir := filepath.Join(state, "eggs", "ours")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := sessionrole.Write(dir, sessionrole.Metadata{Role: role, Pinned: role != ""}); err != nil {
					t.Fatal(err)
				}
				_, err := prepareClaudeLifecycleArgs(nil, home, dir, "provider", work)
				if (err != nil) != (role != "") {
					t.Fatalf("role=%q err=%v", role, err)
				}
				if role != "" {
					for _, args := range [][]string{{"--setting-sources="}, {"--restricted"}} {
						if _, err := prepareClaudeLifecycleArgs(args, home, dir, "provider", work); err != nil {
							t.Fatalf("excluded ambient source blocked role: %v", err)
						}
					}
				}
			}
		})
	}
}
