package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
)

func TestStableUpdateRejectsPreviewStateBeforeAnyWork(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	files := map[string]string{
		".release-channel": "preview\n",
		"wing.pid":         "123456789\n",
		"wing.args":        "[\"wing\",\"start\",\"--foreground\"]",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(state, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Cancellation keeps a regressed command from contacting the release feed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := updateCmd()
	command.SetContext(ctx)
	if err := command.RunE(command, nil); err == nil || !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("update did not reject the channel before other work: %v", err)
	}
	if lock, err := acquireUpdateLifecycleLock(); err == nil {
		_ = lock.Close()
		t.Fatal("update acquired a foreign channel's lifecycle lock")
	} else if !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("update lock did not reject the channel: %v", err)
	}
	if _, err := daemonStateForUpdate(); err == nil || !strings.Contains(err.Error(), "state belongs to") {
		t.Fatalf("daemon inspection did not reject the channel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "daemon.lock")); !os.IsNotExist(err) {
		t.Fatalf("update touched the foreign lifecycle lock: %v", err)
	}
	for name, want := range files {
		if data, err := os.ReadFile(filepath.Join(state, name)); err != nil || string(data) != want {
			t.Fatalf("update changed foreign metadata %s: %q %v", name, data, err)
		}
	}
}

func TestDecodeGitHubReleaseIsBounded(t *testing.T) {
	release, err := decodeGitHubRelease(strings.NewReader(`{"tag_name":"v1","assets":[]}`))
	if err != nil || release.TagName != "v1" {
		t.Fatalf("decode release = %#v, %v", release, err)
	}
	oversized := strings.NewReader(fmt.Sprintf(`{"tag_name":"v1","padding":"%s"}`, strings.Repeat("x", 1<<20)))
	if _, err := decodeGitHubRelease(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized release metadata error = %v", err)
	}
}

func TestReleaseAssetURLsAndRedirectsRequireHTTPS(t *testing.T) {
	for _, valid := range []string{
		"https://github.com/ehrlich-b/wingthing/releases/download/v1/wt-linux-amd64",
		"HTTPS://example.com/SHA256SUMS",
	} {
		if err := validateReleaseAssetURL(valid); err != nil {
			t.Errorf("valid release URL %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "/relative", "http://github.com/asset", "https:///missing-host"} {
		if err := validateReleaseAssetURL(invalid); err == nil {
			t.Errorf("insecure release URL %q was accepted", invalid)
		}
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unexpected"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	if _, err := releaseHTTPClient(time.Second).Get(source.URL); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("HTTPS downgrade redirect error = %v", err)
	}
}

func TestReleaseChecksumStrictlySelectsRequestedAsset(t *testing.T) {
	want := strings.Repeat("a", 64)
	manifest := []byte(
		strings.Repeat("b", 64) + "  wt-linux-arm64\n" +
			want + "  wt-linux-amd64\n",
	)
	got, err := releaseChecksum(manifest, "wt-linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("checksum = %q, want %q", got, want)
	}
	for _, invalid := range [][]byte{
		[]byte(strings.Repeat("a", 63) + "  wt-linux-amd64\n"),
		[]byte(strings.Repeat("z", 64) + "  wt-linux-amd64\n"),
		[]byte(strings.Repeat("a", 64) + "  other\n"),
		[]byte(strings.Repeat("a", 64) + "  wt-linux-amd64\n" + strings.Repeat("a", 64) + "  wt-linux-amd64\n"),
	} {
		if _, err := releaseChecksum(invalid, "wt-linux-amd64"); err == nil {
			t.Fatalf("invalid manifest accepted: %q", invalid)
		}
	}
}

func TestFetchReleaseChecksumBoundsAndHTTPStatus(t *testing.T) {
	want := strings.Repeat("c", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(want + " *wt-darwin-arm64\n"))
		case "/missing":
			http.NotFound(w, r)
		default:
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		}
	}))
	defer server.Close()

	got, err := fetchReleaseChecksum(context.Background(), server.URL+"/ok", "wt-darwin-arm64")
	if err != nil || got != want {
		t.Fatalf("fetch checksum = %q err=%v", got, err)
	}
	if _, err := fetchReleaseChecksum(context.Background(), server.URL+"/missing", "wt-darwin-arm64"); err == nil {
		t.Fatal("missing manifest returned success")
	}
	if _, err := fetchReleaseChecksum(context.Background(), server.URL+"/large", "wt-darwin-arm64"); err == nil {
		t.Fatal("oversized/invalid manifest returned success")
	}
}

func TestValidateReleaseBinaryRequiresCurrentPublicCommandSurface(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell fixture is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "wt")
	fixture := `#!/bin/sh
case "$*" in
  "--version") echo "wt version v-test" ;;
  "mcp connect --help") echo "connect directly" ;;
  "serve --help") echo "--https" ;;
  "roost start --help") echo "--https" ;;
  "local-cert status --help") echo "status" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(fixture), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseBinary(context.Background(), path); err != nil {
		t.Fatal(err)
	}

	broken := filepath.Join(t.TempDir(), "wt")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\necho old release\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseBinary(context.Background(), broken); err == nil {
		t.Fatal("binary without current public command surface was accepted")
	}

	hanging := filepath.Join(t.TempDir(), "wt")
	if err := os.WriteFile(hanging, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := validateReleaseBinary(ctx, hanging); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("hanging release validation error = %v", err)
	}
}

func TestWaitForProcessExitDoesNotConfuseRunningWithStopped(t *testing.T) {
	running, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if waitForProcessExit(running, 20*time.Millisecond) {
		t.Fatal("current process was reported stopped")
	}

	child := exec.Command("sleep", "5")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill child process: %v", err)
		}
	}()
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(waited)
	}()
	if !waitForProcessExit(child.Process, 2*time.Second) {
		t.Fatal("terminated process remained live")
	}
	<-waited
}

func TestDaemonRestartArgsDropsOnlyForegroundAndValidatesKind(t *testing.T) {
	args, err := daemonRestartArgs([]byte("roost\nstart\n--addr\n127.0.0.1:8080\n--foreground\n--https"), daemonctl.RoostDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, "|"); got != "roost|start|--addr|127.0.0.1:8080|--https" {
		t.Fatalf("restart args = %q", got)
	}
	if _, err := daemonRestartArgs([]byte("wing\nstart\n--foreground"), daemonctl.RoostDaemon); err == nil {
		t.Fatal("wing metadata was accepted as a roost restart command")
	}
}
