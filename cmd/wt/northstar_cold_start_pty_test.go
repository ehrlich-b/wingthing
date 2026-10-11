package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"golang.org/x/term"
)

// The provider still runs under the native scoped-parent sandbox. It checks
// the host mailbox binding and stays alive until the fixture explicitly exits.
const coldClaudeProvider = `import json, os, socket, subprocess, sys, tty
args=sys.argv[1:]
if args==['--version']:
 print('fake-claude 1.0'); sys.exit(0)
assert args.count('--mcp-config')==1 and args.count('--strict-mcp-config')==1
with open(args[args.index('--mcp-config')+1]) as f: server=json.load(f)['mcpServers']['wingthing']
assert '--host-mailbox' in server['args']
mailbox=server['args'][server['args'].index('--host-mailbox')+1]
# The provider writes the exchange queue in its workspace. Host dispatch
# registration/state and the controller remain under protected fixture HOME.
state=__FIXTURE_STATE__
assert state.startswith(os.environ['HOME']+'/') and not state.startswith(os.getcwd()+'/')
assert server['command'].startswith(os.environ['HOME']+'/')
assert mailbox.startswith(os.getcwd()+'/.wingthing-conversations/')
try:
 with open(state+'/wing.yaml','rb'): pass
except PermissionError: pass
else: raise RuntimeError('provider could read protected wing policy')
probe=socket.socket(socket.AF_UNIX)
try: probe.connect(state+'/control.sock')
except (PermissionError,OSError): pass
else: raise RuntimeError('provider received unrestricted control socket')
finally: probe.close()
client=subprocess.Popen([server['command']]+server['args'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
client.stdin.write(json.dumps({'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2025-11-25','capabilities':{},'clientInfo':{'name':'cold-fixture','version':'1'}}})+'\n');client.stdin.flush()
initialized=json.loads(client.stdout.readline())
assert initialized['result']['protocolVersion']=='2025-11-25'
tty.setraw(0)
os.write(1,b'COLD_CLAUDE_READY\r\n')
line=b''
while True:
 chunk=os.read(0,1)
 if not chunk: break
 if chunk not in (b'\r',b'\n'): line+=chunk;continue
 if line==b'exit': break
 line=b''
client.terminate();client.wait()
`

func coldFixture(t *testing.T) *northstarFixture {
	t.Helper()
	f := northstarNewFixture(t)
	// Cold local startup has no remembered remote targets. Keep this fixture
	// independent of the navigator helper's fake SSH inventory.
	if err := os.Remove(filepath.Join(f.local, "remotes.yaml")); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(f.local)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(coldClaudeProvider, "__FIXTURE_STATE__", string(state))
	northstarWrite(t, filepath.Join(f.bin, "claude"), "#!"+python+"\n"+script, 0700)
	policy, err := testprovider.ScopedHomePolicy(f.home).YAML()
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.local, "egg.yaml"), policy, 0600)
	if err := config.SaveWingConfig(f.local, &config.WingConfig{Paths: config.PathList{{Path: f.work}}, Roost: "http://127.0.0.1:1", HostedRelay: config.HostedRelayDeny}); err != nil {
		t.Fatal(err)
	}
	path, err := controlsocket.Path(f.local)
	if err != nil {
		t.Fatal(err)
	}
	northstarSocket(t, path)
	return f
}

func coldInstallBinary(t *testing.T, f *northstarFixture, built string) string {
	t.Helper()
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(f.home, "wt")
	if err := os.WriteFile(protected, data, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.bin, "wt")
	if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func coldCleanup(t *testing.T, f *northstarFixture, binary string) {
	t.Helper()
	t.Cleanup(func() {
		entries, _ := os.ReadDir(filepath.Join(f.local, "eggs"))
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if t.Failed() {
				testprovider.LogEggTails(t, filepath.Join(f.local, "eggs", entry.Name()))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := eggclient.KillOrphanEggContext(ctx, &config.Config{Dir: f.local}, entry.Name()); err != nil {
				t.Errorf("clean fixture egg: %v", err)
			}
			cancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		stop := exec.CommandContext(ctx, binary, "wing", "stop")
		stop.Env, stop.Dir = f.env, f.work
		if output, err := stop.CombinedOutput(); err != nil {
			if _, statErr := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(statErr) {
				t.Errorf("stop fixture daemon: %v %s", err, output)
			}
		}
	})
}

func coldPTYExit(t *testing.T, h *northstarPTY, wantFailure bool) {
	t.Helper()
	select {
	case err := <-h.done:
		if (err != nil) != wantFailure {
			t.Fatalf("PTY exit: %v, want failure=%t", err, wantFailure)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cold Claude PTY did not exit")
	}
	after, err := term.GetState(int(h.master.Fd()))
	if err != nil || !reflect.DeepEqual(h.before, after) {
		t.Fatalf("cold Claude did not restore termios: %v", err)
	}
}

func coldSessionIDs(t *testing.T, f *northstarFixture) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.local, "eggs"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range entries {
		dir := filepath.Join(f.local, "eggs", entry.Name())
		if _, alive := eggclient.ReadAliveEggPID(dir); alive {
			northstarSocket(t, filepath.Join(dir, "egg.sock"))
			ids = append(ids, entry.Name())
		}
	}
	return ids
}

func coldPolicyGuard(t *testing.T, f *northstarFixture) {
	t.Helper()
	before := map[string][]byte{}
	for _, name := range []string{"wing.yaml", "egg.yaml", "clients.yaml"} {
		data, err := os.ReadFile(filepath.Join(f.local, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		before[name] = data
	}
	t.Cleanup(func() {
		for name, want := range before {
			got, err := os.ReadFile(filepath.Join(f.local, name))
			if err != nil && !os.IsNotExist(err) || !bytes.Equal(got, want) {
				t.Errorf("cold launch changed policy %s: %v", name, err)
			}
		}
	})
}

func coldRelayGuard(t *testing.T, f *northstarFixture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("required loopback relay fixture unavailable: %v", err)
	}
	var requests atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if requests.Load() != 0 {
			t.Errorf("local-only launch contacted relay %d times", requests.Load())
		}
	})
	if err := config.SaveWingConfig(f.local, &config.WingConfig{Paths: config.PathList{{Path: f.work}}, Roost: "http://" + listener.Addr().String(), HostedRelay: config.HostedRelayAllow}); err != nil {
		t.Fatal(err)
	}
}

func coldOneDaemon(t *testing.T, f *northstarFixture) []byte {
	t.Helper()
	pid, err := os.ReadFile(filepath.Join(f.local, "wing.pid"))
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(f.local, "wing.args"))
	if err != nil || string(args) != "wing\nstart\n--foreground\n--local-only" {
		t.Fatalf("autostart changed daemon arguments: %q %v", args, err)
	}
	data, err := os.ReadFile(filepath.Join(f.local, "wing.log"))
	if err != nil || strings.Count(string(data), "wing: local control ready") != 1 {
		t.Fatalf("expected exactly one daemon readiness: %v\n%s", err, data)
	}
	return pid
}

func TestNorthstarPTYColdClaude(t *testing.T) {
	buildFixture := northstarNewFixture(t)
	binary := northstarBuild(t, buildFixture, "stable")
	if !strings.HasPrefix(binary, buildFixture.tmp+string(filepath.Separator)) {
		t.Fatal("CLI did not launch through symlinked TMPDIR parent")
	}
	t.Run("cold_warm_detach", func(t *testing.T) {
		coldRequireSandbox(t)
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldRelayGuard(t, f)
		coldCleanup(t, f, binary)
		coldPolicyGuard(t, f)
		// Reserve the full runtime ID width and assert both lexical and
		// canonical socket addresses before the runtime binds any egg.
		reserve := filepath.Join(f.local, "eggs", cmdutil.NewRuntimeID())
		if err := os.MkdirAll(reserve, 0700); err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, filepath.Join(reserve, "egg.sock"))
		if err := os.Remove(reserve); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		h := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "cold-parent")
		screen := h.expect("cold provider reached", 0, "COLD_CLAUDE_READY")
		if strings.Contains(screen, "Starting local Claude...") {
			t.Fatal("startup progress was not cleared before provider output")
		}
		coldTime := time.Since(started)
		t.Logf("cold fixture launch-to-provider: %s (target <=3s)", coldTime)
		pid := coldOneDaemon(t, f)
		ids := coldSessionIDs(t, f)
		if len(ids) != 1 {
			t.Fatalf("cold admission count: %v", ids)
		}
		h.exit("\x02q")
		h.expect("admitted parent detach", 0, "reattach with wt attach "+ids[0])
		reattach := northstarStartPTY(t, f, binary, 100, 24, "attach", ids[0])
		reattach.expect("detached session remains reachable", 0, "COLD_CLAUDE_READY")
		reattach.exit("\x02q")
		var warm []time.Duration
		for i := range 5 {
			started := time.Now()
			h := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", fmt.Sprintf("warm-%d", i))
			h.expect("warm provider reached", 0, "COLD_CLAUDE_READY")
			warm = append(warm, time.Since(started))
			h.exit("\x02q")
			if got := coldOneDaemon(t, f); !bytes.Equal(got, pid) {
				t.Fatal("warm launch replaced the daemon")
			}
		}
		sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
		t.Logf("warm fixture launch-to-provider sample=%v p95=%s (target <=1s)", warm, warm[len(warm)-1])
		if ids := coldSessionIDs(t, f); len(ids) != 6 {
			t.Fatalf("expected one admission per launch: %v", ids)
		}
	})
	t.Run("concurrent_one_daemon_two_sessions", func(t *testing.T) {
		coldRequireSandbox(t)
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldRelayGuard(t, f)
		coldCleanup(t, f, binary)
		coldPolicyGuard(t, f)
		first := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "parallel-a")
		second := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "parallel-b")
		first.expect("first concurrent provider", 0, "COLD_CLAUDE_READY")
		second.expect("second concurrent provider", 0, "COLD_CLAUDE_READY")
		coldOneDaemon(t, f)
		if ids := coldSessionIDs(t, f); len(ids) != 2 {
			t.Fatalf("concurrent admissions: %v", ids)
		}
		first.exit("\x02q")
		second.exit("\x02q")
	})
	t.Run("readiness_failure", func(t *testing.T) {
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldCleanup(t, f, binary)
		// Organization enrollment cannot be served by local-only startup.
		// The real child exits before publishing an authenticated endpoint.
		if err := config.SaveWingConfig(f.local, &config.WingConfig{Org: "fixture-org", HostedRelay: config.HostedRelayDeny}); err != nil {
			t.Fatal(err)
		}
		coldPolicyGuard(t, f)
		h := northstarStartPTY(t, f, binary, 100, 24, "claude")
		h.expect("startup failure is actionable", 0, "exited before readiness", "wing.log")
		coldPTYExit(t, h, true)
		for _, name := range []string{"wing.pid", "wing.args"} {
			if _, err := os.Stat(filepath.Join(f.local, name)); !os.IsNotExist(err) {
				t.Fatalf("exited metadata remains: %s %v", name, err)
			}
		}
	})
	t.Run("named_client_never_autostarts", func(t *testing.T) {
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		f.env = append(f.env, "WT_MCP_CLIENT=denied")
		coldPolicyGuard(t, f)
		h := northstarStartPTY(t, f, binary, 100, 24, "claude")
		h.expect("named client missing wing guidance", 0, "wt wing start --local-only")
		coldPTYExit(t, h, true)
		if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
			t.Fatalf("named client started a daemon: %v", err)
		}
	})
	t.Run("denied_named_client", func(t *testing.T) {
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldCleanup(t, f, binary)
		northstarWrite(t, filepath.Join(f.local, "clients.yaml"), "clients:\n  default:\n    grants: [read, spawn, control]\n", 0600)
		coldPolicyGuard(t, f)
		start := exec.CommandContext(t.Context(), binary, "wing", "start", "--local-only")
		start.Env, start.Dir = f.env, f.work
		if output, err := start.CombinedOutput(); err != nil {
			t.Fatalf("fixture wing: %v %s", err, output)
		}
		pid := coldOneDaemon(t, f)
		f.env = append(f.env, "WT_MCP_CLIENT=denied")
		h := northstarStartPTY(t, f, binary, 100, 24, "claude")
		h.expect("named client policy refusal", 0, "not configured in clients.yaml")
		coldPTYExit(t, h, true)
		if got := coldOneDaemon(t, f); !bytes.Equal(got, pid) {
			t.Fatal("refused launch replaced daemon")
		}
		if entries, _ := os.ReadDir(filepath.Join(f.local, "eggs")); len(entries) != 0 {
			t.Fatal("denied client admitted a session")
		}
	})
	t.Run("cancellation_does_not_cancel_other_caller", func(t *testing.T) {
		coldRequireSandbox(t)
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldCleanup(t, f, binary)
		coldPolicyGuard(t, f)
		lock, err := daemonctl.AcquireDaemonLifecycleLockAt(filepath.Join(f.local, "daemon.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		first := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "cancelled")
		second := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "survivor")
		first.expect("cancelled caller waits with delayed progress", 0, "Starting local Claude...")
		second.expect("other caller waits on same lifecycle", 0, "Starting local Claude...")
		if err := first.cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		first.expect("caller cancellation", 0, "context canceled")
		coldPTYExit(t, first, true)
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		second.expect("surviving caller reaches provider", 0, "COLD_CLAUDE_READY")
		pid := coldOneDaemon(t, f)
		if ids := coldSessionIDs(t, f); len(ids) != 1 {
			t.Fatalf("cancelled waiter admitted work: %v", ids)
		}
		second.exit("\x02q")
		third := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "reuse-after-cancel")
		third.expect("daemon reusable after cancellation", 0, "COLD_CLAUDE_READY")
		third.exit("\x02q")
		if got := coldOneDaemon(t, f); !bytes.Equal(got, pid) {
			t.Fatal("cancellation killed shared daemon")
		}
	})
	t.Run("relative_selected_state", func(t *testing.T) {
		coldRequireSandbox(t)
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldCleanup(t, f, binary)
		coldPolicyGuard(t, f)
		relative, err := filepath.Rel(f.work, f.local)
		if err != nil {
			t.Fatal(err)
		}
		f.env = append(f.env, "WINGTHING_DIR="+relative)
		h := northstarStartPTY(t, f, binary, 100, 24, "claude", "--name", "relative-parent")
		h.expect("relative selector reaches original state", 0, "COLD_CLAUDE_READY")
		h.exit("\x02q")
		coldOneDaemon(t, f)
		if ids := coldSessionIDs(t, f); len(ids) != 1 {
			t.Fatalf("relative state moved: %v", ids)
		}
	})
	t.Run("cold_owner_policy_and_relative_state", func(t *testing.T) {
		f := coldFixture(t)
		binary := coldInstallBinary(t, f, binary)
		coldCleanup(t, f, binary)
		if err := config.SaveWingConfig(f.local, &config.WingConfig{Locked: true, Paths: config.PathList{{Path: f.work}}, HostedRelay: config.HostedRelayDeny}); err != nil {
			t.Fatal(err)
		}
		coldPolicyGuard(t, f)
		relative, err := filepath.Rel(f.work, f.local)
		if err != nil {
			t.Fatal(err)
		}
		f.env = append(f.env, "WINGTHING_DIR="+relative)
		started := time.Now()
		first := northstarStartPTY(t, f, binary, 100, 24, "claude")
		second := northstarStartPTY(t, f, binary, 100, 24, "claude")
		first.expect("cold owner preserves passkey refusal", 0, "passkey authentication is required")
		second.expect("concurrent owner preserves passkey refusal", 0, "passkey authentication is required")
		coldPTYExit(t, first, true)
		coldPTYExit(t, second, true)
		coldOneDaemon(t, f)
		if entries, _ := os.ReadDir(filepath.Join(f.local, "eggs")); len(entries) != 0 {
			t.Fatal("locked policy admitted a session")
		}
		t.Logf("cold concurrent relative-state launch-to-authenticated-refusal: %s", time.Since(started))
	})
}

func coldRequireSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		probe := exec.CommandContext(t.Context(), "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
		if output, err := probe.CombinedOutput(); err != nil {
			t.Fatalf("REQUIRED-UNRUN: native provider sandbox unavailable: %v %s", err, output)
		}
	}
}
