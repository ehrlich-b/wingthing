package eggclient

import (
	"bytes"
	"encoding/json"

	"os"
	"path/filepath"

	"testing"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestMemberSessionArtifactsRequireCurrentOwnerAndPathAccess(t *testing.T) {
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	sessionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.owner"), []byte("alice\nalice@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "egg.meta"), []byte("agent=claude\ncwd="+workspace+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	member := ws.TunnelRequest{SenderUserID: "alice", SenderEmail: "alice@example.com", SenderOrgRole: "member"}
	if !CanAccessSessionArtifact(member, sessionDir, []string{workspace}) {
		t.Fatal("member could not read their audit inside their current workspace")
	}
	if CanAccessSessionArtifact(member, sessionDir, []string{otherWorkspace}) {
		t.Fatal("member retained audit access after the session workspace was revoked")
	}
	otherMember := member
	otherMember.SenderUserID = "mallory"
	if CanAccessSessionArtifact(otherMember, sessionDir, []string{workspace}) {
		t.Fatal("member could read another user's session audit")
	}
	admin := ws.TunnelRequest{SenderUserID: "admin", SenderOrgRole: "admin"}
	if !CanAccessSessionArtifact(admin, sessionDir, nil) {
		t.Fatal("admin lost historical session oversight access")
	}

	missingMetadata := t.TempDir()
	if CanAccessSessionArtifact(member, missingMetadata, []string{workspace}) {
		t.Fatal("member audit access did not fail closed without owner/path metadata")
	}
}

func requireSetupAPIKeyHelper(t *testing.T, agent string, envMap map[string]string, home string) {
	t.Helper()
	if err := SetupAPIKeyHelper(agent, envMap, home); err != nil {
		t.Fatalf("setupAPIKeyHelper: %v", err)
	}
}

func TestSetupAPIKeyHelper_RemovesKeyFromEnv(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test123", "OTHER": "keep"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY should be removed from envMap")
	}
	if envMap["OTHER"] != "keep" {
		t.Error("other env vars should be preserved")
	}
}

func TestSetupAPIKeyHelper_NullSettingsDoNotPanic(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireSetupAPIKeyHelper(t, "claude", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test123"}, home)
	data, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Contains(data, []byte("apiKeyHelper")) {
		t.Fatalf("settings after null input = %q, %v", data, err)
	}
}

func TestSetupAPIKeyHelper_WritesKeyFile(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	keyFile := filepath.Join(home, ".anthropic_key")
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if string(data) != "sk-ant-secret" {
		t.Errorf("key file = %q, want sk-ant-secret", string(data))
	}
	info, _ := os.Stat(keyFile)
	if info.Mode().Perm() != 0400 {
		t.Errorf("key file perm = %o, want 0400", info.Mode().Perm())
	}
}

func TestSetupAPIKeyHelper_SetsApiKeyHelperInSettings(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	want := "cat " + filepath.Join(home, ".anthropic_key")
	if got := settings["apiKeyHelper"]; got != want {
		t.Errorf("apiKeyHelper = %q, want %q", got, want)
	}
}

func TestSetupAPIKeyHelper_PreservesExistingSettings(t *testing.T) {
	home := t.TempDir()
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{"theme": "dark", "permissions": map[string]any{"allow": true}}
	data, err := json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	raw, err := os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Errorf("existing theme setting clobbered, got %v", settings["theme"])
	}
	if settings["apiKeyHelper"] == nil {
		t.Error("apiKeyHelper not set")
	}
}

func TestSetupAPIKeyHelper_StablePath_NoSessionRace(t *testing.T) {
	// Two "sessions" calling setupAPIKeyHelper should write to the same file,
	// not per-session paths. This is the v0.128.0 bug fix.
	home := t.TempDir()
	env1 := map[string]string{"ANTHROPIC_API_KEY": "key-session-1"}
	env2 := map[string]string{"ANTHROPIC_API_KEY": "key-session-2"}
	requireSetupAPIKeyHelper(t, "claude", env1, home)
	requireSetupAPIKeyHelper(t, "claude", env2, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	helper := settings["apiKeyHelper"].(string)
	// Both sessions should point to the same stable path (no session ID in path)
	wantPath := filepath.Join(home, ".anthropic_key")
	if helper != "cat "+wantPath {
		t.Errorf("apiKeyHelper = %q, want stable path %q", helper, "cat "+wantPath)
	}
	// The key file should contain the last writer's key (both are valid,
	// the point is the PATH is stable, not per-session)
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "key-session-2" {
		t.Errorf("key file = %q, want key-session-2", string(data))
	}
}

func TestSetupAPIKeyHelper_OverwritesOldReadOnlyKeyFile(t *testing.T) {
	home := t.TempDir()
	keyFile := filepath.Join(home, ".anthropic_key")
	if err := os.WriteFile(keyFile, []byte("old-key"), 0400); err != nil {
		t.Fatal(err)
	}
	envMap := map[string]string{"ANTHROPIC_API_KEY": "new-key"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	data, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("key file gone after overwrite: %v", err)
	}
	if string(data) != "new-key" {
		t.Errorf("key file = %q, want new-key", string(data))
	}
}

func TestSetupAPIKeyHelper_NonClaudeAgent_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"}
	requireSetupAPIKeyHelper(t, "codex", envMap, home)
	if _, ok := envMap["ANTHROPIC_API_KEY"]; !ok {
		t.Error("non-claude agent should not remove ANTHROPIC_API_KEY")
	}
	keyFile := filepath.Join(home, ".anthropic_key")
	if _, err := os.Stat(keyFile); err == nil {
		t.Error("key file should not be created for non-claude agent")
	}
}

func TestSetupAPIKeyHelper_NoKey_Noop(t *testing.T) {
	home := t.TempDir()
	envMap := map[string]string{"OTHER": "val"}
	requireSetupAPIKeyHelper(t, "claude", envMap, home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		t.Error("settings should not be created when no API key present")
	}
}
