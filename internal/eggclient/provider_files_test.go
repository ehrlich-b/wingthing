package eggclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeySetupRefusesProviderPathRedirection(t *testing.T) {
	for _, attack := range []string{"settings-leaf", "settings-hardlink", "provider-directory", "home", "key-leaf"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "user-home")
			outside := filepath.Join(root, "other-user")
			for _, path := range []string{filepath.Join(home, ".claude"), filepath.Join(outside, ".claude")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			secret := filepath.Join(outside, ".claude", "settings.json")
			original := `{"env":{"SECRET":"other-user-only"}}`
			if err := os.WriteFile(secret, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			switch attack {
			case "settings-leaf":
				if err := os.Symlink(secret, filepath.Join(home, ".claude", "settings.json")); err != nil {
					t.Fatal(err)
				}
			case "settings-hardlink":
				if err := os.Link(secret, filepath.Join(home, ".claude", "settings.json")); err != nil {
					t.Fatal(err)
				}
			case "provider-directory":
				if err := os.Remove(filepath.Join(home, ".claude")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, ".claude"), filepath.Join(home, ".claude")); err != nil {
					t.Fatal(err)
				}
			case "home":
				if err := os.RemoveAll(home); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, home); err != nil {
					t.Fatal(err)
				}
			case "key-leaf":
				if err := os.Symlink(secret, filepath.Join(home, ".anthropic_key")); err != nil {
					t.Fatal(err)
				}
			}
			err := SetupAPIKeyHelper("claude", map[string]string{"ANTHROPIC_API_KEY": "fixture-key"}, home)
			if err == nil {
				t.Fatal("provider setup read or wrote through member-controlled path")
			}
			data, readErr := os.ReadFile(secret)
			if readErr != nil || string(data) != original {
				t.Fatalf("other user's settings changed: %q %v", data, readErr)
			}
			if attack == "settings-leaf" || attack == "settings-hardlink" {
				if data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); strings.Contains(string(data), "apiKeyHelper") {
					t.Fatal("republished other-user JSON")
				}
			}
		})
	}
}

func TestProviderMigrationRefusesLinkedLegacyConfig(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "profile.json")
	if err := os.WriteFile(secret, []byte(`{"foreignSecret":"other-user-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareIsolatedClaudeConfig(home, map[string]string{}); err == nil {
		t.Fatal("legacy migration republished another user's hardlinked profile")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("foreign profile published: %v", err)
	}
}
