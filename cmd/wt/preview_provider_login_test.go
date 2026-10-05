package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func previewLoginFixtureStreams(t *testing.T) previewProviderLoginIO {
	t.Helper()
	files := make([]*os.File, 0, 3)
	for i := 0; i < 3; i++ {
		file, err := os.CreateTemp(t.TempDir(), "fake-terminal-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		files = append(files, file)
	}
	return previewProviderLoginIO{in: files[0], out: files[1], errOut: files[2], platform: "linux",
		terminal: func(*os.File, *os.File, *os.File) error { return nil }, managedContext: func() error { return nil },
		run: func(cmd *exec.Cmd) error { return cmd.Run() }}
}

func TestPreviewProviderLoginDirectFakeVendorHasExactStreamsAndNoRecorder(t *testing.T) {
	profile := previewProviderFixture(t, "printf 'fake-public-login-output'\nprintf 'fake-public-login-error' >&2\n")
	t.Setenv("USER", "fixture-ambient-account")
	for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "HTTP_PROXY", "WT_SESSION_DIR", "BROWSER"} {
		t.Setenv(key, "fixture-foreign-secret-or-shim")
	}
	streams := previewLoginFixtureStreams(t)
	streams.platform = runtime.GOOS
	streams.run = func(command *exec.Cmd) error {
		if !reflect.DeepEqual(command.Args, []string{profile.Executable, "auth", "login", "--claudeai"}) {
			t.Fatal("vendor argv widened beyond the supported subscription login")
		}
		if command.Stdin != streams.in || command.Stdout != streams.out || command.Stderr != streams.errOut || command.Dir != profile.Home || command.SysProcAttr != nil {
			t.Fatal("login introduced a stream adapter, recorder, different cwd, or process policy")
		}
		env := "\n" + strings.Join(command.Env, "\n") + "\n"
		if strings.Contains(env, "fixture-foreign") || !strings.Contains(env, "\nHOME="+profile.OSHome+"\n") || !strings.Contains(env, "\nCLAUDE_CONFIG_DIR="+profile.ConfigDirectory+"\n") {
			t.Fatal("login environment did not bind the exact fresh namespace")
		}
		if runtime.GOOS == "darwin" && strings.Contains(env, "\nUSER=") {
			t.Fatal("Mac login inherited a caller credential account selector")
		}
		if runtime.GOOS != "darwin" && !strings.Contains(env, "\nUSER=fixture-ambient-account\n") {
			t.Fatal("non-Mac login changed the caller's USER environment")
		}
		return command.Run()
	}
	before, _ := os.ReadDir(profile.StateDir)
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err != nil {
		t.Fatal(err)
	}
	stdout, _ := os.ReadFile(streams.out.Name())
	stderr, _ := os.ReadFile(streams.errOut.Name())
	if string(stdout) != "fake-public-login-output" || string(stderr) != "fake-public-login-error" {
		t.Fatal("fake vendor did not write directly to the supplied OS streams")
	}
	after, _ := os.ReadDir(profile.StateDir)
	if len(after) != len(before) {
		t.Fatal("login created egg, spool, or browser request state")
	}
	for _, name := range []string{"eggs", "sessions", "browser-requests", "lifecycle.jsonl", "transcript", "hooks"} {
		if _, err := os.Stat(filepath.Join(profile.StateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected managed login artifact %s", name)
		}
	}
}

func TestPreviewProviderLoginRefusesUnpreparedCapturedManagedContexts(t *testing.T) {
	profile := previewProviderFixture(t, "exit 99\n")
	for _, tt := range []struct {
		name   string
		change func(*previewProviderLoginIO)
	}{
		{"captured-stream", func(s *previewProviderLoginIO) { s.terminal = previewProviderLoginTerminal }},
		{"managed-context", func(s *previewProviderLoginIO) {
			s.managedContext = func() error { return errors.New("managed fixture") }
		}},
		{"unsupported-platform", func(s *previewProviderLoginIO) { s.platform = "unsupported" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			streams := previewLoginFixtureStreams(t)
			ran := false
			streams.run = func(*exec.Cmd) error { ran = true; return nil }
			tt.change(&streams)
			err := runPreviewProviderLogin(context.Background(), profile, streams)
			if err == nil || ran {
				t.Fatal("refused login reached vendor process")
			}
		})
	}
	if err := os.Remove(filepath.Join(profile.StateDir, ".release-channel")); err != nil {
		t.Fatal(err)
	}
	streams := previewLoginFixtureStreams(t)
	streams.run = func(*exec.Cmd) error { t.Fatal("unmarked state reached vendor login"); return nil }
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err == nil {
		t.Fatal("unprepared state accepted")
	}
}

func TestPreviewProviderLoginDetectsKnownManagedContextWithoutReadingCredentials(t *testing.T) {
	for _, key := range []string{"WT_SESSION_ID", "WT_SESSION_DIR", "WT_TOOL_SOCKET", "WT_MCP_CLIENT", "CLAUDECODE", "CODEX_THREAD_ID", "CODEX_SANDBOX"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "fixture")
			if err := previewProviderLoginManagedContext(); err == nil {
				t.Fatal("known managed context accepted")
			}
		})
	}
	for _, name := range []string{"/bin/wt-preview", "/usr/local/bin/wt", "/Applications/Codex.app/Contents/MacOS/Codex", "/usr/bin/claude", "herdr"} {
		if !previewProviderLoginManagedProcess(name) {
			t.Fatalf("known process accepted: %s", name)
		}
	}
	for _, name := range []string{"/bin/zsh", "/usr/bin/login", "/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "sshd"} {
		if previewProviderLoginManagedProcess(name) {
			t.Fatalf("ordinary terminal process rejected: %s", name)
		}
	}
}

func TestPreviewProviderCanonicalConfigIdentityAcrossMacStatusAndLogin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("per-directory Keychain identity is a macOS provider contract")
	}
	profile := previewProviderFixture(t, "exit 99\n")
	target := filepath.Join(profile.Home, "account-config")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(profile.Home, ".claude")); err != nil {
		t.Fatal(err)
	}
	profile, err := resolvePreviewProviderProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ConfigDirectory != target || profile.OSHome == profile.Home {
		t.Fatal("Mac preview config identity kept an alias or relocated the OS home")
	}
	writePreviewProviderScript(t, profile, "printf '%s' "+shellQuote(previewProviderJSON(t, profile, true, "claude.ai"))+"\n")
	if status := inspectPreviewClaude(context.Background(), profile, time.Second); status.State != "reported_authenticated" || status.ConfigDirectory != target {
		t.Fatal("status did not accept the shared canonical configuration identity")
	}
	streams := previewLoginFixtureStreams(t)
	streams.platform = "darwin"
	streams.run = func(command *exec.Cmd) error {
		values := strings.Join(command.Env, "\n")
		if !strings.Contains(values, "CLAUDE_CONFIG_DIR="+target+"\n") || !strings.Contains(values, "HOME="+profile.OSHome+"\n") {
			t.Fatal("login and status selected different credential namespaces")
		}
		return nil
	}
	if err := runPreviewProviderLogin(context.Background(), profile, streams); err != nil {
		t.Fatal(err)
	}
	guide, err := previewClaudeSetupGuide(profile)
	if err != nil || guide.ConfigDirectory != target || guide.OSHome != profile.OSHome || guide.Home != profile.Home {
		t.Fatal("setup guide misreported the shared data/OS/config identity")
	}
}
