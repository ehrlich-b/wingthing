package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestPreviewChannelGuardPrecedesStateAndPreservesAgentArguments(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	if _, err := channelInvocationArgs([]string{"--expected-channel", "stable", "stop"}); err == nil {
		t.Fatal("cross-channel control accepted")
	}
	args, err := channelInvocationArgs([]string{"--expected-channel=preview", "egg", "codex", "--", "--expected-channel", "stable"})
	if err != nil || strings.Join(args, " ") != "egg codex -- --expected-channel stable" {
		t.Fatalf("provider argv changed: %v %v", args, err)
	}
	args, err = channelInvocationArgs([]string{"tool-call", "native", "--expected-channel", "stable"})
	if err != nil || strings.Join(args, " ") != "tool-call native --expected-channel stable" {
		t.Fatal("native tool argv changed")
	}
	if eggPidMatchesSession(os.Getpid(), "fixture") {
		t.Fatal("preview accepted unrelated live process as orphan egg")
	}
}

func TestPreviewUpdateRefusesStableNamesSymlinksAndHardlinks(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	dir := t.TempDir()
	stable := filepath.Join(dir, "wt")
	if err := os.WriteFile(stable, []byte("stable"), 0755); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(dir, "wt-preview")
	if err := os.Symlink(stable, preview); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err == nil {
		t.Fatal("stable alias replacement accepted")
	}
	if err := os.Remove(preview); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(stable, preview); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err == nil {
		t.Fatal("stable hardlink replacement accepted")
	}
	if err := os.Remove(preview); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preview, []byte("preview"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateChannelUpdatePath(preview); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewAgentHomeAndCredentialsDoNotReuseHost(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	state := t.TempDir()
	cfg := &config.Config{Dir: state}
	home := effectiveSessionHome(cfg, EggIdentity{})
	if home != config.PreviewProviderHome(state) {
		t.Fatal(home)
	}
	t.Setenv("ANTHROPIC_API_KEY", "private-fixture-never-copy")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "private-fixture-never-copy")
	t.Setenv("CODEX_HOME", "/host/provider/config")
	if err := setupAPIKeyHelper("claude", map[string]string{}, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("ambient host credential copied")
	}
	env := strings.Join(directAgentEnvWithPolicy("codex", home, 0, false), "\n")
	if strings.Contains(env, "private-fixture") || strings.Contains(env, "/host/provider/config") {
		t.Fatal("ambient auth inherited")
	}
	if !strings.Contains("\n"+env+"\n", "\nCODEX_HOME="+filepath.Join(home, ".codex")+"\n") {
		t.Fatal("preview Codex namespace absent")
	}
	if !strings.Contains(env, "HOME="+home) {
		t.Fatal("isolated provider home absent")
	}
}

func TestPreviewDirectProviderNamespacesAndStableEnvironment(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	foreign := "/host/foreign-provider-namespace"
	for _, key := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		t.Setenv(key, foreign)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY"} {
		t.Setenv(key, "fixture-secret-never-inherit")
	}
	home := filepath.Join(t.TempDir(), "provider-home")
	toMap := func(env []string) map[string]string {
		values := map[string]string{}
		for _, entry := range env {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				values[key] = value
			}
		}
		return values
	}
	config.ReleaseChannel = "preview"
	for _, agentName := range []string{"claude", "codex", "opencode"} {
		t.Run("preview-"+agentName, func(t *testing.T) {
			env := directAgentEnvWithPolicy(agentName, home, 0, false)
			values := toMap(env)
			if values["HOME"] != home {
				t.Fatal("preview did not select its provider home")
			}
			for _, entry := range env {
				if strings.Contains(entry, foreign) || strings.Contains(entry, "fixture-secret") {
					t.Fatal("preview inherited a foreign provider namespace or credential")
				}
			}
			wantClaude, wantCodex := "", ""
			if agentName == "claude" {
				wantClaude = filepath.Join(home, ".claude")
			} else if agentName == "codex" {
				wantCodex = filepath.Join(home, ".codex")
			}
			if values["CLAUDE_CONFIG_DIR"] != wantClaude || values["CODEX_HOME"] != wantCodex {
				t.Fatal("preview provider namespace is absent or belongs to another provider")
			}
		})
	}
	// The interactive Claude path already uses the documented Keychain namespace.
	interactive := map[string]string{"CLAUDE_CONFIG_DIR": foreign}
	if err := prepareIsolatedClaudeConfig(home, interactive); err != nil {
		t.Fatal(err)
	}
	if interactive["CLAUDE_CONFIG_DIR"] != filepath.Join(home, ".claude") {
		t.Fatal("interactive Claude provider namespace changed")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("pure namespace selection created provider state")
	}
	config.ReleaseChannel = "stable"
	for _, agentName := range []string{"claude", "codex", "opencode"} {
		t.Run("stable-"+agentName, func(t *testing.T) {
			values := toMap(directAgentEnvWithPolicy(agentName, home, 0, true))
			if values["HOME"] != home || values["CLAUDE_CONFIG_DIR"] != foreign || values["CODEX_HOME"] != foreign {
				t.Fatal("stable custom home or provider namespace changed")
			}
			values = toMap(directAgentEnvWithPolicy(agentName, "", 0, true))
			if values["HOME"] != foreign {
				t.Fatal("stable ambient HOME changed")
			}
			values = toMap(directAgentEnvWithPolicy(agentName, home, 0, false))
			if values["CLAUDE_CONFIG_DIR"] != "" || values["CODEX_HOME"] != "" {
				t.Fatal("stable filtered environment acquired a preview namespace")
			}
		})
	}
}

func TestPreviewReleaseSelectionNeverFallsBackToStable(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	if strings.Contains(releaseMetadataURL(), "/latest") || !strings.HasPrefix(releaseAssetName(), "wt-preview-") {
		t.Fatal("stable update namespace reused")
	}
	fixture := `[{"tag_name":"v0.149.0"},{"tag_name":"v0.149.0-preview.1","draft":true,"prerelease":true},{"tag_name":"v0.148.0-preview.2","prerelease":true}]`
	release, err := decodeChannelRelease(strings.NewReader(fixture))
	if err != nil || release.TagName != "v0.148.0-preview.2" {
		t.Fatalf("release selection: %v %v", release, err)
	}
	if _, err := decodeChannelRelease(strings.NewReader(`[{"tag_name":"v0.149.0"}]`)); err == nil {
		t.Fatal("fell back to stable release")
	}
}
