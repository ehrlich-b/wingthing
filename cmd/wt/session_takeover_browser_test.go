package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

// Real built browser/relay/encrypted tunnels/PTYs; only provider and SSH are
// fixtures. The owner and local relay tokens are created in disposable state.
func TestBuiltSessionTakeoverDashboard(t *testing.T) {
	if os.Getenv("WT_SESSION_TAKEOVER_BROWSER") != "1" {
		t.Skip("select make test-session-takeover-web for Chromium acceptance")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(repo, "wt-preview")
	}
	ssh := testssh.New(t)
	homes := []string{filepath.Join(ssh.Root, "local-home"), filepath.Join(ssh.Root, "remote-home")}
	states := []string{testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch")), testprovider.ShortStateDir(t, filepath.Join(repo, ".scratch"))}
	for i := range states {
		if err := os.Mkdir(homes[i], 0700); err != nil {
			t.Fatal(err)
		}
		initialize := exec.Command(binary, "session", "inventory", "--json")
		initialize.Env = []string{"HOME=" + homes[i], "WINGTHING_DIR=" + states[i], "PATH=" + os.Getenv("PATH")}
		initialize.Dir = homes[i]
		_, _ = initialize.CombinedOutput()
		if marker, err := os.ReadFile(filepath.Join(states[i], ".release-channel")); err != nil || strings.TrimSpace(string(marker)) != "preview" {
			t.Fatalf("fixture not initialized by preview executable: %v", err)
		}
		if err := os.WriteFile(filepath.Join(states[i], "egg.yaml"), []byte("isolation: privileged\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ssh.Root, "claude"), []byte(takeoverClaudeFixture), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	base := "http://" + address
	start := func(i int, label string, args ...string) *exec.Cmd {
		t.Helper()
		if err := config.SaveWingConfig(states[i], &config.WingConfig{Label: label, PhoneFleet: true, AllowUnsandboxed: true, Roost: base}); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = []string{"HOME=" + homes[i], "WINGTHING_DIR=" + states[i], "PATH=" + ssh.Root + string(os.PathListSeparator) + os.Getenv("PATH"), "WT_FAKE_SSH_ROOT=" + ssh.Root, "SHELL=/bin/sh", "TERM=xterm-256color"}
		cmd.Dir = homes[i]
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		ready, done := make(chan struct{}), make(chan error, 1)
		var startupLog strings.Builder
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() {
			scanner := bufio.NewScanner(stderr)
			announced := false
			for scanner.Scan() {
				startupLog.WriteString(scanner.Text() + "\n")
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
			t.Fatalf("wing startup: %v\n%s", err, startupLog.String())
		case <-time.After(10 * time.Second):
			t.Fatal("wing startup deadline")
		}
		return cmd
	}
	start(0, "Laptop", "roost", "start", "--foreground", "--addr", address)
	localOwner, err := config.EnsureLocalOwner(states[0], "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := relay.OpenRelay(filepath.Join(states[0], "roost.db"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.GenerateAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDeviceToken(token, "local", "takeover-fixture", nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := auth.NewTokenStore(states[1]).Save(&auth.DeviceToken{Token: token, UserID: "local", DeviceID: "takeover-fixture"}); err != nil {
		t.Fatal(err)
	}
	destination, _ := url.Parse(base)
	directProxy := httputil.NewSingleHostReverseProxy(destination)
	var directDown atomic.Bool
	var directMu sync.Mutex
	var directConnections []net.Conn
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			directMu.Lock()
			directConnections = append(directConnections, conn)
			directMu.Unlock()
		}
		return conn, err
	}
	directProxy.Transport = transport
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if directDown.Load() {
			w.WriteHeader(503)
			return
		}
		directProxy.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	defer transport.CloseIdleConnections()
	// The explicit fixture enrollment binds only this disposable local relay.
	if _, err := config.BindLocalOwnerRelay(states[1], proxy.URL, "local"); err != nil {
		t.Fatal(err)
	}
	start(1, "Desktop", "wing", "start", "--foreground", "--roost", proxy.URL)
	localMeta, err := sshcontrol.InspectLocal(t.Context(), states[0], "")
	if err != nil {
		t.Fatal(err)
	}
	remoteMeta, err := sshcontrol.InspectLocal(t.Context(), states[1], "")
	if err != nil {
		t.Fatal(err)
	}
	ssh.Host(t, "takeover-peer", remoteMeta)
	if err := config.SaveRemotes(states[0], map[string]config.Remote{"takeover-peer": {SSHTarget: "takeover-peer", WTBinary: binary, WingID: remoteMeta.WingID, WingthingDir: states[1], ControlSocket: remoteMeta.ControlSocket, ControlVersion: control.ContractVersion}}); err != nil {
		t.Fatal(err)
	}
	launch, _ := json.Marshal(map[string]any{"agent": "claude", "cwd": homes[1], "model": "fixture", "label": "DNS-repair"})
	started, err := localmcp.CallLocalWingTool(t.Context(), states[1], "", "agent_start", launch)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := started["session"].(string)
	if !ok {
		t.Fatal("missing exact launched session")
	}
	t.Cleanup(func() { _ = eggclient.KillOrphanEggContext(context.Background(), &config.Config{Dir: states[1]}, id) })
	// A second machine deliberately has the same full ID; only the remote one
	// is live. Search/action must retain origin, even with identical selectors.
	dir := filepath.Join(states[0], "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteEggOwner(dir, localOwner.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := eggclient.WriteSessionName(dir, "DNS-local"); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"sequence": 1, "type": "message", "source": "claude_transcript", "role": "assistant", "text": "Local DNS helper is a separate session."})
	for file, text := range map[string]string{"egg.meta": "agent=claude\ncwd=" + homes[0] + "\nprovider_home=" + homes[0] + "\n", "lifecycle.jsonl": string(event) + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Direct relay down while the execution process/control socket and verified
	// gateway stay healthy. The relay credential is restored only for fixtures.
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/direct-down":
			directDown.Store(true)
			directMu.Lock()
			for _, conn := range directConnections {
				_ = conn.Close()
			}
			directMu.Unlock()
		case "/remote-down":
			ssh.Offline(t, "takeover-peer", true)
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(204)
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(repo, "web/test/session-takeover-browser.mjs"), base, fixture.URL, localMeta.WingID, remoteMeta.WingID, id)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "WT_TAKEOVER_EXPECT_REAL_ROOST=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("takeover journey: %v\n%s", err, output)
	}
	if !pake.Available {
		t.Fatal("required encrypted phone fixture needs CGO PAKE")
	}
	phone := pairBuiltFleetPhone(t, states[0])
	identity := map[string]any{"origin_wing_id": remoteMeta.WingID, "session_id": id}
	attach := map[string]any{"origin_wing_id": remoteMeta.WingID, "session_id": id, "takeover": true}
	phoneResult := func(op string, args any) map[string]any {
		outer := phone.command(op, args)
		if outer["error"] != nil {
			t.Fatalf("phone %s failed: %v", op, outer["error"])
		}
		value, ok := outer["result"].(map[string]any)
		if !ok {
			t.Fatalf("phone %s result is not structured", op)
		}
		return value
	}
	receipt := phoneResult("fleet_attach", attach)
	if receipt["error"] != nil || receipt["session_id"] != id || receipt["origin_wing_id"] != remoteMeta.WingID {
		t.Fatalf("phone exact attach failed: %v", receipt["error"])
	}
	input := map[string]any{"origin_wing_id": remoteMeta.WingID, "session_id": id, "input": "Phone takeover input\r"}
	if sent := phoneResult("fleet_input", input); sent["ok"] != true {
		t.Fatalf("phone PTY input failed: %v", sent["error"])
	}
	deadline := time.Now().Add(5 * time.Second)
	observed := false
	for time.Now().Before(deadline) {
		read := phoneResult("fleet_read", identity)
		screen, _ := read["screen"].(string)
		if strings.Contains(screen, "Phone takeover input") {
			observed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !observed {
		t.Fatal("encrypted phone input never reached exact remote PTY")
	}
	handback := phoneResult("fleet_detach", identity)
	if handback["ok"] != true || handback["session_id"] != id {
		t.Fatal("encrypted phone hand-back failed")
	}
	if after := phone.command("fleet_input", input); after["error"] == nil {
		t.Fatal("phone retained writer after hand-back")
	}
	prompt := map[string]any{"origin_wing_id": remoteMeta.WingID, "session_id": id, "operation": "session_prompt", "arguments": map[string]any{"request_id": "phone-message-after-handback", "input": "Phone message after hand-back", "timeout_seconds": 5}}
	message := phoneResult("action", prompt)
	if message["error"] != nil {
		t.Fatalf("qualified phone prompt failed: %v", message["error"])
	}
	response, _ := message["result"].(map[string]any)
	native, _ := response["receipt"].(map[string]any)
	if native["native_receipt_observed"] != true {
		t.Fatal("phone message text was not natively observed")
	}
	_ = phone.command("detach", nil)
	fmt.Println("encrypted paired phone: qualified gateway takeover, PTY input, hand-back, writer denial and native prompt text receipt passed")
	fmt.Print(string(output))
}

const takeoverClaudeFixture = `#!/usr/bin/python3
import json, os, subprocess, sys, tty, uuid, datetime
args=sys.argv[1:]
if args==['--version']:
 print('fake-claude 1.0');sys.exit(0)
with open(args[args.index('--settings')+1]) as f: hooks=json.load(f)['hooks']
provider=args[args.index('--session-id')+1]
project=os.getcwd().replace('/','-')
transcript=os.path.join(os.environ['HOME'],'.claude','projects',project,provider+'.jsonl')
os.makedirs(os.path.dirname(transcript),exist_ok=True)
def record(role,text):
 value={'uuid':str(uuid.uuid4()),'type':role,'sessionId':provider,'timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat(),'message':{'role':role,'content':text}}
 with open(transcript,'a') as f:f.write(json.dumps(value)+'\n')
def hook(kind,**extra):
 payload={'hook_event_name':kind,'session_id':provider};payload.update(extra)
 for entry in hooks.get(kind,[]):
  for command in entry['hooks']:subprocess.run(['/bin/sh','-c',command['command']],input=json.dumps(payload).encode(),check=True,timeout=5)
tty.setraw(0)
hook('SessionStart',source='startup')
record('assistant','Desktop DNS helper is ready for your next step.')
hook('Notification',notification_type='idle_prompt')
os.write(1,b'DNS repair terminal ready\r\n> ')
buffer=b''
while True:
 chunk=os.read(0,1)
 if not chunk:break
 if chunk==b'\x03':os.write(1,b'Interrupted by human\r\n> ');buffer=b'';continue
 if chunk!=b'\r':buffer+=chunk;continue
 text=buffer.decode().removeprefix('\x1b[200~').removesuffix('\x1b[201~');buffer=b''
 record('user',text);hook('UserPromptSubmit',prompt=text)
 reply='Checked the DNS helper: '+text
 record('assistant',reply);os.write(1,reply.encode()+b'\r\n> ')
 hook('Stop')
 if 'permission' in text.lower():hook('Notification',notification_type='permission_prompt')
`
