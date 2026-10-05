//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

// A temporary state bound to a temporary provider data home outside it. No
// real provider home, credential, login, or model is read or invoked.
func previewProviderBindingFixture(t *testing.T, script string) (state, provider, observed string) {
	t.Helper()
	old := config.ReleaseChannel
	config.ReleaseChannel = "preview"
	t.Cleanup(func() { config.ReleaseChannel = old })
	root := config.CanonicalProviderPath(t.TempDir())
	state = filepath.Join(root, "control state")
	provider = filepath.Join(root, "bound provider-home")
	observed = filepath.Join(root, "observed")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{state, provider, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, ".release-channel"), []byte("preview\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, config.ProviderHomeBinding), []byte(provider+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "host-home"))
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return state, provider, observed
}

func TestPreviewProviderBindingProfileStatusGuideAndLoginUseBoundHome(t *testing.T) {
	state, provider, observed := previewProviderBindingFixture(t, "exit 99\n")
	t.Setenv("USER", "fixture-ambient-account")
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	configDir := config.CanonicalProviderPath(filepath.Join(provider, ".claude"))
	if profile.StateDir != state || profile.Home != provider || profile.HomeBinding != filepath.Join(state, config.ProviderHomeBinding) || profile.ConfigDirectory != configDir {
		t.Fatalf("profile did not select the bound data home: %#v", profile)
	}
	if runtime.GOOS == "darwin" {
		osHome, wantConfig, err := egg.PreviewClaudeOSContext(provider)
		if err != nil || profile.OSHome != osHome || wantConfig != configDir {
			t.Fatalf("bound Mac OS context changed: %#v %v", profile, err)
		}
	}
	env := "\n" + strings.Join(previewClaudeStatusEnv(profile), "\n") + "\n"
	if !strings.Contains(env, "\nHOME="+profile.OSHome+"\n") || !strings.Contains(env, "\nCLAUDE_CONFIG_DIR="+configDir+"\n") {
		t.Fatal("status environment did not bind the exact bound namespace")
	}
	if runtime.GOOS == "darwin" && strings.Contains(env, "\nUSER=") {
		t.Fatal("Mac status inherited a caller credential account selector")
	}

	report := previewProviderJSON(t, profile, true, "claude.ai")
	writePreviewProviderScript(t, profile, "pwd -P > "+remotepkg.ShellQuote(observed)+"\nprintf '%s' "+remotepkg.ShellQuote(report)+"\n")
	status := inspectPreviewClaude(context.Background(), profile, 5*time.Second)
	if status.State != "reported_authenticated" {
		t.Fatalf("fake vendor status was not accepted for the bound namespace: %#v", status)
	}
	if cwd, err := os.ReadFile(observed); err != nil || strings.TrimSpace(string(cwd)) != provider {
		t.Fatalf("status did not run from the bound data home: %q %v", cwd, err)
	}

	guide, err := previewClaudeSetupGuide(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(guide.PrepareCommands) != 1 || strings.Contains(strings.Join(guide.PrepareCommands, "\n"), "mkdir") {
		t.Fatalf("guide suggested changing the bound data home: %q", guide.PrepareCommands)
	}
	data, err := json.Marshal(guide)
	if err != nil || !strings.Contains(string(data), `"data_home_binding":`) {
		t.Fatal("guide JSON omitted the binding source")
	}

	streams := previewLoginFixtureStreams(t)
	streams.platform = runtime.GOOS
	ran := false
	streams.run = func(command *exec.Cmd) error {
		ran = true
		if !reflect.DeepEqual(command.Args, []string{profile.Executable, "auth", "login", "--claudeai"}) || command.Dir != provider {
			t.Fatal("bound login changed argv or cwd")
		}
		if got := "\n" + strings.Join(command.Env, "\n") + "\n"; got != env {
			t.Fatal("bound login environment differs from status")
		}
		return nil
	}
	// The default state/provider-home is deliberately absent when bound.
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err != nil || !ran {
		t.Fatalf("bound login did not reach the fake vendor: %v", err)
	}
	if entries, _ := os.ReadDir(provider); len(entries) != 0 {
		t.Fatal("Wingthing wrote into the bound provider home")
	}
}

func TestPreviewProviderBindingStatusRechecksCredentialPaths(t *testing.T) {
	state, provider, observed := previewProviderBindingFixture(t, "exit 99\n")
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	writePreviewProviderScript(t, profile, "printf ran > "+remotepkg.ShellQuote(observed)+"\nexit 99\n")
	// The binding is still valid, but a credential namespace was redirected
	// after resolution into protected controller state.
	if err := os.Symlink(state, filepath.Join(provider, ".claude")); err != nil {
		t.Fatal(err)
	}
	status := inspectPreviewClaude(context.Background(), profile, time.Second)
	if status.State != "unknown" || status.LoggedIn != nil {
		t.Fatalf("unsafe credential paths produced an auth result: %#v", status)
	}
	if _, err := os.Stat(observed); !os.IsNotExist(err) {
		t.Fatalf("vendor CLI ran after credential paths changed: %v", err)
	}
}

func TestPreviewProviderBindingStatusRejectsChangedMacConfigDirectory(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS stores the resolved Claude configuration directory")
	}
	_, provider, observed := previewProviderBindingFixture(t, "exit 99\n")
	link := filepath.Join(provider, ".claude")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	writePreviewProviderScript(t, profile, "printf ran > "+remotepkg.ShellQuote(observed)+"\nexit 99\n")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	inspectPreviewClaude(context.Background(), profile, time.Second)
	if _, err := os.Stat(observed); !os.IsNotExist(err) {
		t.Fatalf("vendor CLI ran with a stale resolved configuration directory: %v", err)
	}
}

func TestPreviewProviderBindingInvalidStopsBeforeVendor(t *testing.T) {
	state, provider, observed := previewProviderBindingFixture(t, "")
	binding := filepath.Join(state, config.ProviderHomeBinding)
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	writePreviewProviderScript(t, profile, "touch "+remotepkg.ShellQuote(observed)+"\n")
	streams := previewLoginFixtureStreams(t)
	streams.platform = runtime.GOOS
	streams.run = func(*exec.Cmd) error {
		t.Fatal("vendor login ran with an invalid or changed binding")
		return nil
	}

	other := filepath.Join(filepath.Dir(provider), "other provider-home")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binding, []byte(other+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err == nil {
		t.Fatal("login followed a binding retargeted after profile resolution")
	}

	if err := os.Chmod(binding, 0644); err != nil {
		t.Fatal(err)
	}
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err == nil {
		t.Fatal("login accepted an invalid present binding")
	}
	if _, err := resolvePreviewProviderProfile(); err == nil {
		t.Fatal("status/setup-guide profile accepted an invalid present binding")
	}
	if _, err := config.Load(); err == nil {
		t.Fatal("config loader accepted an invalid present binding")
	}
	if _, err := os.Stat(observed); !os.IsNotExist(err) {
		t.Fatal("fake vendor ran")
	}
}

func TestPreviewProviderBindingSessionAndLifecycleHome(t *testing.T) {
	_, provider, _ := previewProviderBindingFixture(t, "exit 99\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if home := effectiveSessionHome(cfg, EggIdentity{}); home != provider {
		t.Fatalf("native session home ignored the binding: %q", home)
	}
	for _, recorded := range []string{"", provider} {
		if home, err := lifecycleProviderHome(cfg, recorded); err != nil || home != provider {
			t.Fatalf("lifecycle home ignored the binding: %q %v", home, err)
		}
	}
	if _, err := lifecycleProviderHome(cfg, config.PreviewProviderHome(cfg.Dir)); err == nil {
		t.Fatal("lifecycle accepted the unbound default provider home")
	}
}
