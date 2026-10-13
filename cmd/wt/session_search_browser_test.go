package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

// Opt-in browser acceptance uses the real built dashboard, real authenticated
// encrypted tunnels and two isolated wing runtimes. Only SSH is a fixture.
func TestBuiltSessionSearchDashboard(t *testing.T) {
	if os.Getenv("WT_SESSION_SEARCH_BROWSER") != "1" {
		t.Skip("select make test-session-search-web for Chromium acceptance")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(repo, "wt")
	}
	h := testssh.New(t)
	home := filepath.Join(h.Root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	localState := testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))
	remoteState := testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	start := func(state, label string, args ...string) *exec.Cmd {
		t.Helper()
		if err := config.SaveWingConfig(state, &config.WingConfig{Label: label, PhoneFleet: true, Roost: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = []string{"HOME=" + home, "WINGTHING_DIR=" + state, "PATH=" + h.Root + string(os.PathListSeparator) + os.Getenv("PATH"), "WT_FAKE_SSH_ROOT=" + h.Root, "SHELL=/bin/sh", "TERM=xterm-256color"}
		cmd.Dir = home
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		ready, done := make(chan struct{}), make(chan error, 1)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() {
			scanner := bufio.NewScanner(stderr)
			announced := false
			for scanner.Scan() {
				if !announced && strings.Contains(scanner.Text(), "wing: local control ready") {
					close(ready)
					announced = true
				}
			}
			done <- cmd.Wait()
		}()
		t.Cleanup(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
		select {
		case <-ready:
		case err := <-done:
			done <- err
			t.Fatalf("wing startup: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("wing startup deadline")
		}
		return cmd
	}
	start(remoteState, "Desktop", "wing", "start", "--foreground", "--local-only")
	start(localState, "Laptop", "roost", "start", "--foreground", "--addr", addr)
	localMeta, err := sshcontrol.InspectLocal(t.Context(), localState, "")
	if err != nil {
		t.Fatal(err)
	}
	remoteMeta, err := sshcontrol.InspectLocal(t.Context(), remoteState, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "search-peer", remoteMeta)
	if err := config.SaveRemotes(localState, map[string]config.Remote{"search-peer": {SSHTarget: "search-peer", WTBinary: binary, WingID: remoteMeta.WingID, WingthingDir: remoteState, ControlSocket: remoteMeta.ControlSocket, ControlVersion: control.ContractVersion}}); err != nil {
		t.Fatal(err)
	}
	seed := func(state, name, agent, text string) {
		t.Helper()
		dir := filepath.Join(state, "eggs", "shared-session-id")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		owner, err := config.EnsureLocalOwner(state, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteEggOwner(dir, owner.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteSessionName(dir, name); err != nil {
			t.Fatal(err)
		}
		event, _ := json.Marshal(map[string]any{"sequence": 1, "type": "message", "source": "codex_transcript", "role": "assistant", "text": text})
		for file, value := range map[string]string{"egg.meta": "agent=" + agent + "\ncwd=/personal/repo\nprovider_home=" + home + "\n", "lifecycle.jsonl": string(event) + "\n"} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(localState, "DNS-investigation", "codex", "Local DNS helper checked. API_KEY=sk-fixtureSECRET123456789")
	seed(remoteState, "DNS-repair", "claude", "Fixed the DNS helper on Desktop.")
	// This loopback fixture endpoint manipulates only a fixture SSH subprocess.
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/offline" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		h.Offline(t, "search-peer", true)
		select {
		case forward := <-h.Started:
			forward.Drop()
		case <-time.After(time.Second):
		}
		w.WriteHeader(204)
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(repo, "web/test/session-search-browser.mjs"), "http://"+addr, fixture.URL, localMeta.WingID, remoteMeta.WingID)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "WT_SEARCH_EXPECT_REAL_ROOST=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard journey: %v\n%s", err, out)
	}
	fmt.Print(string(out))
}
