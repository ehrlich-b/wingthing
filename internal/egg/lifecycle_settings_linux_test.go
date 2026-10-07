//go:build linux

package egg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestClaudeLifecycleJailSettingsRejectDirectorySymlinks(t *testing.T) {
	for _, attack := range []string{"unmounted", "nested-unmounted", "mounted"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			home, work, victim := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "victim", ".claude")
			for _, path := range []string{home, work, victim} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
			if attack == "mounted" {
				victim = filepath.Join(work, "private")
				if err := os.MkdirAll(victim, 0700); err != nil {
					t.Fatal(err)
				}
			}
			lifecycleWrite(t, filepath.Join(victim, ".credentials.json"), `{"env":{"STOLEN":"victim-provider-secret"}}`)
			parent := work
			if attack == "nested-unmounted" {
				parent = filepath.Join(work, "sub")
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(parent, "link")
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
			// Control isolation splits HOME into mounts for existing directories.
			allowed := []string{filepath.Join(work, "settings.json"), filepath.Join(home, ".claude", "settings.json")}
			for _, path := range allowed {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				lifecycleWrite(t, path, `{"model":"sonnet"}`)
			}
			policy, err := IsolateControl(sandbox.Config{Deny: []string{"/"}, Mounts: []sandbox.Mount{{Source: work}, {Source: home}}}, "", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(link, ".credentials.json")
			args, err := claudeLifecycleArgs([]string{"--settings", settings}, home, "ours", "ours", work, &policy)
			if err == nil || !strings.Contains(err.Error(), "settings file is outside the session filesystem policy") {
				t.Fatalf("directory symlink imported provider credentials into the settings bridge: args=%v err=%v", args, err)
			}
			for _, path := range allowed {
				args, err := claudeLifecycleArgs([]string{"--settings", path}, home, "ours", "ours", work, &policy)
				if err != nil {
					t.Fatalf("mounted workspace/provider settings %s refused: %v", path, err)
				}
				var settings map[string]any
				if err := json.Unmarshal([]byte(args[providerOptionsEnd(args)-1]), &settings); err != nil || settings["model"] != "sonnet" || settings["env"] != nil {
					t.Fatalf("mounted settings not preserved: %v %v", settings, err)
				}
			}
		})
	}
}

func TestClaudeLifecycleJailSettingsUseMostSpecificMount(t *testing.T) {
	root := t.TempDir()
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	path := filepath.Join(work, "sub", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, path, `{"model":"sonnet"}`)
	policy := sandbox.Config{Deny: []string{"/"}, Mounts: []sandbox.Mount{{Source: work}, {Source: filepath.Join(root, "replacement"), Target: filepath.Dir(path)}}}
	if _, err := claudeLifecycleArgs([]string{"--settings", path}, home, "ours", "ours", work, &policy); err == nil {
		t.Fatal("parent mount exposed a file hidden by the more specific mount")
	}
	// Exact file mounts remain usable without mounting their parent directory.
	policy.Mounts = []sandbox.Mount{{Source: path, ReadOnly: true}}
	if _, err := claudeLifecycleArgs([]string{"--settings", path}, home, "ours", "ours", work, &policy); err != nil {
		t.Fatalf("explicit file mount refused: %v", err)
	}
}
