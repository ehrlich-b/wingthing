package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The experiment enters through invocation settings; existing observer hooks
// remain present and never become decision-producing hooks.
func TestPermissionSpikePreservesLifecycleObservers(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, "spike-settings.json")
	data := `{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"fixture hook","timeout":5}]}],"Stop":[{"hooks":[{"type":"command","command":"fixture stop"}]}]}}`
	if err := os.WriteFile(settings, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := ClaudeLifecycleArgs([]string{"--settings", settings, "--model", "claude-haiku-5-5"}, home, "egg-fixture", "provider-fixture")
	if err != nil {
		t.Fatal(err)
	}
	var merged struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(args[len(args)-1]), &merged); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"} {
		entries := merged.Hooks[event]
		if len(entries) == 0 {
			t.Fatalf("missing observer for %s", event)
		}
		observer := entries[len(entries)-1].Hooks[0]
		if observer.Timeout != 5 || !strings.Contains(observer.Command, lifecycleHookDir(home, "egg-fixture")) {
			t.Fatalf("observer changed for %s", event)
		}
	}
	if len(merged.Hooks["PermissionRequest"]) != 2 || merged.Hooks["PermissionRequest"][0].Hooks[0].Command != "fixture hook" {
		t.Fatal("experiment replaced PermissionRequest observer")
	}
	if len(merged.Hooks["Stop"]) != 2 || merged.Hooks["Stop"][0].Hooks[0].Command != "fixture stop" {
		t.Fatal("existing Stop hook lost")
	}
}
