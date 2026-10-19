//go:build isolation && linux

package egg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// These canaries exercise the production preparation and native execution
// boundary, using a disposable HOME and fixture credentials only.
type isolationFixture struct {
	home, work, outside, python string
	config                      *EggConfig
}

func isolationSetup(t *testing.T) isolationFixture {
	t.Helper()
	root := t.TempDir()
	f := isolationFixture{home: filepath.Join(root, "home"), work: filepath.Join(root, "home", "work"), outside: filepath.Join(root, "outside"), config: DefaultEggConfig()}
	var err error
	f.python, err = exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required: ", err)
	}
	for _, dir := range append([]string{f.home, f.work, f.outside}, DefaultCacheDirs()...) {
		if err := os.MkdirAll(expandTilde(dir, f.home), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i, rule := range f.config.FS {
		f.config.FS[i] = strings.ReplaceAll(rule, "./", f.work+string(filepath.Separator))
	}
	// Do not inspect or alter the real user's credential paths.
	for _, path := range DefaultDenyPaths() {
		path = expandTilde(path, f.home)
		if !strings.HasSuffix(path, "history") && !strings.HasSuffix(path, ".netrc") {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(path, "canary")
		}
		isolationWrite(t, path, "fixture-secret")
	}
	isolationWrite(t, filepath.Join(f.work, "egg.yaml"), "fixture-policy")
	return f
}

func isolationWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f isolationFixture) prepare(t *testing.T, agent string) PreparedLaunch {
	t.Helper()
	rc := RunConfig{Agent: agent, Kind: "command", Command: []string{f.python}, CWD: f.work, UserHome: f.home,
		FS: f.config.FS, Network: f.config.Network.Domains, AgentDomains: f.config.Network.AgentDomains,
		CPULimit: f.config.Resources.CPUDuration(), MemLimit: f.config.Resources.MemBytes(), MaxFDs: f.config.Resources.MaxFDs, PidLimit: f.config.Resources.MaxPids}
	inherited := []string{"HOME=" + f.home, "PATH=/usr/local/bin:/usr/bin:/bin", "AWS_SECRET_ACCESS_KEY=fixture-unselected", "OPENAI_API_KEY=fixture-provider", "SSH_AUTH_SOCK=" + filepath.Join(f.home, ".ssh", "agent.sock"), "ISOLATION_ALLOWED=fixture-allowed"}
	p, err := PrepareLaunch(rc, LaunchPreparationOptions{Binary: f.python, Home: f.home, HostHome: f.home, EggDir: filepath.Join(f.home, "session"), EnvironmentConfig: f.config, InheritedEnvironment: inherited, CodexCapability: SavedCodexCapability("")}, NativeLaunchState())
	if err != nil {
		t.Fatal("prepare: ", err)
	}
	if !p.Sandboxed {
		t.Fatal("native sandbox was bypassed")
	}
	p.Sandbox.SessionID = "isolation-" + filepath.Base(filepath.Dir(filepath.Dir(f.home)))
	return p
}

func (f isolationFixture) run(t *testing.T, p PreparedLaunch, script string, args ...string) string {
	t.Helper()
	sb, err := sandbox.New(p.Sandbox)
	if err != nil {
		t.Fatal("native admission (no canary executed): ", err)
	}
	defer func() {
		if err := sb.Destroy(); err != nil {
			t.Error("sandbox cleanup: ", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd, err := sb.Exec(ctx, f.python, append([]string{"-I", "-c", script}, args...))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = f.work
	cmd.Env = nil
	for key, value := range p.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("start: ", err)
	}
	if err := sb.PostStart(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("post-start: ", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("canary did not complete: %v; stderr=%s; stdout=%s", err, &stderr, &stdout)
	}
	return strings.TrimSpace(stdout.String())
}

func isolationResult(t *testing.T, output string, want map[string]bool) {
	t.Helper()
	var got map[string]bool
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("missing canary receipt: %v; %q", err, output)
	}
	if len(got) != len(want) {
		t.Fatalf("receipt fields: %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s=%v, want %v", key, got[key], value)
		}
	}
	t.Log(output)
}

func TestIsolationWriteBoundary(t *testing.T) {
	f := isolationSetup(t)
	output := f.run(t, f.prepare(t, ""), `
import errno,json,os,sys
r={}
for key,path in zip(['workspace','home','outside_home','policy'],sys.argv[1:]):
 try:
  with open(path,'w') as out: out.write('changed')
  r[key]=True
 except OSError as e:
  if e.errno not in (errno.EPERM,errno.EACCES,errno.EROFS): raise
  r[key]=False
print(json.dumps(r))
`, filepath.Join(f.work, "created"), filepath.Join(f.home, "created"), filepath.Join(f.outside, "created"), filepath.Join(f.work, "egg.yaml"))
	isolationResult(t, output, map[string]bool{"workspace": true, "home": false, "outside_home": runtime.GOOS == "linux", "policy": false})
	data, err := os.ReadFile(filepath.Join(f.work, "egg.yaml"))
	if err != nil || string(data) != "fixture-policy" {
		t.Fatalf("policy changed: %v", err)
	}
	t.Log("BASELINE: Linux permits OS-user-writable paths outside HOME; this is not a workspace allowlist")
}

func TestIsolationSecretPaths(t *testing.T) {
	f := isolationSetup(t)
	readable := filepath.Join(f.home, "ordinary")
	isolationWrite(t, readable, "fixture-secret")
	alias := filepath.Join(f.work, "secret-link")
	if err := os.Symlink(filepath.Join(f.home, ".aws", "canary"), alias); err != nil {
		t.Fatal(err)
	}
	paths := []string{readable, alias}
	want := map[string]bool{"0": true, "1": false}
	for _, path := range DefaultDenyPaths() {
		path = expandTilde(path, f.home)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			path = filepath.Join(path, "canary")
		}
		want[fmt.Sprint(len(paths))] = false
		paths = append(paths, path)
	}
	isolationResult(t, f.run(t, f.prepare(t, ""), `
import errno,json,sys
r={}
for i,path in enumerate(sys.argv[1:]):
 try:
  with open(path) as src: r[str(i)]=src.read()=='fixture-secret'
 except OSError as e:
  if e.errno not in (errno.EPERM,errno.EACCES,errno.ENOENT,errno.ENOTDIR): raise
  r[str(i)]=False
print(json.dumps(r))
`, paths...), want)
}

func TestIsolationCredentials(t *testing.T) {
	f := isolationSetup(t)
	f.config.Env = append(f.config.Env, "ISOLATION_ALLOWED")
	f.config.Network = NetworkField{Domains: []string{"*"}, AgentDomains: "none"}
	auth := filepath.Join(f.home, ".codex", "auth.json")
	isolationWrite(t, auth, "fixture-provider-file")
	isolationResult(t, f.run(t, f.prepare(t, "codex"), `
import json,os,sys
with open(sys.argv[1]) as src: readable=src.read()=='fixture-provider-file'
print(json.dumps({'allowed':os.getenv('ISOLATION_ALLOWED')=='fixture-allowed','unselected':bool(os.getenv('AWS_SECRET_ACCESS_KEY')),'ssh_agent':bool(os.getenv('SSH_AUTH_SOCK')),'provider_env':os.getenv('OPENAI_API_KEY')=='fixture-provider','provider_file':readable}))
`, auth), map[string]bool{"allowed": true, "unselected": false, "ssh_agent": false, "provider_env": true, "provider_file": true})
	t.Log("BASELINE: provider profile reinjects its API key and can read its own auth file; credentials are not brokered")
}

func TestIsolationResourceFD(t *testing.T) {
	f := isolationSetup(t)
	f.config.Resources.MaxFDs = 32
	isolationResult(t, f.run(t, f.prepare(t, ""), `
import errno,json,resource
files=[]
try:
 for i in range(128): files.append(open('/dev/null'))
except OSError as e:
 if e.errno!=errno.EMFILE: raise
print(json.dumps({'limit':resource.getrlimit(resource.RLIMIT_NOFILE)==(32,32),'exhausted':len(files)<128}))
`), map[string]bool{"limit": true, "exhausted": true})
}

func TestIsolationResourceCPU(t *testing.T) {
	f := isolationSetup(t)
	f.config.Resources.CPU = "1s"
	isolationResult(t, f.run(t, f.prepare(t, ""), `
import json,resource,subprocess,sys
limit=resource.getrlimit(resource.RLIMIT_CPU)==(1,1)
p=subprocess.run([sys.executable,'-I','-c','while True: pass'],timeout=6)
print(json.dumps({'limit':limit,'killed':p.returncode==-9}))
`), map[string]bool{"limit": true, "killed": true})
}

// Never substitute RLIMIT_AS or RLIMIT_NPROC for memory/PID tree controls.
// A refused host is explicitly unrun, which the strict report rejects.
func TestIsolationResourceMemory(t *testing.T) { isolationCgroup(t, true) }
func TestIsolationResourcePIDs(t *testing.T)   { isolationCgroup(t, false) }
func isolationCgroup(t *testing.T, memory bool) {
	t.Helper()
	f := isolationSetup(t)

	if memory {
		f.config.Resources.Memory = "64MB"
	} else {
		f.config.Resources.MaxPids = 16
	}
	p := f.prepare(t, "")
	sb, err := sandbox.New(p.Sandbox)
	if err != nil {
		var enforcement *sandbox.EnforcementError
		if !errors.As(err, &enforcement) {
			t.Fatal(err)
		}
		t.Skipf("UNRUN: requested resource boundary refused before exec: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatal(err)
	}
	script := `
import json,subprocess,sys
p=subprocess.run([sys.executable,'-I','-c','import time; x=bytearray(160*1024*1024); time.sleep(1)'],timeout=6)
print(json.dumps({'bounded':p.returncode==-9}))
`
	if !memory {
		script = `
import errno,json,subprocess
children=[]
bounded=False
try:
 for i in range(40): children.append(subprocess.Popen(['/bin/sleep','5']))
except OSError as e:
 if e.errno!=errno.EAGAIN: raise
 bounded=True
finally:
 for p in children: p.kill()
 for p in children: p.wait()
print(json.dumps({'bounded':bounded}))
`
	}
	isolationResult(t, f.run(t, p, script), map[string]bool{"bounded": true})
}

func TestIsolationEgress(t *testing.T) {
	f := isolationSetup(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fixture-egress") }))
	defer target.Close()
	address := strings.TrimPrefix(target.URL, "http://")
	// Establish that the destination is live before attempting a denied dial.
	resp, err := http.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "fixture-egress" {
		t.Fatal("host positive control failed")
	}
	direct := `
import json,socket,sys
host,port=sys.argv[1].split(':')
try:
 s=socket.create_connection((host,int(port)),timeout=2); s.close(); connected=True
except OSError: connected=False
print(json.dumps({'connected':connected}))
`
	t.Run("network_none", func(t *testing.T) {
		isolationResult(t, f.run(t, f.prepare(t, ""), direct, address), map[string]bool{"connected": false})
	})
	t.Run("domain_filter_and_bypass", func(t *testing.T) {
		proxy, err := sandbox.StartProxy([]string{"127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		defer proxy.Close()
		f.config.Network = NetworkField{Domains: []string{"127.0.0.1"}}
		p := f.prepare(t, "")
		p.Sandbox.ProxyPort = proxy.Port()
		isolationResult(t, f.run(t, p, `
import json,socket,sys
port=int(sys.argv[1]); address=sys.argv[2]
def tunnel(target):
 s=socket.create_connection(('127.0.0.1',port),timeout=2); s.settimeout(2)
 s.sendall(('CONNECT '+target+' HTTP/1.1\r\nHost: '+target+'\r\n\r\n').encode())
 header=b''
 while b'\r\n\r\n' not in header: header+=s.recv(4096)
 ok=b' 200 ' in header.split(b'\r\n')[0]
 if ok:
  s.sendall(b'GET / HTTP/1.0\r\nHost: fixture\r\n\r\n')
  data=b''
  while True:
   chunk=s.recv(4096)
   if not chunk: break
   data+=chunk
  ok=b'fixture-egress' in data
 s.close()
 return ok,b' 403 ' in header.split(b'\r\n')[0]
allowed,_=tunnel(address)
_,denied=tunnel('blocked.invalid:443')
host,target_port=address.split(':')
try:
 s=socket.create_connection((host,int(target_port)),timeout=2); s.close(); bypass=True
except OSError: bypass=False
print(json.dumps({'allowed':allowed,'denied':denied,'bypass':bypass}))
`, strconv.Itoa(proxy.Port()), address), map[string]bool{"allowed": true, "denied": true, "bypass": false})
		events := proxy.Events()
		if len(events) != 2 || !events[0].Matched || events[0].Blocked || !events[1].Blocked {
			t.Fatalf("wrong proxy audit: %+v", events)
		}
	})
}
