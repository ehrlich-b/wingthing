package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
	"google.golang.org/grpc"
)

// This checkout has room for the carrier's short HOME, but not an extra
// state child plus a maximum runtime ID. State directly under protected HOME
// retains both lexical/canonical socket bounds without changing 01's helper.
func northstarNativeMouseFixture(t *testing.T) *northstarFixture {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(repo, ".scratch")
	home := testprovider.ShortStateDir(t, scratch)
	root, err := os.MkdirTemp(scratch, "native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &northstarFixture{repo: repo, root: root, home: home, local: home, remote: home, work: filepath.Join(root, "work"), tmp: filepath.Join(root, "tmp-alias"), bin: filepath.Join(root, "tmp-alias", "bin"), evidence: filepath.Join(scratch, "northstar", "northstar-evidence")}
	real := filepath.Join(root, "tmp-real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.tmp); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.work, f.bin, filepath.Join(home, "mailbox"), f.evidence} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = []string{"HOME=" + home, "WINGTHING_DIR=" + home, "PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + f.tmp, "TERM=xterm-256color", "LANG=en_US.UTF-8", "PYTHONDONTWRITEBYTECODE=1"}
	return f
}

type nativeMouseCapture struct {
	mu      sync.Mutex
	data    bytes.Buffer
	changed chan struct{}
}

func newNativeMouseCapture() *nativeMouseCapture {
	return &nativeMouseCapture{changed: make(chan struct{}, 1)}
}
func (c *nativeMouseCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.data.Write(p)
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
	return n, err
}
func (c *nativeMouseCapture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data.Bytes()...)
}
func (c *nativeMouseCapture) wait(t *testing.T, needle string) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		if bytes.Contains(c.bytes(), []byte(needle)) {
			return
		}
		select {
		case <-c.changed:
		case <-timer.C:
			t.Fatalf("output timeout waiting for %q: %q", needle, c.bytes())
		}
	}
}
func (h *northstarPTY) waitNativeBytes(needle string) []byte {
	h.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		h.mu.Lock()
		data := append([]byte(nil), h.transcript.Bytes()...)
		h.mu.Unlock()
		if bytes.Contains(data, []byte(needle)) {
			return data
		}
		select {
		case <-h.changed:
		case <-timer.C:
			h.t.Fatalf("native PTY bytes timeout for %q: %q", needle, data)
		}
	}
}

func (h *northstarPTY) waitNativePartial(needle string, after uint64) {
	h.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		h.mu.Lock()
		match := h.revision > after && bytes.HasSuffix(h.transcript.Bytes(), []byte(needle))
		h.mu.Unlock()
		if match {
			return
		}
		select {
		case <-h.changed:
		case <-timer.C:
			h.t.Fatalf("partial escape %q was not delivered in a separate PTY read", needle)
		}
	}
}

const nativeMouseProvider = `import os, sys, tty, fcntl, termios, struct, signal
# No provider account or network. Read/write only this fixture's PTY.
tty.setraw(0)
def emit(s): os.write(1, s.encode())
def size(*unused):
    rows, cols, _, _ = struct.unpack('HHHH', fcntl.ioctl(0, termios.TIOCGWINSZ, b'\0'*8))
    emit('SIZE:%dx%d\r\n' % (cols, rows))
signal.signal(signal.SIGWINCH, size)
try:
    with open(os.path.join(os.environ['HOME'], 'mailbox', 'forbidden'), 'w') as f: f.write('bad')
    emit('PROTECTION_BROKEN\r\n')
except PermissionError: emit('PROTECTED_OK\r\n')
emit('TMP_ALIAS:' + os.environ['TMPDIR'] + '\r\n')
for i in range(40): emit('HISTORY-%02d\r\n' % i)
emit('MOUSE_READY\r\n')
wheel = b''
while True:
    c = os.read(0, 1)
    if not c: break
    if wheel or c == b'\x1b':
        wheel += c
        if c == b'M':
            if wheel == b'\x1b[<64;5;3M': emit('WHEEL_UP_SGR\r\n')
            elif wheel == b'\x1b[<65;5;3M': emit('WHEEL_DOWN_SGR\r\n')
            else: emit('WRONG_WHEEL\r\n')
            wheel = b''
        continue
    if c == b'e': emit('\x1b[?10')
    elif c == b'f': emit('00h\x1b[?1006h\x1b[?1049h\x1b[2J\x1b[HALT_READY\r\n')
    elif c == b'd': emit('\x1b[?100')
    elif c == b'D': emit('0l\x1b[?1006l\x1b[?1049lNORMAL_HISTORY_READY\r\n')
    elif c == b'r': size()
    elif c == b'q': emit('FIXTURE_EXIT\r\n'); break
`

func TestNorthstarPTYNativeMouse(t *testing.T) {
	coldRequireSandbox(t)
	f := northstarNativeMouseFixture(t) // creates a real parent and symlinked TMPDIR
	binary := northstarBuild(t, f, "stable")
	// All actual children inherit GOTMPDIR too; the build helper's clone-local
	// caches and this alias keep fixture work out of ambient temporary roots.
	f.env = append(f.env, "GOTMPDIR="+f.tmp)
	resolved, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || resolved != filepath.Join(f.root, "tmp-real") {
		t.Fatalf("fixture TMPDIR alias: %s %v", resolved, err)
	}
	if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("built CLI did not launch through alias")
	}
	provider := filepath.Join(f.work, "native-mouse.py")
	northstarWrite(t, provider, nativeMouseProvider, 0600)
	dir := filepath.Join(f.local, "eggs", "mouse")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := northstarSocket(t, filepath.Join(dir, "egg.sock"))
	args := []string{"egg", "run", "--session-id", "mouse", "--agent=", "--kind", "command", "--command-arg", "/usr/bin/python3", "--command-arg", provider, "--cwd", f.work, "--network", "none", "--omit-browser-bridge", "--env", "TMPDIR=" + f.tmp, "--env", "PYTHONDONTWRITEBYTECODE=1"}
	// Keep the established policy, including protected HOME and ancestor guards.
	for _, rule := range testprovider.ScopedHomePolicy(f.home).FS {
		args = append(args, "--fs", rule)
	}
	args = append(args, "--fs", "rw:"+f.work, "--fs", "rw:"+f.tmp)
	command := exec.CommandContext(t.Context(), binary, args...)
	command.Env, command.Dir = f.env, f.work
	logs := newNativeMouseCapture()
	command.Stdout, command.Stderr = logs, logs
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				<-done
			}
		}
		northstarWrite(t, filepath.Join(f.evidence, "native-mouse-egg.log"), string(logs.bytes()), 0600)
	})
	logs.wait(t, "serving on ")
	ec, err := egg.Dial(socket, filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	defer ec.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	legacy, err := ec.AttachSessionWithOptions(ctx, "mouse", egg.AttachOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	browser := newNativeMouseCapture()
	readerDone := make(chan error, 1)
	go func() {
		for {
			msg, err := legacy.Recv()
			if err != nil {
				readerDone <- err
				return
			}
			if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
				readerDone <- nil
				return
			}
			_, _ = browser.Write(msg.GetOutput())
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-readerDone
		northstarWrite(t, filepath.Join(f.evidence, "native-mouse-legacy.ansi"), string(browser.bytes()), 0600)
	})
	browser.wait(t, "MOUSE_READY")
	h := northstarStartPTY(t, f, binary, 80, 24, "attach", "mouse")
	initial := h.waitNativeBytes("MOUSE_READY")
	for i := range 40 {
		if !bytes.Contains(initial, []byte(fmt.Sprintf("HISTORY-%02d", i))) {
			t.Fatalf("raw history line %d disappeared", i)
		}
	}
	h.send("e")
	h.waitNativeBytes("\x1b[?10") // provider waits for f: two different kernel PTY reads
	h.expect("split enable / alternate screen", h.send("f"), "ALT_READY")
	native := h.waitNativeBytes("\x1b[?1000h\x1b[?1006h")
	if !bytes.Contains(native, []byte("\x1b[?1049h")) {
		t.Fatal("native alternate screen missing")
	}
	browser.wait(t, "ALT_READY")
	if bytes.Contains(browser.bytes(), []byte("\x1b[?1000h")) || bytes.Contains(browser.bytes(), []byte("\x1b[?1006h")) || !bytes.Contains(browser.bytes(), []byte("\x1b]7771;1\a")) {
		t.Fatalf("browser mouse contract changed: %q", browser.bytes())
	}
	h.expect("native wheel up reaches provider", h.send("\x1b[<64;5;3M"), "WHEEL_UP_SGR")
	h.expect("native wheel down reaches provider", h.send("\x1b[<65;5;3M"), "WHEEL_DOWN_SGR")
	browser.wait(t, "WHEEL_DOWN_SGR")
	h.expect("provider resize", h.resize(40, 12), "SIZE:40x12")
	h.exit("\x02q")
	// Reconnect while enabled after resizing: native mode and alternate grid survive.
	h2 := northstarStartPTY(t, f, binary, 40, 12, "attach", "mouse")
	h2.expect("reconnect enabled / alternate grid", 0, "WHEEL_DOWN_SGR", "SIZE:40x12")
	h2.waitNativeBytes("\x1b[?1000h\x1b[?1006h")
	rev := h2.send("d")
	h2.waitNativePartial("\x1b[?100", rev)
	h2.expect("split disable / normal screen", h2.send("D"), "NORMAL_HISTORY_READY")
	browser.wait(t, "NORMAL_HISTORY_READY")
	h2.exit("\x02q")
	// Old enables in replay must not win over the later split disable.
	h3 := northstarStartPTY(t, f, binary, 40, 12, "attach", "mouse")
	h3.expect("reconnect disabled / normal history", 0, "NORMAL_HISTORY_READY")
	snapshot := h3.waitNativeBytes("NORMAL_HISTORY_READY")
	for _, mode := range []string{"1000", "1006", "1049"} {
		enabled := bytes.LastIndex(snapshot, []byte("\x1b[?"+mode+"h"))
		disabled := bytes.LastIndex(snapshot, []byte("\x1b[?"+mode+"l"))
		if enabled < 0 || disabled < enabled {
			t.Fatalf("reconnect restored stale mode %s: %q", mode, snapshot)
		}
	}
	for i := range 40 {
		if !bytes.Contains(snapshot, []byte(fmt.Sprintf("HISTORY-%02d", i))) {
			t.Fatalf("reconnect lost normal history %d", i)
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "mailbox", "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("provider wrote protected mailbox: %v", err)
	}
	if !bytes.Contains(initial, []byte("PROTECTED_OK")) || !bytes.Contains(initial, []byte("TMP_ALIAS:"+f.tmp)) {
		t.Fatal("provider boundary or TMPDIR alias was not exercised")
	}
	h3.exit("\x02q")
	// Close the observer RPC before graceful shutdown, then stop only the
	// owned fake-provider session and join it.
	cancel()
	if err := ec.Kill(t.Context(), "mouse"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned egg did not join")
	}
	t.Log("kernel PTY bytes and SGR wheel input passed; physical trackpad/selection modifier remains a GUI canary")
}

func TestNativeTerminalControlNegotiatesOldAndNewPeers(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			f := northstarNativeMouseFixture(t)
			path := northstarSocket(t, filepath.Join(f.local, "control.sock"))
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			errors := make(chan error, 1)
			observed := make(chan bool, 1)
			go func() {
				for connection := 0; connection < 2; connection++ {
					conn, err := listener.Accept()
					if err != nil {
						errors <- err
						return
					}
					reader := bufio.NewReader(conn)
					line, err := reader.ReadBytes('\n')
					if err != nil {
						conn.Close()
						errors <- err
						return
					}
					var hello map[string]json.RawMessage
					if err = json.Unmarshal(line, &hello); err != nil {
						conn.Close()
						errors <- err
						return
					}
					welcome := map[string]any{"version": control.ContractVersion, "wing_id": "fixture", "grants": map[string]bool{}}
					if connection == 0 {
						if _, ok := hello["attach"]; ok {
							conn.Close()
							errors <- fmt.Errorf("capability request was an attachment")
							return
						}
						if capable {
							welcome["capabilities"] = []string{controlsocket.NativeTerminalCapability}
						}
					} else {
						var attachment map[string]json.RawMessage
						if err = json.Unmarshal(hello["attach"], &attachment); err != nil {
							conn.Close()
							errors <- err
							return
						}
						option, present := attachment["native_terminal"]
						if capable && string(option) != "true" || !capable && present {
							conn.Close()
							errors <- fmt.Errorf("unnegotiated native option: %s", hello["attach"])
							return
						}
						observed <- present
					}
					if err = json.NewEncoder(conn).Encode(welcome); err != nil {
						conn.Close()
						errors <- err
						return
					}
					if connection == 0 {
						_, _ = io.Copy(io.Discard, reader)
					} else {
						_ = json.NewEncoder(conn).Encode(map[string]bool{"end": true})
					}
					conn.Close()
				}
				errors <- nil
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream, welcome, err := controlsocket.DialAttachmentPath(ctx, path, controlsocket.Hello{Attach: &controlsocket.Attachment{Session: "mouse", NativeTerminal: true}})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if len(welcome.Capabilities) != 0 {
				t.Fatal("attachment welcome carried capabilities")
			}
			if <-observed != capable {
				t.Fatal("peer capability not honored")
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("attachment stream end: %v", err)
			}
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeTerminalControlKeepsLegacyWelcome(t *testing.T) {
	f := northstarNativeMouseFixture(t)
	path := northstarSocket(t, filepath.Join(f.local, "control.sock"))
	seen := make(chan bool, 2)
	server, err := controlsocket.Listen(t.Context(), f.local, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(context.Context, control.DirectRequest) control.DirectResponse { return control.DirectResponse{} }, nil
	}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		seen <- hello.Attach.NativeTerminal
		return controlsocket.Welcome{Capabilities: []string{controlsocket.NativeTerminalCapability}}, func(context.Context, *controlsocket.SessionStream) error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := controlsocket.DialPath(t.Context(), path, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.Welcome.Capabilities) != 1 || client.Welcome.Capabilities[0] != controlsocket.NativeTerminalCapability {
		t.Fatal("normal welcome did not advertise native presentation")
	}
	client.Close()
	// Decode precisely the old client's closed welcome envelope.
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(controlsocket.Hello{Version: control.ContractVersion, Attach: &controlsocket.Attachment{Session: "mouse"}}); err != nil {
		t.Fatal(err)
	}
	var oldWelcome struct {
		Version       string          `json:"version"`
		WingID        string          `json:"wing_id"`
		Principal     string          `json:"principal"`
		Actor         string          `json:"actor"`
		Grants        map[string]bool `json:"grants"`
		Tools         map[string]bool `json:"tools"`
		Error         string          `json:"error"`
		Isolation     string          `json:"isolation"`
		RemoteAllowed bool            `json:"remote_allowed"`
	}
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&oldWelcome); err != nil {
		t.Fatalf("new server broke strict legacy attachment welcome: %v", err)
	}
	if <-seen {
		t.Fatal("old client selected native presentation")
	}
	stream, _, err := controlsocket.DialAttachmentPath(t.Context(), path, controlsocket.Hello{Attach: &controlsocket.Attachment{Session: "mouse", NativeTerminal: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !<-seen {
		t.Fatal("new client did not select negotiated native presentation")
	}
}

// These transport fixtures test native CLI and wing negotiation even on hosts
// without a usable provider sandbox. They are separate from the required real
// provider/kernel-PTY journey above and cannot substitute for that gate.
type nativeMousePeer struct {
	pb.UnimplementedEggServer
	capable bool
	options chan bool
}

func (p *nativeMousePeer) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{NativeTerminal: p.capable, Agent: "claude"}, nil
}
func (p *nativeMousePeer) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	native := first.AttachOptions.GetNativeTerminal()
	p.options <- native
	output := []byte("\x1b[?1000h\x1b[?1006h\x1b[?1049hPEER_READY\r\n")
	if !native {
		output = []byte("\x1b]7771;1\a\x1b[?1049hPEER_READY\r\n")
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: output}}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.GetDetach() {
			return nil
		}
		if len(msg.GetInput()) > 0 {
			if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("PEER_INPUT:" + fmt.Sprintf("%x", msg.GetInput()) + "\r\n")}}); err != nil {
				return err
			}
		}
	}
}
func northstarNativeProtocolPeer(t *testing.T, f *northstarFixture, capable bool) *nativeMousePeer {
	t.Helper()
	dir := filepath.Join(f.local, "eggs", "peer")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity := exec.CommandContext(t.Context(), exe, "egg", "run", "--session-id", "peer")
	identity.Env = append(f.env, "WT_NORTHSTAR_PID_FIXTURE=1")
	input, err := identity.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := identity.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := identity.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- identity.Wait() }()
	t.Cleanup(func() {
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = identity.Process.Kill()
			<-done
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatal("identity did not start")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("identity timeout")
	}
	for file, value := range map[string]string{"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "native-fixture", "egg.meta": "kind=agent\nagent=claude\ncwd=" + f.work + "\nisolation=fixture\n"} {
		northstarWrite(t, filepath.Join(dir, file), value, 0600)
	}
	socket := northstarSocket(t, filepath.Join(dir, "egg.sock"))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	peer := &nativeMousePeer{capable: capable, options: make(chan bool, 4)}
	rpc := grpc.NewServer()
	pb.RegisterEggServer(rpc, peer)
	served := make(chan struct{})
	go func() { defer close(served); _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-served })
	return peer
}

func TestNativeTerminalCLINegotiatesFixturePeers(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			f := northstarNativeMouseFixture(t)
			binary := northstarBuild(t, f, "stable")
			peer := northstarNativeProtocolPeer(t, f, capable)
			h := northstarStartPTY(t, f, binary, 80, 24, "attach", "peer")
			output := h.waitNativeBytes("PEER_READY")
			select {
			case option := <-peer.options:
				if option != capable {
					t.Fatalf("native CLI option=%t capable=%t", option, capable)
				}
			case <-time.After(time.Second):
				t.Fatal("CLI did not attach")
			}
			if bytes.Contains(output, []byte("\x1b[?1000h")) != capable {
				t.Fatalf("CLI presentation did not honor capability: %q", output)
			}
			h.expect("SGR input survives CLI", h.send("\x1b[<64;5;3M"), "PEER_INPUT:1b5b3c36343b353b334d")
			h.exit("\x02q")
		})
	}
}

func TestNativeTerminalWingAdapterNegotiatesEggSupport(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			f := northstarNativeMouseFixture(t)
			peer := northstarNativeProtocolPeer(t, f, capable)
			cfg := &config.Config{Dir: f.local, WingID: "native-fixture"}
			wing := &config.WingConfig{WingID: cfg.WingID}
			service := &wingsession.Service{Config: cfg, Home: f.home, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wing, Egg: egg.DefaultEggConfig()} }}
			northstarSocket(t, filepath.Join(f.local, "control.sock"))
			server, err := localmcp.ListenLocalWingControl(t.Context(), "test", service, "owner", localmcp.NewMCPAdmissionState())
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			// Explicit request from this presentation fixture; stdin is a pipe here.
			var output bytes.Buffer
			detached, err := localmcp.AttachWingIO(ctx, f.local, "", "peer", strings.NewReader("\x02q"), &output, egg.AttachOptions{NativeTerminal: true})
			if err != nil || !detached {
				t.Fatalf("wing attachment: detached=%t err=%v", detached, err)
			}
			select {
			case option := <-peer.options:
				if option != capable {
					t.Fatalf("wing forwarded native option without egg support: %t", option)
				}
			case <-time.After(time.Second):
				t.Fatal("wing did not attach")
			}
			if bytes.Contains(output.Bytes(), []byte("\x1b[?1000h")) != capable {
				t.Fatalf("wing presentation: %q", output.Bytes())
			}
		})
	}
}
