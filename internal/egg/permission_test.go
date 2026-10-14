package egg

import (
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/permissions"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionSettingsPreserveObserversAndOtherHooks(t *testing.T) {
	home, err := os.MkdirTemp("", "wp-home-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	dir := filepath.Join(home, "egg")
	os.MkdirAll(dir, 0700)
	args, err := prepareClaudeLifecycleArgs([]string{"--settings", `{"permissions":{"deny":["Bash(rm:*)"],"ask":["Write"]},"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"true"}]}],"Stop":[{"hooks":[{"type":"command","command":"true"}]}]}}`}, home, dir, "provider", home)
	if err != nil {
		t.Fatal(err)
	}
	path := args[len(args)-1]
	s := &Server{dir: dir}
	env := map[string]string{}
	cleanup, err := s.preparePermissions(t.Context(), home, path, "provider", env, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var settings struct {
		Permissions struct{ Ask, Deny []string }
		Hooks       map[string][]struct {
			Hooks []struct {
				Command string
				Args    []string
				Timeout int
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &settings) != nil {
		t.Fatal(err)
	}
	if len(settings.Permissions.Ask) != 2 || settings.Permissions.Ask[1] != "Bash" || len(settings.Permissions.Deny) != 1 {
		t.Fatal("permission rules lost")
	}
	entries := settings.Hooks["PermissionRequest"]
	if len(entries) != 3 || entries[0].Hooks[0].Command != "true" || entries[1].Hooks[0].Timeout != 5 || entries[2].Hooks[0].Timeout != permissions.HookTimeout {
		t.Fatal("hooks replaced")
	}
	if len(entries[2].Hooks[0].Args) == 0 || entries[2].Hooks[0].Args[0] != "_permission_hook" || env["WT_PERMISSION_TOKEN"] != "" || strings.Contains(string(data), s.permissions.Token) {
		t.Fatal("hook credential leaked or exec-form channel missing")
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "StopFailure", "SessionEnd"} {
		if len(settings.Hooks[event]) == 0 {
			t.Fatal("observer missing " + event)
		}
	}
}
func TestPermissionDisabledHooksDoNotInstallBroker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"disableAllHooks":true}`), 0600)
	s := &Server{dir: dir}
	env := map[string]string{}
	cleanup, err := s.preparePermissions(context.Background(), dir, path, "provider", env, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if s.permissions != nil || env["WT_PERMISSION_TOKEN"] != "" {
		t.Fatal("disabled hooks ignored")
	}
}
