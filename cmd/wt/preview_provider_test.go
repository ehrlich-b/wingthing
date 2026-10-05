package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func previewProviderFixture(t *testing.T, script string) previewProviderProfile {
	t.Helper()
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "host-home"))
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "preview state"))
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	state := os.Getenv("WINGTHING_DIR")
	home := filepath.Join(state, "provider-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, ".release-channel"), []byte("preview\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func previewProviderJSON(t *testing.T, profile previewProviderProfile, loggedIn bool, method string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"loggedIn": loggedIn, "authMethod": method, "apiProvider": "firstParty",
		"configDirectory": profile.ConfigDirectory, "email": "personal@example.com", "orgId": "fixture-org",
		"orgName": "Personal fixture", "subscriptionType": "max", "accessToken": "secret-ignored-token", "apiKeySource": "secret-ignored-source"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writePreviewProviderScript(t *testing.T, profile previewProviderProfile, script string) {
	t.Helper()
	if err := os.WriteFile(profile.Executable, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewProviderStatusUsesExactNamespaceAndScrubbedEnvironment(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	t.Setenv("USER", "fixture-ambient-account")
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "ANTHROPIC_BASE_URL", "HTTP_PROXY", "WT_PROVIDER_BASE_URL", "XPC_SERVICE_NAME"} {
		t.Setenv(key, "foreign-credential-or-route")
	}
	script := `printf '%s\n' "$@" > ` + remotepkg.ShellQuote(filepath.Join(profile.Home, "argv.fixture")) + `
printf '%s\n' "$HOME" "$CLAUDE_CONFIG_DIR" "$PWD" > ` + remotepkg.ShellQuote(filepath.Join(profile.Home, "namespace.fixture")) + `
env > ` + remotepkg.ShellQuote(filepath.Join(profile.Home, "env.fixture")) + `
printf '%s' ` + remotepkg.ShellQuote(previewProviderJSON(t, profile, true, "claude.ai")) + `
printf '%s' 'secret-stderr-never-publish' >&2
`
	writePreviewProviderScript(t, profile, script)
	result := inspectPreviewClaude(context.Background(), profile, time.Second)
	if result.State != "reported_authenticated" || result.LoggedIn == nil || !*result.LoggedIn || result.Account == nil || result.Account.Email != "personal@example.com" {
		t.Fatalf("unexpected report: %+v", result)
	}
	argv, err := os.ReadFile(filepath.Join(profile.Home, "argv.fixture"))
	if err != nil || string(argv) != "auth\nstatus\n--json\n" {
		t.Fatalf("vendor invocation changed: %q %v", argv, err)
	}
	namespace, err := os.ReadFile(filepath.Join(profile.Home, "namespace.fixture"))
	if err != nil || string(namespace) != profile.OSHome+"\n"+profile.ConfigDirectory+"\n"+profile.Home+"\n" {
		t.Fatalf("profile or working directory changed: %q %v", namespace, err)
	}
	env, err := os.ReadFile(filepath.Join(profile.Home, "env.fixture"))
	if err != nil || bytes.Contains(env, []byte("foreign-credential-or-route")) {
		t.Fatal("foreign authentication or routing environment inherited")
	}
	userPresent := strings.Contains("\n"+string(env), "\nUSER=")
	if runtime.GOOS == "darwin" && userPresent {
		t.Fatal("Mac status inherited a different credential account selector")
	}
	if runtime.GOOS != "darwin" && !strings.Contains("\n"+string(env), "\nUSER=fixture-ambient-account\n") {
		t.Fatal("non-Mac status changed the caller's USER environment")
	}
	for _, key := range []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1"} {
		if !strings.Contains("\n"+string(env), "\n"+key+"\n") {
			t.Fatalf("missing vendor traffic setting %s", key)
		}
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte("secret-")) || bytes.Contains(data, []byte("apiKeySource")) || bytes.Contains(data, []byte("accessToken")) {
		t.Fatal("unrecognized metadata or raw stderr was published")
	}
}

func TestPreviewProviderStatusDoesNotConvertErrorsToNoLogin(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	valid := previewProviderJSON(t, profile, true, "claude.ai")
	loggedOut := previewProviderJSON(t, profile, false, "none")
	for _, tt := range []struct {
		name, output, suffix, state string
	}{
		{"reported-no-login", loggedOut, "exit 1", "reported_not_logged_in"},
		{"invalid-json", "secret-malformed-output", "exit 1", "unknown"},
		{"missing-namespace", `{"loggedIn":false,"authMethod":"none"}`, "exit 1", "unknown"},
		{"foreign-namespace", strings.Replace(valid, profile.ConfigDirectory, "/foreign/profile", 1), "exit 0", "unknown"},
		{"missing-state", strings.Replace(valid, `"loggedIn":true,`, "", 1), "exit 0", "unknown"},
		{"wrong-state-type", strings.Replace(valid, `"loggedIn":true`, `"loggedIn":"false"`, 1), "exit 0", "unknown"},
		{"vendor-error", loggedOut, "exit 2", "unknown"},
		{"logged-out-success", loggedOut, "exit 0", "unknown"},
		{"logged-in-failure", valid, "exit 1", "unknown"},
		{"unknown-method", strings.Replace(valid, "claude.ai", "future-auth", 1), "exit 0", "unknown"},
		{"trailing-output", valid + "\nsecret-trailing", "exit 0", "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writePreviewProviderScript(t, profile, "printf '%s' "+remotepkg.ShellQuote(tt.output)+"\n"+tt.suffix+"\n")
			result := inspectPreviewClaude(context.Background(), profile, time.Second)
			if result.State != tt.state {
				t.Fatalf("got %s, want %s", result.State, tt.state)
			}
			if result.State == "unknown" && (result.LoggedIn != nil || result.Account != nil) {
				t.Fatal("unverified report exposed account metadata")
			}
			data, _ := json.Marshal(result)
			if bytes.Contains(data, []byte("secret-")) || bytes.Contains(data, []byte("/foreign/profile")) {
				t.Fatal("raw vendor error or foreign account namespace published")
			}
		})
	}
}

func TestPreviewProviderStatusBoundsProcessAndBothOutputStreams(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			writePreviewProviderScript(t, profile, "while :; do printf '%s' 'secret-overflow-0123456789'"+redirect+"; done\n")
			start := time.Now()
			result := inspectPreviewClaude(context.Background(), profile, time.Second)
			if result.State != "unknown" || !strings.Contains(result.Diagnostic, "output limit") || time.Since(start) > 2*time.Second {
				t.Fatalf("output limit not enforced: %+v", result)
			}
		})
	}
	writePreviewProviderScript(t, profile, "exec /bin/sleep 5\n")
	start := time.Now()
	result := inspectPreviewClaude(context.Background(), profile, 50*time.Millisecond)
	if result.State != "unknown" || !strings.Contains(result.Diagnostic, "timed out") || time.Since(start) > time.Second {
		t.Fatalf("timeout not enforced: %+v", result)
	}
	writePreviewProviderScript(t, profile, "exit 99\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := inspectPreviewClaude(ctx, profile, time.Second); result.State != "unknown" || !strings.Contains(result.Diagnostic, "canceled") {
		t.Fatal("cancellation was reported as no login")
	}
}

func TestPreviewProviderGuideAndAbsentProfileNeverRunVendorOrWriteState(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	writePreviewProviderScript(t, profile, "printf 'ran' > "+remotepkg.ShellQuote(filepath.Join(profile.Home, "vendor-ran.fixture"))+"\nexit 99\n")
	t.Setenv("ANTHROPIC_API_KEY", "secret-ambient-never-publish")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/foreign-keychain-namespace")
	guide, err := previewClaudeSetupGuide(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile.Home, "vendor-ran.fixture")); !os.IsNotExist(err) {
		t.Fatal("setup guide invoked vendor")
	}
	if !strings.Contains(guide.LoginCommand, "'provider' 'login' 'claude'") || !strings.Contains(guide.LoginCommand, remotepkg.ShellQuote("WINGTHING_DIR="+profile.StateDir)) || !strings.Contains(guide.LoginCommand, "env -i ") {
		t.Fatal("manual command did not bind the first-class login route to exact state")
	}
	data, _ := json.Marshal(guide)
	if bytes.Contains(data, []byte("secret-ambient")) || bytes.Contains(data, []byte("foreign-keychain")) {
		t.Fatal("setup guide included ambient credentials or namespace")
	}
	if err := os.Remove(profile.Home); err != nil {
		t.Fatal(err)
	}
	result := inspectPreviewClaude(context.Background(), profile, time.Second)
	if result.State != "profile_not_initialized" {
		t.Fatal("absent profile did not receive actionable setup diagnostic")
	}
	if _, err := os.Stat(profile.Home); !os.IsNotExist(err) {
		t.Fatal("inspection created provider state")
	}
	if err := os.Remove(filepath.Join(profile.StateDir, ".release-channel")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(profile.StateDir); err != nil {
		t.Fatal(err)
	}
	profile, err = resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previewClaudeSetupGuide(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile.StateDir); !os.IsNotExist(err) {
		t.Fatal("read-only guide initialized Wingthing state")
	}
}

func TestPreviewProviderCLIRegistrationAndArgumentBounds(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	writePreviewProviderScript(t, profile, "printf '%s' "+remotepkg.ShellQuote(previewProviderJSON(t, profile, false, "none"))+"\nexit 1\n")
	for _, argv := range [][]string{{"provider", "status", "codex", "--json"}, {"provider", "status", "claude", "--timeout", "0s"}, {"provider", "status", "claude", "--timeout", "31s"}, {"provider", "setup-guide", "claude", "extra"}} {
		root := newRootCommand()
		root.SetArgs(argv)
		if err := root.Execute(); err == nil {
			t.Fatalf("invalid diagnostic invocation accepted: %v", argv)
		}
	}
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"provider", "status", "claude", "--json"})
	if err := root.Execute(); err != nil || !strings.Contains(out.String(), `"state":"reported_not_logged_in"`) {
		t.Fatalf("structured CLI status failed: %s %v", out.String(), err)
	}
	// Stable does not gain a new diagnostic or alter custom home/config behavior.
	config.ReleaseChannel = "stable"
	root = newRootCommand()
	root.SetArgs([]string{"provider", "status", "claude", "--json"})
	if err := root.Execute(); err == nil {
		t.Fatal("preview-only provider command registered in stable")
	}
}

func TestPreviewProviderMissingExecutableAndUnsafeMetadata(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	missing := profile
	missing.Executable = ""
	if result := inspectPreviewClaude(context.Background(), missing, time.Second); result.State != "not_installed" || result.LoggedIn != nil {
		t.Fatal("missing executable was treated as an authentication result")
	}
	valid := previewProviderJSON(t, profile, true, "claude.ai")
	valid = strings.Replace(valid, `"Personal fixture"`, `"untrusted\nterminal-control"`, 1)
	valid = strings.Replace(valid, `"max"`, shellJSONValue(t, strings.Repeat("x", 513)), 1)
	writePreviewProviderScript(t, profile, "printf '%s' "+remotepkg.ShellQuote(valid)+"\n")
	result := inspectPreviewClaude(context.Background(), profile, time.Second)
	if result.State != "reported_authenticated" || result.Account == nil || result.Account.OrgName != "" || result.Account.SubscriptionType != "" || result.Account.Email != "personal@example.com" {
		t.Fatal("metadata controls or oversized values reached the report")
	}
}

func shellJSONValue(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
