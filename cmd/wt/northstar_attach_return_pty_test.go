package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The provider and SSH are isolated fixtures; every attach and navigator
// handoff executes the actual built CLI on a kernel PTY.
type northstarReturnEgg struct {
	pb.UnimplementedEggServer
	dir, marker string
	attached    chan *pb.AttachOptions
	inputs      chan []byte
	resizes     chan *pb.Resize
	connections atomic.Int32
}

func (f *northstarReturnEgg) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Agent: "claude", Readers: 1, NativeTerminal: true, WriterId: "fixture-writer", WriterOwner: "browser", InputEpoch: 7}, nil
}

const northstarReturnModes = "\x1b[?1h\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H"

func (f *northstarReturnEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.SessionId != filepath.Base(f.dir) {
		return status.Error(codes.NotFound, "wrong exact fixture session")
	}
	f.connections.Add(1)
	f.attached <- first.AttachOptions
	emit := func(text string) error {
		return stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte(text)}})
	}
	if err := emit(northstarReturnModes + f.marker + "\r\n"); err != nil {
		return err
	}
	var input []byte
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.GetDetach() {
			return nil
		}
		if size := msg.GetResize(); size != nil {
			if first.AttachOptions.GetReadOnly() {
				return status.Error(codes.PermissionDenied, "read-only resized")
			}
			select {
			case f.resizes <- size:
			default:
			}
			if err := emit(fmt.Sprintf("RETURN_SIZE:%dx%d\r\n", size.Cols, size.Rows)); err != nil {
				return err
			}
		}
		data := msg.GetInput()
		if len(data) == 0 {
			continue
		}
		if first.AttachOptions.GetReadOnly() {
			return status.Error(codes.PermissionDenied, "read-only sent input")
		}
		select {
		case f.inputs <- bytes.Clone(data):
		default:
		}
		input = append(input, data...)
		if bytes.Contains(input, []byte("\x1b[201~")) {
			if err := emit("PASTE_HEX:" + hex.EncodeToString(input) + "\r\n"); err != nil {
				return err
			}
			input = nil
		}
		if len(data) == 1 {
			switch data[0] {
			case 'E':
				return nil // EOF does not certify provider completion.
			case 'F':
				return status.Error(codes.Unavailable, "fixture input transport failed")
			case 'N':
				if err := emit("PROVIDER_NONZERO\r\n"); err != nil {
					return err
				}
				return stream.Send(&pb.SessionMsg{Payload: &pb.SessionMsg_ExitCode{ExitCode: 17}})
			case 'V':
				return stream.Send(&pb.SessionMsg{Payload: &pb.SessionMsg_ExitCode{ExitCode: 255}})
			case 'X':
				if err := os.Remove(filepath.Join(f.dir, "egg.pid")); err != nil {
					return err
				}
				return stream.Send(&pb.SessionMsg{Payload: &pb.SessionMsg_ExitCode{ExitCode: 0}})
			case 'P':
				if err := emit("\x1b[?100"); err != nil {
					return err
				}
			}
		}
	}
}

func northstarReturnSession(t *testing.T, f *northstarFixture, state, id, name, marker string) *northstarReturnEgg {
	t.Helper()
	dir := filepath.Join(state, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity := exec.CommandContext(t.Context(), exe, "egg", "run", "--session-id", id)
	identity.Env = append(f.env, "WT_NORTHSTAR_PID_FIXTURE=1")
	input, err := identity.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	ready, err := identity.StdoutPipe()
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
	ack := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(ready).ReadString('\n'); ack <- line }()
	select {
	case line := <-ack:
		if line != "ready\n" {
			t.Fatalf("PID fixture readiness: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PID fixture not ready")
	}
	for name, value := range map[string]string{
		"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "return-fixture-token",
		"egg.meta":        "kind=agent\nagent=claude\ncwd=" + f.work + "\nisolation=fixture\n",
		"lifecycle.jsonl": "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"working\"}\n",
	} {
		northstarWrite(t, filepath.Join(dir, name), value, 0600)
	}
	if err := eggclient.WriteSessionName(dir, name); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", northstarSocket(t, filepath.Join(dir, "egg.sock")))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &northstarReturnEgg{dir: dir, marker: marker, attached: make(chan *pb.AttachOptions, 32), inputs: make(chan []byte, 64), resizes: make(chan *pb.Resize, 64)}
	rpc := grpc.NewServer()
	pb.RegisterEggServer(rpc, fixture)
	served := make(chan struct{})
	go func() { defer close(served); _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-served })
	return fixture
}

func northstarReturnWait(t *testing.T, h *northstarPTY, wantFailure bool) {
	t.Helper()
	select {
	case err := <-h.done:
		if (err != nil) != wantFailure {
			t.Fatalf("PTY exit=%v expected failure=%v", err, wantFailure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PTY child failed to return")
	}
	state, err := term.GetState(int(h.master.Fd()))
	if err != nil || !reflect.DeepEqual(state, h.before) {
		t.Fatalf("original termios not restored: before=%+v after=%+v err=%v", h.before, state, err)
	}
	// Wait on an explicit cleanup sequence, not process exit alone: the PTY
	// output reader may still be draining the child's final writes.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		h.mu.Lock()
		data := bytes.Clone(h.transcript.Bytes())
		h.mu.Unlock()
		reset := bytes.LastIndex(data, egg.NativeTerminalReset())
		clean := reset >= 0
		for _, mode := range []string{"1", "1000", "1006", "2004", "1049"} {
			// The startup reset also matches. Require the cleanup reset after
			// every provider enable, while the reader drains the final writes.
			clean = clean && reset > bytes.LastIndex(data, []byte("\x1b[?"+mode+"h"))
		}
		if clean {
			return
		}
		select {
		case <-h.changed:
		case <-timer.C:
			t.Fatalf("cleanup reset did not follow provider modes: %q", data)
		}
	}
}

func northstarReturnAttached(t *testing.T, f *northstarReturnEgg) *pb.AttachOptions {
	t.Helper()
	select {
	case options := <-f.attached:
		return options
	case <-time.After(3 * time.Second):
		t.Fatal("attachment did not reach exact fixture")
	}
	return nil
}

func TestNorthstarPTYAttachReturn(t *testing.T) {
	started := time.Now()
	f := northstarNewFixture(t) // real TMPDIR parent and symlink created inside this test
	f.evidence = filepath.Join(f.repo, ".scratch", "evidence", "pty")
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "GOTMPDIR="+f.tmp)
	canonical, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || canonical != filepath.Join(f.root, "tmp-real") {
		t.Fatalf("TMPDIR alias: %s %v", canonical, err)
	}
	binary := northstarBuild(t, f, "stable")
	if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("CLI did not launch through symlinked TMPDIR parent")
	}
	northstarReturnReceipt(t, f, binary, canonical, started)
	custom := filepath.Join(f.bin, "custom return wt")
	if err := os.Symlink(binary, custom); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRemotes(f.local, map[string]config.Remote{"forge": {SSHTarget: "ns-forge", WTBinary: custom, WingthingDir: f.remote}}); err != nil {
		t.Fatal(err)
	}
	sshLog := filepath.Join(f.home, "return-ssh.jsonl")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "NORTHSTAR_REMOTE_SSH_LOG="+sshLog)
	northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nexec "+remotepkg.ShellQuote(exe)+" northstar-fake-ssh \"$@\"\n", 0700)
	t.Cleanup(func() {
		if data, err := os.ReadFile(sshLog); err == nil {
			northstarWrite(t, filepath.Join(f.evidence, "attach-return-ssh.jsonl"), string(data), 0600)
		}
	})
	local := northstarReturnSession(t, f, f.local, "same", "local-return", "LOCAL_RETURN_READY")
	remote := northstarReturnSession(t, f, f.remote, "same", "remote-return", "REMOTE_RETURN_READY")

	t.Run("split-prefix-and-paste", func(t *testing.T) {
		h := northstarStartPTY(t, f, binary, 100, 24, "attach", "same")
		h.expect("local attach ready", 0, "LOCAL_RETURN_READY")
		northstarReturnAttached(t, local)
		h.waitNativeBytes("Ctrl+B then q detaches")
		// Cross kernel writes inside both paste delimiters and both chords.
		paste := []string{"\x1b[2", "00~雪🙂", "\x02", "q\x02Q\x02\x02tail", "\x1b[20", "1~"}
		for _, chunk := range paste {
			h.send(chunk)
		}
		want := strings.Join(paste, "")
		h.waitNativeBytes("PASTE_HEX:" + hex.EncodeToString([]byte(want)))
		rev := h.send("P")
		h.waitNativePartial("\x1b[?100", rev)
		h.send("\x02")
		h.send("q")
		northstarReturnWait(t, h, false)
	})

	for _, target := range []struct {
		name, ref, marker string
		fixture           *northstarReturnEgg
	}{
		{"local", "same", "LOCAL_RETURN_READY", local}, {"remote", "forge:same", "REMOTE_RETURN_READY", remote},
	} {
		t.Run(target.name+"-eof", func(t *testing.T) {
			h := northstarStartPTY(t, f, binary, 120, 30, "attach", target.ref)
			h.expect("attach ready", 0, target.marker)
			northstarReturnAttached(t, target.fixture)
			h.send("E")
			northstarReturnWait(t, h, true)
			h.waitNativeBytes("Reconnect:")
			if target.name == "remote" {
				h.waitNativeBytes("'attach' '--' 'forge:same'")
				for _, want := range []string{"forge (ns-forge)", "'--remote-binary' " + remotepkg.ShellQuote(custom), "'--remote-state' " + remotepkg.ShellQuote(f.remote)} {
					h.waitNativeBytes(want)
				}
			} else {
				reconnectBinary := binary
				// Linux os.Executable resolves the alias through /proc/self/exe.
				// Still require this exact fixture binary and qualified session.
				if runtime.GOOS == "linux" {
					var err error
					reconnectBinary, err = filepath.EvalSymlinks(binary)
					if err != nil {
						t.Fatal(err)
					}
				}
				h.waitNativeBytes(remotepkg.ShellQuote(reconnectBinary) + " 'attach' '--' 'same'")
			}
		})
		t.Run(target.name+"-sigterm", func(t *testing.T) {
			h := northstarStartPTY(t, f, binary, 100, 24, "attach", target.ref)
			h.expect("attach before SIGTERM", 0, target.marker)
			northstarReturnAttached(t, target.fixture)
			if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			northstarReturnWait(t, h, true)
		})
	}

	t.Run("sigwinch-burst-and-read-only", func(t *testing.T) {
		h := northstarStartPTY(t, f, binary, 100, 24, "attach", "same")
		h.expect("resize attachment ready", 0, "LOCAL_RETURN_READY")
		northstarReturnAttached(t, local)
		for i := range 20 {
			h.resize(80+i, 24+i%3)
			h.send("z")
		}
		h.expect("last burst size acknowledged", h.resize(111, 31), "RETURN_SIZE:111x31")
		h.send("\x02q")
		northstarReturnWait(t, h, false)
		for len(local.resizes) > 0 {
			<-local.resizes
		}
		for len(local.inputs) > 0 {
			<-local.inputs
		}
		observer := northstarStartPTY(t, f, binary, 100, 24, "attach", "--read-only", "same")
		observer.expect("observer ready", 0, "LOCAL_RETURN_READY")
		options := northstarReturnAttached(t, local)
		if !options.ReadOnly || options.Rows != 0 || options.Cols != 0 || options.Claim {
			t.Fatalf("observer requested input/resize: %+v", options)
		}
		observer.resize(72, 18)
		observer.send("uncertain\x1b[200~\x02q\x1b[201~")
		observer.send("\x02q")
		northstarReturnWait(t, observer, false)
		if len(local.resizes) != 0 || len(local.inputs) != 0 {
			t.Fatal("read-only attachment wrote input or resized")
		}
	})

	t.Run("nonzero-and-input-failure", func(t *testing.T) {
		for _, test := range []struct {
			key, want string
			code      int
		}{{"N", "session exited with code 17", 1}, {"V", "session exited with code 255", 1}, {"F", "session outcome unknown", 255}} {
			h := northstarStartPTY(t, f, binary, 100, 24, "attach", "same")
			h.expect("provider ready", 0, "LOCAL_RETURN_READY")
			northstarReturnAttached(t, local)
			h.send(test.key)
			northstarReturnWait(t, h, true)
			h.waitNativeBytes(test.want)
			if h.cmd.ProcessState.ExitCode() != test.code {
				t.Fatalf("provider/transport exit confused: %v", h.cmd.ProcessState)
			}
			h.mu.Lock()
			if test.code == 1 && bytes.Contains(h.transcript.Bytes(), []byte("Reconnect:")) {
				t.Error("confirmed provider exit reported as a disconnect")
			}
			h.mu.Unlock()
		}
	})

	t.Run("navigator-return-selection-filter-and-removal", func(t *testing.T) {
		h := northstarStartPTY(t, f, binary, 120, 30)
		h.expect("initial navigator", 0, "local return", "remote return")
		h.expect("keep qualified filter", h.send("/remote-return\r"), "> remote return")
		h.expect("paste cannot run actions", h.send("\x1b[200~q\rxyvt\x03\x1b[201~?"), "Help")
		h.expect("new navigation key after paste", h.send("\x1b"), "remote return", "Enter open")
		h.expect("navigator hands off", h.send("\r"), "REMOTE_RETURN_READY")
		northstarReturnAttached(t, remote)
		h.expect("EOF returns to previous filter and selection", h.send("E"), "remote return", "Enter open", "Enter retries")
		connections := remote.connections.Load()
		h.expect("old attach reader cannot steal new key", h.send("?"), "Help")
		if remote.connections.Load() != connections {
			t.Fatal("navigator automatically relaunched attachment")
		}
		h.expect("close help", h.send("\x1b"), "remote return", "Enter open")
		h.expect("user retry resolves remote afresh", h.send("\r"), "REMOTE_RETURN_READY")
		northstarReturnAttached(t, remote)
		h.expect("provider removal returns safely", h.send("X"), "No matching sessions", "Enter open")
		h.expect("navigation after selected-session removal", h.send("?"), "Help")
		h.expect("return after removal", h.send("\x1b"), "No matching sessions")
		h.expect("clear retained filter", h.send("/\x15\r"), "local return")
		h.exit("q")
	})

	t.Run("preview-view-and-confirmed-takeover", func(t *testing.T) {
		northstarReturnNativeProbe(t, f)
		preview := northstarNewFixture(t)
		preview.evidence = f.evidence
		preview.env = append(preview.env, "WINGTHING_PREVIEW_DIR="+preview.local, "GOTMPDIR="+preview.tmp)
		previewBinary := northstarBuild(t, preview, "preview")
		if err := os.Remove(filepath.Join(preview.local, "remotes.yaml")); err != nil {
			t.Fatal(err)
		}
		// Adopt empty preview state through the CLI's usual config load.
		initialize := exec.CommandContext(t.Context(), previewBinary, "_remote", "enter", "--json")
		initialize.Env, initialize.Dir = preview.env, preview.work
		if output, err := initialize.CombinedOutput(); err != nil {
			t.Fatalf("initialize preview fixture: %v: %s", err, output)
		}
		dir := northstarReturnSpawnNative(t, preview, previewBinary, "lease")
		if err := eggclient.WriteSessionName(dir, "preview-return"); err != nil {
			t.Fatal(err)
		}
		ec, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ec.Close() })
		ownerCtx, cancelOwner := context.WithCancel(t.Context())
		owner, err := ec.AttachSessionWithOptions(ownerCtx, "lease", egg.AttachOptions{Claim: true, Owner: "browser", NativeTerminal: true})
		if err != nil {
			cancelOwner()
			t.Fatal(err)
		}
		ownerDone := make(chan struct{})
		go func() {
			defer close(ownerDone)
			for {
				if _, err := owner.Recv(); err != nil {
					return
				}
			}
		}()
		t.Cleanup(func() { cancelOwner(); <-ownerDone })
		before, err := ec.Status(t.Context())
		if err != nil || before.WriterOwner != "browser" || before.InputEpoch == 0 {
			t.Fatalf("native ownership unavailable: %+v %v", before, err)
		}
		h := northstarStartPTY(t, preview, previewBinary, 120, 30)
		h.expect("preview ownership inventory", 0, "preview return", "Enter open")
		h.expect("v observes", h.send("v"), "NATIVE_RETURN_READY", "PROTECTED_OK")
		h.resize(90, 25)
		h.expect("observer returns", h.send("\x02q"), "preview return", "Enter open")
		after, err := ec.Status(t.Context())
		if err != nil || after.WriterId != before.WriterId || after.InputEpoch != before.InputEpoch {
			t.Fatalf("observer changed ownership: %+v %v", after, err)
		}
		h.expect("takeover requires confirmation", h.send("t"), "Current input owner: browser")
		h.expect("Enter cancels takeover", h.send("\r"), "preview return", "Enter open")
		// A real wing handshake refusal must never fall back to the egg.
		path, err := controlsocket.Path(preview.local)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, path)
		denied := atomic.Int32{}
		wing, err := controlsocket.Listen(t.Context(), preview.local, "return-policy", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			return controlsocket.Welcome{}, nil, nil
		}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
			if hello.Attach != nil && hello.Attach.Takeover {
				denied.Add(1)
			}
			return controlsocket.Welcome{}, nil, errors.New("fixture takeover denied")
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = wing.Close() })
		h.expect("confirm takeover again", h.send("t"), "Current input owner: browser")
		h.expect("denied takeover returns", h.send("y"), "preview return", "Enter open", "Enter retries")
		h.waitNativeBytes("fixture takeover denied")
		after, err = ec.Status(t.Context())
		if err != nil || denied.Load() != 1 || after.WriterId != before.WriterId {
			t.Fatalf("denied takeover bypassed wing authority: %+v %v", after, err)
		}
		h.expect("new reader after denial", h.send("?"), "Help")
		h.expect("back after denial", h.send("\x1b"), "preview return")
		if err := wing.Close(); err != nil {
			t.Fatal(err)
		}
		h.expect("new explicit confirmation", h.send("t"), "Current input owner: browser")
		h.expect("confirmed takeover attaches", h.send("y"), "NATIVE_RETURN_READY")
		after, err = ec.Status(t.Context())
		if err != nil || after.WriterId == before.WriterId || after.InputEpoch <= before.InputEpoch {
			t.Fatalf("confirmed takeover did not acquire a new lease: %+v %v", after, err)
		}
		h.expect("takeover detach returns", h.send("\x02q"), "preview return", "Enter open")
		h.expect("new reader after takeover", h.send("?"), "Help")
		h.expect("close help", h.send("\x1b"), "preview return")
		h.exit("q")
	})

	t.Run("native-provider-nonzero", func(t *testing.T) { northstarReturnNativeProvider(t, f, binary) })
	t.Logf("attach return fixture matrix duration: %s; evidence: %s", time.Since(started), f.evidence)
}

func northstarReturnReceipt(t *testing.T, f *northstarFixture, binary, canonical string, started time.Time) {
	t.Helper()
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	receipt := map[string]any{"test": t.Name(), "go_version": runtime.Version(), "binary_path": binary, "binary_sha256": hex.EncodeToString(hash.Sum(nil)), "tmp_parent_alias": f.tmp, "tmp_parent_canonical": canonical, "protected_home": f.home, "provider_workspace": f.work, "kernel_pty": true}
	git := exec.Command("git", "rev-parse", "HEAD")
	git.Dir = f.repo
	if head, err := git.Output(); err == nil {
		receipt["source_head"] = strings.TrimSpace(string(head))
	}
	t.Cleanup(func() {
		receipt["failed"] = t.Failed()
		receipt["seconds"] = time.Since(started).Seconds()
		data, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		northstarWrite(t, filepath.Join(f.evidence, "attach-return-receipt.json"), string(data)+"\n", 0600)
	})
}

func northstarReturnNativeProvider(t *testing.T, f *northstarFixture, binary string) {
	t.Helper()
	northstarReturnNativeProbe(t, f)
	northstarReturnSpawnNative(t, f, binary, "native-return")
	h := northstarStartPTY(t, f, binary, 100, 24, "attach", "native-return")
	h.expect("native provider and protected HOME ready", 0, "NATIVE_RETURN_READY", "PROTECTED_OK")
	h.send("N")
	northstarReturnWait(t, h, true)
	h.waitNativeBytes("session exited with code 17")
	if h.cmd.ProcessState.ExitCode() != 1 {
		t.Fatal("native provider nonzero exit was lost")
	}
	if _, err := os.Stat(filepath.Join(f.home, "mailbox", "forbidden")); !os.IsNotExist(err) {
		t.Fatal("provider crossed protected HOME boundary")
	}
}

func northstarReturnNativeProbe(t *testing.T, f *northstarFixture) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("REQUIRED-UNRUN: attach confinement fixture requires native macOS sandbox-exec")
	}
	probe := exec.CommandContext(t.Context(), "/usr/bin/sandbox-exec", "-p", "(version 1) (allow default)", "/usr/bin/true")
	probe.Env, probe.Dir = f.env, f.work
	if output, err := probe.CombinedOutput(); err != nil {
		northstarWrite(t, filepath.Join(f.evidence, "attach-return-native-probe.log"), string(output), 0600)
		t.Fatalf("REQUIRED-UNRUN: sandbox-exec cannot apply native fixture policy: %v: %s", err, output)
	}
}

func northstarReturnSpawnNative(t *testing.T, f *northstarFixture, binary, id string) string {
	t.Helper()
	provider := filepath.Join(f.work, id+"-provider.py")
	northstarWrite(t, provider, `import os, tty, sys
tty.setraw(0)
# Enter the provider screen before publishing either required visible marker.
os.write(1, b'\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1049h\x1b[?25l')
try:
    with open(os.path.join(os.environ['RETURN_PROTECTED_HOME'], 'mailbox', 'forbidden'), 'w') as f: f.write('bad')
    os.write(1, b'PROTECTION_BROKEN\r\n')
except PermissionError: os.write(1, b'PROTECTED_OK\r\n')
os.write(1, b'NATIVE_RETURN_READY\r\n')
while True:
    b = os.read(0, 1)
    if b == b'N': sys.exit(17)
    if not b: break
`, 0600)
	dir := filepath.Join(f.local, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	northstarSocket(t, filepath.Join(dir, "egg.sock"))
	args := []string{"egg", "run", "--session-id", id, "--agent=", "--kind", "command", "--command-arg", "/usr/bin/python3", "--command-arg", provider, "--cwd", f.work, "--network", "none", "--omit-browser-bridge", "--env", "TMPDIR=" + f.tmp, "--env", "PYTHONDONTWRITEBYTECODE=1", "--env", "RETURN_PROTECTED_HOME=" + f.home}
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
		northstarWrite(t, filepath.Join(f.evidence, "attach-return-"+id+"-egg.log"), string(logs.bytes()), 0600)
	})
	logs.wait(t, "serving on ")
	return dir
}
