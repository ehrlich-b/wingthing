package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareIsolatedClaudeConfigLoadsStableWritableProfile(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".claude.json")
	legacyData := []byte(`{"hasCompletedOnboarding":true,"wtFixture":"persisted"}`)
	if err := os.WriteFile(legacy, legacyData, 0600); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{}
	if err := prepareIsolatedClaudeConfig(home, envMap); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(home, ".claude")
	if got := envMap["CLAUDE_CONFIG_DIR"]; got != claudeDir {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got, claudeDir)
	}
	profile := filepath.Join(claudeDir, ".claude.json")
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(legacyData) {
		t.Fatalf("migrated profile = %q, want %q", data, legacyData)
	}
	if info, err := os.Stat(profile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("profile mode = %v, %v", infoMode(info), err)
	}
}

func TestPrepareIsolatedClaudeConfigNeverOverwritesCurrentProfile(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"wtFixture":"legacy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(claudeDir, ".claude.json")
	if err := os.WriteFile(profile, []byte(`{"wtFixture":"current"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareIsolatedClaudeConfig(home, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"wtFixture":"current"}` {
		t.Fatalf("current profile was overwritten: %s", data)
	}
}

func TestPrepareIsolatedClaudeConfigDoesNotFollowLegacySymlink(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"secret":"outside"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := prepareIsolatedClaudeConfig(home, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy symlink was migrated: %v", err)
	}
}

func TestUserHashSeparatesDistinctAuthenticatedIdentities(t *testing.T) {
	first := userHash("user-id-for-work-account")
	second := userHash("user-id-for-personal-account")
	if first == second {
		t.Fatal("distinct authenticated identities shared an agent-home hash")
	}
	if first != userHash("user-id-for-work-account") {
		t.Fatal("stable identity did not retain a stable agent-home hash")
	}
	if got := userHash("u-alice"); got != "e3fb03053ead" {
		t.Fatalf("userHash(u-alice) = %q; browser identity fixture expects e3fb03053ead", got)
	}
}
