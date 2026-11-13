package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
)

// Exercise the installed CLI against the deployment environment, rather than an
// in-process relay with different policy defaults or single-user local mode.
func TestWingsAgainstHostedShapeRelay(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the real wing's Unix socket short, including with a long TMPDIR.
	root, err := os.MkdirTemp("", "wh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	binary := filepath.Join(root, "wt")
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-p", "2", "-buildvcs=false", "-o", binary, "./cmd/wt")
	build.Dir = repo
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "CGO_ENABLED=") {
			build.Env = append(build.Env, variable)
		}
	}
	build.Env = append(build.Env, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CGO_ENABLED=0 wt: %v\n%s", err, output)
	}
	t.Log("built wt with CGO_ENABLED=0")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + addr
	// Do not inherit credentials, Fly topology, state overrides, provider CLIs,
	// or browser launchers from the developer's environment.
	environment := func(home string) []string {
		return []string{"HOME=" + home, "PATH=" + filepath.Join(root, "empty-bin"), "TMPDIR=" + os.TempDir(), "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}
	}
	relayHome := filepath.Join(root, "r")
	if err := os.Mkdir(relayHome, 0700); err != nil {
		t.Fatal(err)
	}
	serverEnv := environment(relayHome)
	for key, value := range hostedShapeFlyEnv(t, filepath.Join(repo, "fly.toml")) {
		if key != "WT_BASE_URL" {
			serverEnv = append(serverEnv, key+"="+value)
			t.Logf("fly.toml [env]: %s=%s", key, value)
		}
	}
	// A configured provider prevents serve's automatic single-user fallback.
	// Fixture claims below replace OAuth; no request goes to the provider.
	serverEnv = append(serverEnv, "WT_BASE_URL="+baseURL,
		"GITHUB_CLIENT_ID=fixture", "GITHUB_CLIENT_SECRET=fixture",
		"WT_JWT_SECRET=hosted-shape-fixture-signing-secret")
	hostedShapeStart(t, root, "relay", binary, serverEnv, "serve", "--addr", addr)
	httpClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(httpClient.CloseIdleConnections)
	hostedShapeAwait(t, "relay health", 10*time.Second, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("health: %s", response.Status)
		}
		return nil
	})
	store, err := relay.OpenRelay(filepath.Join(relayHome, ".wingthing", "roost.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, user := range []string{"hosted-owner", "hosted-outsider"} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	// Use the relay's existing device sign-in path and fixture claim method.
	// Tokens are minted over HTTP by serve, never fabricated or copied between
	// homes. Each client starts with only its own login state and identity key.
	signIn := func(name, deviceID, user string) string {
		t.Helper()
		home := filepath.Join(root, name)
		state := filepath.Join(home, ".wingthing")
		if err := os.MkdirAll(state, 0700); err != nil {
			t.Fatal(err)
		}
		publicKey, err := auth.EnsureKeyPair(state)
		if err != nil {
			t.Fatal(err)
		}
		code, err := auth.RequestDeviceCode(baseURL, deviceID, publicKey)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(code.VerificationURL, baseURL+"/auth/claim?") {
			t.Fatalf("device sign-in escaped fixture relay: %s", code.VerificationURL)
		}
		if err := store.ClaimDeviceCode(code.DeviceCode, user); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		token, err := auth.PollForToken(ctx, baseURL, code.DeviceCode, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := auth.NewTokenStore(state).Save(&auth.DeviceToken{
			Token: token.Token, ExpiresAt: token.ExpiresAt, DeviceID: deviceID, PublicKey: publicKey,
		}); err != nil {
			t.Fatal(err)
		}
		identity, err := auth.FetchUserInfo(baseURL, token.Token)
		if err != nil || identity.UserID != user {
			t.Fatalf("device signed in as wrong user: %+v, %v", identity, err)
		}
		data := fmt.Sprintf("wing_id: %s\nroost_url: %s\n", deviceID, baseURL)
		if err := os.WriteFile(filepath.Join(state, "config.yaml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return home
	}
	const wingID = "0123456789abcdef01234567"
	wingHome := signIn("w", wingID, "hosted-owner")
	if err := config.SaveWingConfig(filepath.Join(wingHome, ".wingthing"), &config.WingConfig{
		Label: "hosted-shape-wing", Roost: baseURL, ConnectionMode: "relay",
	}); err != nil {
		t.Fatal(err)
	}
	stopWing := hostedShapeStart(t, root, "wing", binary, environment(wingHome),
		"wing", "start", "--foreground", "--roost", baseURL, "--paths", wingHome, "--listen", "127.0.0.1")
	clientHome := signIn("c", "1123456789abcdef01234567", "hosted-owner")
	outsiderHome := signIn("o", "2123456789abcdef01234567", "hosted-outsider")
	wings := func(ctx context.Context, home string, jsonOutput bool) (string, []wingFinderEntry, error) {
		args := []string{"wings", "--timeout", "1s"}
		if jsonOutput {
			args = append(args, "--json")
		} else {
			args = append(args, "--no-probe")
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env, cmd.Dir = environment(home), home
		output, err := cmd.CombinedOutput()
		var entries []wingFinderEntry
		if err == nil && jsonOutput {
			err = json.Unmarshal(output, &entries)
		}
		return string(output), entries, err
	}
	started := time.Now()
	hostedShapeAwait(t, "owner sees wing online", 30*time.Second, func(ctx context.Context) error {
		output, entries, err := wings(ctx, clientHome, true)
		if err != nil || len(entries) != 1 || entries[0].WingID != wingID || !entries[0].Online {
			return fmt.Errorf("online roster: %v: %s", err, output)
		}
		t.Logf("wt wings --json: %s", strings.TrimSpace(output))
		return nil
	})
	t.Logf("owner sees %s online within %s (limit 30s)", wingID, time.Since(started))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, _, err := wings(ctx, clientHome, false)
	if err != nil || !strings.Contains(output, wingID) || !strings.Contains(output, "online") {
		t.Fatalf("human online output: %v: %s", err, output)
	}
	t.Logf("wt wings --no-probe:\n%s", output)
	output, entries, err := wings(ctx, outsiderHome, true)
	if err != nil || len(entries) != 0 {
		t.Fatalf("other user can see owner's online wing: %v: %s", err, output)
	}
	t.Log("second fixture user sees no wings while owner's wing is online")
	stopWing()
	stopped := time.Now()
	// This roster lists connected, accessible wings only. Assert disappearance
	// after disconnection; do not infer a named offline status from absence.
	hostedShapeAwait(t, "stopped wing disappears from online roster", 60*time.Second, func(ctx context.Context) error {
		output, entries, err := wings(ctx, clientHome, true)
		if err != nil || len(entries) != 0 {
			return fmt.Errorf("offline roster: %v: %s", err, output)
		}
		output, _, err = wings(ctx, clientHome, false)
		if err != nil || strings.TrimSpace(output) != "no wings online" {
			return fmt.Errorf("offline human output: %v: %s", err, output)
		}
		return nil
	})
	t.Logf("stopped wing absent: no wings online within %s (limit 60s)", time.Since(stopped))
	postStopCtx, cancelPostStop := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelPostStop()
	output, entries, err = wings(postStopCtx, outsiderHome, true)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outsider sees stopped wing: %v: %s", err, output)
	}
	t.Log("second fixture user sees no wings after shutdown")
}

func hostedShapeFlyEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	values := make(map[string]string)
	inEnv := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inEnv = line == "[env]"
			continue
		}
		if !inEnv || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		value, err := strconv.Unquote(strings.TrimSpace(raw))
		// fly.toml currently uses quoted string env values. Fail loudly if its
		// syntax changes rather than silently dropping deployment configuration.
		if !ok || err != nil {
			t.Fatalf("unsupported fly.toml env assignment %q: %v", line, err)
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"WT_BASE_URL", "WT_RELAY_POLICY", "WT_RELAY_MIGRATION_BEFORE"} {
		if values[key] == "" {
			t.Fatalf("fly.toml [env] missing %s", key)
		}
	}
	return values
}

func hostedShapeAwait(t *testing.T, description string, timeout time.Duration, check func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	for {
		err := check(ctx)
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s within %s: %v", description, timeout, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func hostedShapeStart(t *testing.T, root, name, binary string, env []string, args ...string) func() {
	t.Helper()
	logPath := filepath.Join(root, name+".log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, root, log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			select {
			case <-done:
				t.Errorf("%s exited before shutdown: %v", name, waitErr)
			default:
			}
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Errorf("%s did not stop gracefully", name)
			}
			_ = log.Close()
			if waitErr != nil {
				data, _ := os.ReadFile(logPath)
				// The foreground wing currently returns context.Canceled from
				// its signal-driven shutdown, which Cobra reports as exit 1.
				// Require its clean shutdown marker rather than ignoring errors.
				exit, ok := waitErr.(*exec.ExitError)
				if name != "wing" || !ok || exit.ExitCode() != 1 ||
					!strings.Contains(string(data), "wing daemon exiting cleanly\nError: context canceled") {
					t.Errorf("%s exited: %v", name, waitErr)
				}
			}
		})
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("%s process log:\n%s", name, data)
		}
	})
	return stop
}
