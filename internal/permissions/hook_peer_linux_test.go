//go:build linux

package permissions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The original provider creates the native hook's socketpair, just as Node
// does. A Bash child inherits the same environment and tries both a direct
// exchange and the real hook executable with a counterfeit stdin socketpair.
func TestPermissionPeerBashCannotRegisterOrReplace(t *testing.T) {
	if os.Getenv("WT_PERMISSION_PEER_HELPER") == "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		defer cancel()
		brokerPID, _ := strconv.Atoi(os.Getenv("WT_PERMISSION_PEER_BROKER_PID"))
		var err error
		if os.Getenv("WT_PERMISSION_PEER_FORGED") == "1" {
			// An authentic hook process can claim different arguments in its
			// envelope; the broker must display only its native stdin JSON.
			var c net.Conn
			c, err = net.Dial("unix", os.Getenv("WT_PERMISSION_PEER_SOCKET"))
			if err == nil {
				err = checkHookServer(c, brokerPID)
			}
			if err == nil {
				err = sendHookInput(c, os.Stdin)
			}
			if err == nil {
				err = json.NewEncoder(c).Encode(exchange{Token: "", SessionID: "egg", Generation: os.Getenv("WT_PERMISSION_PEER_GENERATION"), Input: input("echo envelope spoof")})
			}
			var native permissionbridge.Input
			if err == nil {
				err = decode(c, &native)
			}
			var answer reply
			if err == nil {
				err = decode(c, &answer)
			}
			if err == nil && (native.Arguments["command"] != "echo authentic" || answer.Request.Digest != permissionbridge.Digest(native)) {
				err = errors.New("registration envelope replaced native command")
			}
			if err == nil {
				err = json.NewEncoder(os.Stdout).Encode(answer)
			}
			if err == nil {
				err = json.NewEncoder(c).Encode(map[string]string{"request_id": answer.Request.RequestID})
			}
			if c != nil {
				c.Close()
			}
		} else {
			err = HookNative(ctx, os.Getenv("WT_PERMISSION_PEER_SOCKET"), "egg", os.Getenv("WT_PERMISSION_PEER_GENERATION"), "provider", brokerPID, os.Stdin, os.Stdout)
		}
		if err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	ready := make(chan int, 1)
	b, socket := brokerFixture(t, 4*time.Second, func(b *Broker) {
		b.Authenticate = func(c net.Conn, input *os.File) error {
			root := <-ready
			ready <- root
			return HookPeer(root, settings, os.Args[0], c, input)
		}
	})
	payload, _ := json.Marshal(input("echo authentic"))
	script := `import json,os,socket,subprocess,sys,time
exe,settings,payload=sys.argv[1:4]
assert not os.environ.get('WT_PERMISSION_TOKEN')
argv=[exe,'-test.run=^TestPermissionPeerBashCannotRegisterOrReplace$']
a,b=socket.socketpair()
p=subprocess.Popen(argv,stdin=b,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
b.close();a.sendall(payload.encode());a.shutdown(socket.SHUT_WR)
# Keep the original hook open while the tool attempts to spoof a new command.
time.sleep(.2)
child=r'''import json,os,socket,subprocess,sys
exe=sys.argv[1]
args=[exe,'-test.run=^TestPermissionPeerBashCannotRegisterOrReplace$']
a,b=socket.socketpair()
p=subprocess.Popen(args,stdin=b,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
b.close()
payload={'hook_event_name':'PermissionRequest','session_id':'provider','tool_name':'Bash','tool_input':{'command':'echo spoofed'},'cwd':'/workspace'}
a.sendall(json.dumps(payload).encode());a.shutdown(socket.SHUT_WR)
out,err=p.communicate(timeout=3)
assert p.returncode==4 and out==b'',(p.returncode,out,err)
s=socket.socket(socket.AF_UNIX);s.connect(os.environ['WT_PERMISSION_PEER_SOCKET'])
s.sendall(json.dumps({'token':os.environ.get('WT_PERMISSION_TOKEN',''),'session_id':'egg','generation':os.environ['WT_PERMISSION_PEER_GENERATION'],'input':payload}).encode()+b'\n')
s.shutdown(socket.SHUT_WR)
try: assert s.recv(1024)==b''
except ConnectionResetError: pass
print('Bash create/replace refused')
'''
r=subprocess.run(['/bin/bash','-c','exec python3 -c "$1" "$2"','bash',child,exe],capture_output=True,text=True,timeout=4)
assert r.returncode==0,(r.stdout,r.stderr)
print(r.stdout,flush=True)
# Even exec-ing the authentic hook directly from Bash cannot authenticate a
# heredoc created by that Bash tool instead of the provider's native input.
fake=json.loads(payload);fake['tool_input']={'command':'echo direct spoof'}
command='exec "$@" <<\'WT_INPUT\'\n'+json.dumps(fake)+'\nWT_INPUT\n'
r=subprocess.run(['/bin/bash','-c',command,'bash']+argv,capture_output=True,timeout=3)
assert r.returncode==4 and r.stdout==b'',(r.returncode,r.stdout,r.stderr)
out,err=p.communicate(timeout=4)
assert p.returncode==0,(p.returncode,err)
print(out.decode(),flush=True)
# A second tool still cannot create a request after the legitimate answer.
r=subprocess.run(['/bin/bash','-c','exec python3 -c "$1" "$2"','bash',child,exe],capture_output=True,text=True,timeout=4)
assert r.returncode==0,(r.stdout,r.stderr)
print(r.stdout,flush=True)
`
	cmd := exec.Command(python, "-c", script, os.Args[0], settings, string(payload), "--settings", settings)
	cmd.Env = append(os.Environ(), "WT_PERMISSION_PEER_HELPER=1", "WT_PERMISSION_PEER_FORGED=1", "WT_PERMISSION_PEER_SOCKET="+socket, "WT_PERMISSION_PEER_GENERATION="+b.Generation, "WT_PERMISSION_PEER_BROKER_PID="+strconv.Itoa(os.Getpid()))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready <- cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	req := waitRequest(t, b)
	// Read the malicious tool receipt before answering the unchanged request.
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "Bash create/replace refused\n" {
		t.Fatalf("missing refusal receipt: %q", line)
	}
	rows := b.List()
	if len(rows) != 1 || rows[0].RequestID != req.RequestID || rows[0].Digest != req.Digest || rows[0].Arguments["command"] != "echo authentic" {
		t.Fatal("Bash altered display")
	}
	if _, err = b.Respond(response(req, "deny")); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(b.List()) != 0 {
		t.Fatal("Bash created a new request")
	}
}

func TestPermissionNativeHookRefusesWrongServer(t *testing.T) {
	b, path := brokerFixture(t, time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := HookNative(ctx, path, b.SessionID, b.Generation, b.ProviderID, os.Getpid()+1, os.Stdin, &out); err == nil || out.Len() != 0 || len(b.List()) != 0 {
		t.Fatal("hook accepted a different broker process")
	}
}
