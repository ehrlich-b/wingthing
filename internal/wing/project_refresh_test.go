package wing

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

// Exercise the actual wing callback through a loopback relay, including its
// encrypted inventory response and SIGHUP completion acknowledgement.
func TestProjectRefreshInventoryAfterSIGHUP(t *testing.T) {
	// Build before localOnlyState changes HOME, so repeated runs share the
	// caller's Go cache instead of compiling into a fresh disposable home.
	// Compilation has its own bound and cannot consume the refresh deadline.
	binary := projectRefreshTestBinary(t)
	state, home, _ := localOnlyState(t)
	oldProject := filepath.Join(home, "old")
	newProject := filepath.Join(home, "new")
	if err := os.MkdirAll(filepath.Join(oldProject, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{ConnectionMode: "direct", Paths: config.PathList{{Path: oldProject}, {Path: newProject, Members: []string{"alice@example.com"}}}}
	if err := config.SaveWingConfig(state, wc); err != nil {
		t.Fatal(err)
	}
	if err := auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: "fixture", UserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	checkSession := projectRefreshLiveSession(t, ctx, binary, state, oldProject)
	checkSession("before")
	ready := make(chan *websocket.Conn, 1)
	messages := make(chan json.RawMessage, 32)
	var registrations atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		var reg ws.WingRegister
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		if len(reg.Projects) != 0 {
			t.Error("project paths disclosed in unencrypted registration")
		}
		registrations.Add(1)
		if err := wsjson.Write(ctx, conn, ws.RegisteredMsg{Type: ws.TypeRegistered, WingID: reg.WingID}); err != nil {
			return
		}
		ready <- conn
		for {
			var msg json.RawMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			select {
			case messages <- msg:
			case <-ctx.Done():
				return
			}
		}
	}))
	defer relay.Close()
	signals := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunWingWithContext(EntryOptions{Version: "test"}, ctx, signals, relay.URL, "", "auto", "", "", nil, "", false, false, false, false, false, nil)
	}()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("wing exit: %v", err)
		}
	}()
	var conn *websocket.Conn
	select {
	case conn = <-ready:
	case <-ctx.Done():
		t.Fatal("wing did not register")
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wingKey, err := auth.LoadPrivateKey(state)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := auth.DeriveSharedKey(key, base64.StdEncoding.EncodeToString(wingKey.PublicKey().Bytes()), "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	waitMessage := func(kind string) json.RawMessage {
		t.Helper()
		for {
			select {
			case msg := <-messages:
				var env ws.Envelope
				if err := json.Unmarshal(msg, &env); err != nil {
					t.Fatal(err)
				}
				if env.Type == kind {
					return msg
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s: %v", kind, ctx.Err())
			}
		}
	}
	inventory := func(role, email string, legacy bool) []string {
		t.Helper()
		payload, err := auth.Encrypt(gcm, []byte(`{"type":"wing.info"}`))
		if err != nil {
			t.Fatal(err)
		}
		req := ws.TunnelRequest{Type: ws.TypeTunnelRequest, RequestID: "inventory", Purpose: ws.TunnelPurposeDiscovery, SenderUserID: "owner", SenderOrgRole: role, SenderEmail: email, SenderPub: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Payload: payload}
		if legacy {
			req.Purpose = ""
		}
		if err := wsjson.Write(ctx, conn, req); err != nil {
			t.Fatal(err)
		}
		var response ws.TunnelResponse
		if err := json.Unmarshal(waitMessage(ws.TypeTunnelResponse), &response); err != nil {
			t.Fatal(err)
		}
		plain, err := auth.Decrypt(gcm, response.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var info struct {
			Projects []ws.WingProject `json:"projects"`
		}
		if err := json.Unmarshal(plain, &info); err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, p := range info.Projects {
			paths = append(paths, p.Path)
		}
		return paths
	}
	if got := inventory("owner", "", false); !reflect.DeepEqual(got, []string{oldProject}) {
		t.Fatalf("initial projects: %v", got)
	}
	if err := os.MkdirAll(filepath.Join(newProject, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(oldProject, ".git")); err != nil {
		t.Fatal(err)
	}
	signals <- syscall.SIGHUP
	waitMessage(ws.TypeWingConfig)
	if got := inventory("owner", "", false); !reflect.DeepEqual(got, []string{newProject}) {
		t.Fatalf("inventory stayed at startup snapshot after project add/remove: got %v, want %v", got, []string{newProject})
	}
	if got := inventory("member", "alice@example.com", true); !reflect.DeepEqual(got, []string{newProject}) {
		t.Fatalf("legacy member inventory: %v", got)
	}
	if got := inventory("member", "bob@example.com", false); len(got) != 0 {
		t.Fatalf("unauthorized member received projects: %v", got)
	}
	wc.Paths[1].Members = []string{"bob@example.com"}
	if err := config.SaveWingConfig(state, wc); err != nil {
		t.Fatal(err)
	}
	signals <- syscall.SIGHUP
	waitMessage(ws.TypeWingConfig)
	if got := inventory("member", "alice@example.com", false); len(got) != 0 {
		t.Fatalf("revoked member received projects: %v", got)
	}
	if got := inventory("member", "bob@example.com", false); !reflect.DeepEqual(got, []string{newProject}) {
		t.Fatalf("newly authorized member inventory: %v", got)
	}
	checkSession("after")
	if registrations.Load() != 1 {
		t.Fatal("refresh reconnected the wing")
	}
}

// Keep cold Go/CGO compilation bounded independently of live-session waits.
func projectRefreshTestBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("WT_TEST_BINARY"); binary != "" {
		return binary
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "wt")
	started := time.Now()
	build := exec.CommandContext(ctx, "nice", "-n", "10", "go", "build", "-p", "2", "-o", binary, "./cmd/wt")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build test CLI after %s: %v\n%s", time.Since(started), err, output)
	}
	t.Logf("built test CLI in %s", time.Since(started))
	return binary
}

// The same real egg, child process and writer stream must survive refresh.
func projectRefreshLiveSession(t *testing.T, ctx context.Context, binary, state, work string) func(string) {
	t.Helper()
	script := `printf '%s' "$$" > child.pid; printf 'ready\n'; while IFS= read -r line; do printf 'reply:%s\n' "$line"; done`
	cmd := exec.CommandContext(ctx, binary, "terminal", "--cwd", work, "--json", "--", "/bin/sh", "-c", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start real session: %v\n%s", err, output)
	}
	var started struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(output, &started); err != nil || started.Session == "" {
		t.Fatalf("start: %s %v", output, err)
	}
	dir := filepath.Join(state, "eggs", started.Session)
	client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Kill(stopCtx, started.Session); err != nil {
			t.Errorf("stop real session: %v", err)
		}
		_ = client.Close()
	})
	stream, err := client.AttachSessionWithOptions(ctx, started.Session, egg.AttachOptions{Takeover: true})
	if err != nil {
		t.Fatal(err)
	}
	var eggPID, childPID []byte
	return func(label string) {
		t.Helper()
		if err := stream.Send(&pb.SessionMsg{SessionId: started.Session, Payload: &pb.SessionMsg_Input{Input: []byte(label + "\n")}}); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for !strings.Contains(text.String(), "reply:"+label) {
			msg, err := stream.Recv()
			if err != nil {
				t.Fatalf("session %s: %v", label, err)
			}
			text.Write(msg.GetOutput())
		}
		for path, previous := range map[string]*[]byte{filepath.Join(dir, "egg.pid"): &eggPID, filepath.Join(work, "child.pid"): &childPID} {
			pid, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if *previous != nil && !slices.Equal(*previous, pid) {
				t.Fatalf("process restarted: %s", path)
			}
			*previous = pid
		}
		t.Logf("%s refresh: same terminal stream responds; egg pid=%s child pid=%s", label, strings.TrimSpace(string(eggPID)), childPID)
	}
}

func TestProjectRefreshBoundedImmutableSnapshots(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	oldProject, newProject := filepath.Join(root, "old"), filepath.Join(root, "new")
	for _, path := range []string{oldProject, filepath.Join(cwd, "outside")} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p := newProjectInventory(cwd)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	var scans atomic.Int32
	p.discover = func(roots []string, cwd string) []ws.WingProject {
		scans.Add(1)
		return wingpolicy.DiscoverWingProjects(roots, cwd)
	}
	roots := []string{root}
	before := p.snapshot(roots)
	if len(before) != 1 || before[0].Path != oldProject {
		t.Fatalf("configured-root snapshot: %v", before)
	}
	if err := os.RemoveAll(oldProject); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(newProject, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(projectRefreshInterval - time.Nanosecond))
	if got := p.snapshot(roots); !reflect.DeepEqual(got, before) {
		t.Fatal("refreshed before interval")
	}
	clock.Add(1)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			got := p.snapshot(roots)
			if len(got) != 1 || got[0].Path != newProject {
				t.Errorf("refreshed snapshot: %v", got)
				return
			}
			got[0].Name = "caller mutation"
		})
	}
	wg.Wait()
	if scans.Load() != 2 {
		t.Fatalf("concurrent requests made %d scans, want 2 total", scans.Load())
	}
	if before[0].Path != oldProject || p.snapshot(roots)[0].Name != "new" {
		t.Fatal("snapshot mutated")
	}
	// Path mutations refresh immediately, without ever supplementing explicit
	// roots from cwd. Only an unconfigured wing falls back to cwd.
	if got := p.snapshot([]string{newProject}); len(got) != 1 || got[0].Path != newProject {
		t.Fatalf("changed roots: %v", got)
	}
	if got := p.snapshot(nil); len(got) != 1 || got[0].Path != filepath.Join(cwd, "outside") {
		t.Fatalf("cwd fallback: %v", got)
	}
	p.invalidate()
	p.snapshot(nil)
	if scans.Load() != 5 {
		t.Fatalf("root changes and invalidation made %d scans, want 5 total", scans.Load())
	}
}
