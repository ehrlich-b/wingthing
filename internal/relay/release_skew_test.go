package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// Verify the retained release bytes before executing any command, including
// --version. SHA256SUMS travels beside the original release binary.
func verifyReleasedCLI(binary string) error {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("released v0.149.5 binary required at %s; set WT_RELEASE_SKEW_BINARY to the original binary with adjacent SHA256SUMS", binary)
	}
	manifest := filepath.Join(filepath.Dir(binary), "SHA256SUMS")
	sums, err := os.ReadFile(manifest)
	if err != nil {
		return fmt.Errorf("released v0.149.5 SHA256SUMS required: %w", err)
	}
	file, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	var matches []string
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == filepath.Base(binary) {
			matches = append(matches, fields[0])
		}
	}
	if len(matches) != 1 || matches[0] != actual {
		return fmt.Errorf("released v0.149.5 checksum mismatch against %s: %s", manifest, binary)
	}
	return nil
}

func TestReleasedCLIBaselineValidation(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "wt-linux-amd64")
	if err := verifyReleasedCLI(binary); err == nil || !strings.Contains(err.Error(), "WT_RELEASE_SKEW_BINARY") {
		t.Fatalf("missing artifact must be actionable: %v", err)
	}
	data := []byte("small nonexecutable checksum fixture")
	mustTest(t, os.WriteFile(binary, data, 0600))
	if err := verifyReleasedCLI(binary); err == nil || !strings.Contains(err.Error(), "SHA256SUMS required") {
		t.Fatalf("missing manifest accepted: %v", err)
	}
	manifest := filepath.Join(filepath.Dir(binary), "SHA256SUMS")
	entry := fmt.Sprintf("%x *%s\n", sha256.Sum256(data), filepath.Base(binary))
	mustTest(t, os.WriteFile(manifest, []byte(entry), 0600))
	mustTest(t, verifyReleasedCLI(binary))
	mustTest(t, os.WriteFile(binary, []byte("corrupt"), 0600))
	if err := verifyReleasedCLI(binary); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt artifact accepted: %v", err)
	}
	mustTest(t, os.WriteFile(manifest, []byte(entry+entry), 0600))
	if err := verifyReleasedCLI(binary); err == nil {
		t.Fatal("ambiguous checksum manifest accepted")
	}
}

// releaseSkewOutput acknowledges receipt of the complete response before the
// fixture shell may exit; the released CLI can observe exit before draining PTY.
// exec serializes writes to the shared stdout/stderr writer, and Wait joins it.
type releaseSkewOutput struct {
	buffer bytes.Buffer
	echoed chan struct{}
	once   sync.Once
}

func (w *releaseSkewOutput) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if bytes.Contains(w.buffer.Bytes(), []byte("HEAD-ATTACH-ECHO:previous-release-input")) {
		w.once.Do(func() { close(w.echoed) })
	}
	return n, err
}

// TestReleasedCLISkew executes the shipped artifact, never a rebuild of its tag.
// make test-release-skew requires it; ordinary unit tests remain artifact-free.
func TestReleasedCLISkew(t *testing.T) {
	if os.Getenv("WT_RELEASE_SKEW_REQUIRED") != "1" {
		t.Skip("run make test-release-skew with the original v0.149.5 binary and SHA256SUMS")
	}
	binary := os.Getenv("WT_RELEASE_SKEW_BINARY")
	if binary == "" {
		binary = "/work/cache/wingthing-releases/v0.149.5/wt-linux-amd64"
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyReleasedCLI(binary); err != nil {
		t.Fatal(err)
	}
	t.Logf("SHA256SUMS verified for %s", binary)
	store := testStore(t)
	token, userID := createTestToken(t, store, "release-skew")
	server := NewServer(store, ServerConfig{})
	// Suppress the release-feed network fetch; the tested HTTP server is real.
	server.latestVersion = "v0.149.5"
	server.latestVersionAt = time.Now()
	var mu sync.Mutex
	var requests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		server.ServeHTTP(w, r)
	}))
	defer ts.Close()
	// Reserve socket-path space for long TMPDIR roots as well as the egg suffix.
	// MkdirTemp still gives each run private, unique state.
	state, err := os.MkdirTemp("", "wt-")
	mustTest(t, err)
	defer os.RemoveAll(state)
	mustTest(t, os.WriteFile(filepath.Join(state, "config.yaml"), []byte("roost_url: "+ts.URL+"\n"), 0600))
	mustTest(t, auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: token, UserID: userID, DeviceID: "release-skew"}))
	home := t.TempDir()
	runInput := func(input string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "WINGTHING_DIR=" + state,
			"TMPDIR=" + os.TempDir(), "NO_COLOR=1"}
		command.Stdin = strings.NewReader(input)
		output, err := command.CombinedOutput()
		t.Logf("v0.149.5 wt %s against HEAD relay %s: exit=%v\n%s", strings.Join(args, " "), ts.URL, err, output)
		if err != nil {
			t.Fatalf("released CLI failed: %v", err)
		}
		return string(output)
	}
	run := func(args ...string) string { return runInput("", args...) }
	if version := run("--version"); !strings.Contains(version, "0.149.5") {
		t.Fatalf("wrong released binary: %s", version)
	}
	if identity := run("whoami"); !strings.Contains(identity, userID) {
		t.Fatalf("wrong identity: %s", identity)
	}
	// The released CLI attaches through native wing control, never an HTTP
	// relay PTY route. Advertise that same HEAD runtime in the HEAD directory
	// and exercise its real control listener and egg with a finite command.
	const wingID, sessionID = "release-skew-wing", "s"
	cfg := &config.Config{Dir: state, WingID: wingID}
	wc := &config.WingConfig{WingID: wingID, Paths: config.PathList{{Path: home}}}
	service := &wingsession.Service{Config: cfg, Home: home, Policy: func() wingsession.Policy {
		return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()}
	}}
	_, err = config.EnsureLocalOwner(state, userID)
	mustTest(t, err)
	listener, err := localmcp.ListenLocalWingControl(t.Context(), "HEAD", service, userID, nil)
	mustTest(t, err)
	if listener == nil {
		t.Fatal("HEAD wing control is not listening")
	}
	defer listener.Close()
	server.Wings.Add(&ConnectedWing{ID: "release-skew", WingID: wingID, UserID: userID})
	var wings []map[string]any
	mustTest(t, json.Unmarshal([]byte(run("wings", "--json", "--no-probe")), &wings))
	if len(wings) != 1 || wings[0]["wing_id"] != wingID {
		t.Fatalf("HEAD relay did not advertise the attachment wing: %v", wings)
	}
	eggDir := filepath.Join(state, "eggs", sessionID)
	mustTest(t, os.MkdirAll(eggDir, 0700))
	mustTest(t, eggclient.WriteEggOwner(eggDir, userID, ""))
	mustTest(t, eggclient.WriteSessionPrincipal(eggDir, wingsession.UserPrincipal(userID)))
	headEgg, err := egg.NewServer(eggDir)
	mustTest(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	// No provider, external command input, or sandbox qualification is needed
	// for this transport fixture. The isolated shell owns only its test home.
	go func() {
		finished <- headEgg.RunSession(ctx, egg.RunConfig{
			Command: []string{"/bin/sh", "-c", "printf 'HEAD-ATTACH-READY\\n'; read line; printf 'HEAD-ATTACH-ECHO:%s\\n' \"$line\"; read finish"},
			CWD:     home, UserHome: home, OuterBoundary: true, Network: []string{"*"},
			Env: map[string]string{"HOME": home}, Rows: 24, Cols: 80, OmitBrowserBridge: true,
		})
	}()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("HEAD egg: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("HEAD egg did not stop")
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		client, err := egg.Dial(filepath.Join(eggDir, "egg.sock"), filepath.Join(eggDir, "egg.token"))
		if err == nil {
			status, statusErr := client.Status(ctx)
			client.Close()
			if statusErr == nil && status.SessionId == sessionID {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("HEAD egg did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var sessions []map[string]any
	mustTest(t, json.Unmarshal([]byte(run("attach", "--json")), &sessions))
	if len(sessions) != 1 || sessions[0]["id"] != sessionID {
		t.Fatalf("HEAD wing inventory missing live session: %v", sessions)
	}
	// Keep stdin open until HEAD sends its explicit exit result. A closed pipe
	// means detach to the native CLI, and would race away the output proof.
	attachCtx, attachCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer attachCancel()
	command := exec.CommandContext(attachCtx, binary, "attach", sessionID)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "WINGTHING_DIR=" + state,
		"TMPDIR=" + os.TempDir(), "NO_COLOR=1"}
	stdin, err := command.StdinPipe()
	mustTest(t, err)
	output := releaseSkewOutput{echoed: make(chan struct{})}
	command.Stdout, command.Stderr = &output, &output
	mustTest(t, command.Start())
	_, err = fmt.Fprintln(stdin, "previous-release-input")
	mustTest(t, err)
	select {
	case <-output.echoed:
		// Keep the egg alive until the old client has delivered its response.
		_, err = fmt.Fprintln(stdin, "finish")
		if err != nil {
			attachCancel()
		}
	case <-attachCtx.Done():
		attachCancel()
	}
	err = command.Wait()
	stdin.Close()
	t.Logf("v0.149.5 wt attach %s on HEAD wing %s advertised by HEAD relay %s: exit=%v\n%s", sessionID, wingID, ts.URL, err, output.buffer.String())
	mustTest(t, err)
	for _, expected := range []string{"HEAD-ATTACH-READY", "HEAD-ATTACH-ECHO:previous-release-input"} {
		if !strings.Contains(output.buffer.String(), expected) {
			t.Fatalf("attachment missing %q: %s", expected, output.buffer.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("HEAD relay requests: %v", requests)
	for _, expected := range []string{"GET /auth/check", "GET /api/app/wings"} {
		found := false
		for _, request := range requests {
			if request == expected {
				found = true
			}
		}
		if !found {
			t.Errorf("released CLI did not reach %s", expected)
		}
	}
}
