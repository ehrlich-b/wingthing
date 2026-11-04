package main

// Shared, test-only kernel PTY helpers for the Northstar acceptance journeys.
// Providers and SSH may be fixtures; the root CLI and terminal handoff are real.
import (
	"bufio"
	"bytes"
	"context"
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
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"golang.org/x/term"
	"google.golang.org/grpc"
)

// Only the test executable recognizes this fixture switch. No production CLI
// backdoor or provider is needed to hold an owned process for standalone eggs.
func init() {
	if os.Getenv("WT_NORTHSTAR_PID_FIXTURE") == "1" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

type northstarFixture struct {
	repo, root, home, local, remote, work, tmp, bin, evidence string
	env                                                       []string
}

func northstarWrite(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func northstarNewFixture(t *testing.T) *northstarFixture {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(repo, ".scratch")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "northstar-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, local, remote := testprovider.ScopedHomeDirs(t, scratch)
	f := &northstarFixture{repo: repo, root: root, home: home, local: local, remote: remote,
		work: filepath.Join(root, "work"), tmp: filepath.Join(root, "tmp-alias"), bin: filepath.Join(root, "tmp-alias", "bin"),
		evidence: filepath.Join(scratch, "northstar", "northstar-evidence")}
	// TMPDIR must actually traverse an alias created by this regression. State
	// and mailbox stay in protected HOME, outside provider-writable work/TMPDIR.
	if err := os.Mkdir(filepath.Join(root, "tmp-real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "tmp-real"), f.tmp); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.work, f.bin, filepath.Join(home, "mailbox"), f.evidence} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = []string{"HOME=" + home, "WINGTHING_DIR=" + local, "PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + f.tmp,
		"TERM=xterm-256color", "LANG=en_US.UTF-8", "PYTHONDONTWRITEBYTECODE=1", "NORTHSTAR_SSH_OFFLINE_FILE=" + filepath.Join(home, "devbox-offline")}
	// Only these exact fixture aliases can reach the shell executor. The command
	// still runs the built wt with the literal state/argv produced by real SSH code.
	northstarWrite(t, filepath.Join(f.bin, "ssh"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in -T|-t) shift ;; -o) shift 2 ;; *) break ;; esac
done
host=$1
shift
case "$host" in
 ns-devbox)
 if [ -f "$NORTHSTAR_SSH_OFFLINE_FILE" ]; then printf 'fixture query failure\n' >&2; exit 255; fi
 exec /bin/sh -c "$1" ;;
 ns-empty) exec /bin/sh -c "$1" ;;
 ns-offline) printf 'fixture offline\n' >&2; exit 255 ;;
 *) printf 'unexpected fixture SSH target\n' >&2; exit 99 ;;
esac
`, 0700)
	empty := filepath.Join(home, "e")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(local, "remotes.yaml"), fmt.Sprintf(`remotes:
  devbox:
    ssh_target: ns-devbox
    wingthing_dir: %s
  empty:
    ssh_target: ns-empty
    wingthing_dir: %s
  offline:
    ssh_target: ns-offline
`, remote, empty), 0600)
	return f
}

func northstarBuild(t *testing.T, f *northstarFixture, channel string) string {
	t.Helper()
	binary := filepath.Join(f.bin, "wt")
	selector := "WT_TEST_BINARY"
	if channel == "preview" {
		binary += "-preview"
		selector = "WT_TEST_PREVIEW_BINARY"
	}
	if supplied := os.Getenv(selector); supplied != "" {
		// Copy the selected channel into the fixture so every CLI still launches
		// through its TMPDIR alias when a gate supplies a prebuilt binary.
		data, err := os.ReadFile(supplied)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, data, 0700); err != nil {
			t.Fatal(err)
		}
		return binary
	}
	// Use the toolchain that compiled this test, regardless of the runner's
	// username or whether a fixture replaced PATH with provider-only tools.
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if info, err := os.Stat(goBin); err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		goBin = "go"
	}
	args := []string{"-n", "15", goBin, "build", "-p", "2", "-buildvcs=false", "-o", binary}
	if channel == "preview" {
		args = append(args, "-ldflags", "-X github.com/ehrlich-b/wingthing/internal/config.ReleaseChannel=preview")
	}
	args = append(args, "./cmd/wt")
	build := exec.Command("nice", args...)
	if runtime.GOOS == "darwin" {
		build = exec.Command("taskpolicy", append([]string{"-b", "nice"}, args...)...)
	}
	build.Dir = f.repo
	// Reuse the invoking gate's warmed caches. Without explicit selectors,
	// keep caches clone-local; application children retain their isolated env.
	build.Env = os.Environ()
	for key, dir := range map[string]string{"GOCACHE": "go-build", "GOMODCACHE": "go-mod"} {
		if os.Getenv(key) == "" {
			build.Env = append(build.Env, key+"="+filepath.Join(f.repo, ".scratch", "northstar", dir))
		}
	}
	// t.Context is cancelled during cleanup, not before the test timeout
	// alarm. Give this nested build its own deadline so it cannot consume the
	// entire acceptance budget or strand compiler children after a timeout.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if output, err := northstarBuildOutput(ctx, build); err != nil {
		t.Fatalf("build actual wt: %v\n%s", err, output)
	}
	return binary
}

type northstarBuildLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (log *northstarBuildLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.buffer.Write(data)
}

func (log *northstarBuildLog) snapshot() []byte {
	log.mu.Lock()
	defer log.mu.Unlock()
	return bytes.Clone(log.buffer.Bytes())
}

func northstarBuildOutput(ctx context.Context, build *exec.Cmd) ([]byte, error) {
	var log northstarBuildLog
	build.Stdout, build.Stderr = &log, &log
	build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A compiler descendant may keep an output pipe open after the Go process
	// exits. Bound that copy too, independently of the process exit wait.
	build.WaitDelay = time.Second
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := build.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- build.Wait() }()
	signalGroup := func(signal syscall.Signal) error {
		err := syscall.Kill(-build.Process.Pid, signal)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	select {
	case err := <-done:
		if err != nil {
			err = errors.Join(err, signalGroup(syscall.SIGKILL))
		}
		return log.snapshot(), err
	case <-ctx.Done():
	}
	err := errors.Join(ctx.Err(), signalGroup(syscall.SIGTERM))
	grace := time.NewTimer(time.Second)
	defer grace.Stop()
	select {
	case waitErr := <-done:
		return log.snapshot(), errors.Join(err, waitErr, signalGroup(syscall.SIGKILL))
	case <-grace.C:
	}
	err = errors.Join(err, signalGroup(syscall.SIGKILL))
	join := time.NewTimer(2 * time.Second)
	defer join.Stop()
	select {
	case waitErr := <-done:
		return log.snapshot(), errors.Join(err, waitErr)
	case <-join.C:
		return log.snapshot(), errors.Join(err, errors.New("build child was not reaped after SIGKILL"))
	}
}

func northstarSocket(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	canonical = filepath.Join(canonical, filepath.Base(path))
	for _, candidate := range []string{path, canonical} {
		if len([]byte(candidate)) > 103 {
			t.Fatalf("Unix socket has %d bytes (max 103): %s", len([]byte(candidate)), candidate)
		}
	}
	t.Logf("Unix socket lexical/canonical bytes: %d/%d", len([]byte(path)), len([]byte(canonical)))
	return path
}

type northstarEgg struct {
	pb.UnimplementedEggServer
	dir, marker      string
	attached         chan string
	killed           atomic.Int32
	previewReads     atomic.Int32
	previewMutations atomic.Int32
}

func (f *northstarEgg) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Agent: "claude", UptimeSeconds: 120, Readers: 1}, nil
}
func (f *northstarEgg) Kill(_ context.Context, r *pb.KillRequest) (*pb.KillResponse, error) {
	if r.SessionId != filepath.Base(f.dir) {
		return nil, fmt.Errorf("wrong fixture kill target: %s", r.SessionId)
	}
	f.killed.Add(1)
	if err := os.Remove(filepath.Join(f.dir, "egg.pid")); err != nil {
		return nil, err
	}
	return &pb.KillResponse{}, nil
}
func (f *northstarEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.SessionId != filepath.Base(f.dir) {
		return fmt.Errorf("wrong fixture attach: %s", first.SessionId)
	}
	options := first.GetAttachOptions()
	if options != nil && options.ReadOnly && !options.Claim && !options.Takeover && options.Rows == 0 && options.Cols == 0 {
		f.previewReads.Add(1)
	} else {
		f.previewMutations.Add(1)
	}
	// Snapshot observations are distinct from an interactive viewer. Keep the
	// interactive channel useful for assertions about explicit attachment.
	if options == nil || options.Owner != "snapshot" {
		select {
		case f.attached <- first.SessionId:
		default:
		}
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("\x1b[2J\x1b[H" + f.marker + "\r\n")}}); err != nil {
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
		if msg.GetResize() != nil || len(msg.GetInput()) > 0 {
			f.previewMutations.Add(1)
		}
		if input := msg.GetInput(); len(input) > 0 {
			if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: append([]byte("INPUT:"), input...)}}); err != nil {
				return err
			}
		}
	}
}

func northstarStandaloneEgg(t *testing.T, f *northstarFixture, state, id, name, lifecycle, marker string) *northstarEgg {
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
			t.Fatalf("fixture identity: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fixture identity timeout")
	}
	for file, value := range map[string]string{"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "northstar-fixture-token",
		"egg.meta":        "kind=agent\nagent=claude\ncwd=" + f.work + "\nisolation=fixture\n",
		"lifecycle.jsonl": fmt.Sprintf("{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":%q}\n", lifecycle)} {
		northstarWrite(t, filepath.Join(dir, file), value, 0600)
	}
	if err := eggclient.WriteSessionName(dir, name); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", northstarSocket(t, filepath.Join(dir, "egg.sock")))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &northstarEgg{dir: dir, marker: marker, attached: make(chan string, 4)}
	rpc := grpc.NewServer()
	pb.RegisterEggServer(rpc, fixture)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-serveDone })
	return fixture
}

type northstarPTY struct {
	t             *testing.T
	cmd           *exec.Cmd
	master, slave *os.File
	before        *term.State
	emu           *vt.Emulator
	mu            sync.Mutex
	revision      uint64
	changed       chan struct{}
	done          chan error
	readDone      chan struct{}
	transcript    bytes.Buffer
	screens       strings.Builder
	artifact      string
}

func northstarStartPTY(t *testing.T, f *northstarFixture, binary string, cols, rows int, args ...string) *northstarPTY {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Env, cmd.Dir = f.env, f.work
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &northstarPTY{t: t, cmd: cmd, master: master, slave: slave, before: before, emu: vt.NewEmulator(cols, rows), changed: make(chan struct{}, 1), done: make(chan error, 1), readDone: make(chan struct{}), artifact: filepath.Join(f.evidence, strings.ReplaceAll(t.Name(), "/", "-")+"-"+strconv.Itoa(cols)+"-"+strconv.Itoa(cmd.Process.Pid))}
	// VT query replies must be drained even if a child asks for capabilities.
	queryDone := make(chan struct{})
	go func() { defer close(queryDone); _, _ = io.Copy(io.Discard, h.emu) }()
	go func() { h.done <- cmd.Wait(); close(h.done) }()
	go func() {
		defer close(h.readDone)
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				h.mu.Lock()
				h.transcript.Write(buf[:n])
				_, _ = h.emu.Write(buf[:n])
				h.revision++
				h.mu.Unlock()
				select {
				case h.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// Exact owned child group only; no process scans or state-derived PID kills.
		select {
		case <-h.done:
		default:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-h.done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				select {
				case <-h.done:
				case <-time.After(time.Second):
					t.Error("owned PTY child did not join")
				}
			}
		}
		_ = master.Close()
		_ = slave.Close()
		<-h.readDone
		// Emulator.Read checks an unsynchronized closed flag. Close its query
		// pipe first and join the reader before Emulator.Close changes that flag.
		_ = h.emu.InputPipe().(io.Closer).Close()
		<-queryDone
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = h.emu.Close()
		northstarWrite(t, h.artifact+".ansi", h.transcript.String(), 0600)
		northstarWrite(t, h.artifact+".txt", h.screens.String(), 0600)
		t.Logf("PTY screen transcript: %s.txt", h.artifact)
	})
	return h
}

func (h *northstarPTY) send(input string) uint64 {
	h.t.Helper()
	h.mu.Lock()
	rev := h.revision
	h.mu.Unlock()
	if os.Getenv("WT_TEST_INVENTORY_TRACE") != "" {
		h.t.Logf("PTY send %q at %s", input, time.Now().Format(time.RFC3339Nano))
	}
	if _, err := io.WriteString(h.master, input); err != nil {
		h.t.Fatal(err)
	}
	return rev
}
func (h *northstarPTY) screen() (string, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.emu.String(), h.revision
}
func (h *northstarPTY) waitScreen(label string, after uint64, match func(string) bool) string {
	h.t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		screen, rev := h.screen()
		if rev > after && match(screen) {
			if os.Getenv("WT_TEST_INVENTORY_TRACE") != "" {
				h.t.Logf("PTY screen %s at %s", label, time.Now().Format(time.RFC3339Nano))
			}
			h.mu.Lock()
			fmt.Fprintf(&h.screens, "\n--- %s ---\n%s\n", label, screen)
			h.mu.Unlock()
			return screen
		}
		select {
		case <-h.changed:
		case <-timer.C:
			h.t.Fatalf("screen timeout (%s):\n%s", label, screen)
		}
	}
}
func (h *northstarPTY) expect(label string, after uint64, contains ...string) string {
	h.t.Helper()
	return h.waitScreen(label, after, func(screen string) bool {
		flat := strings.NewReplacer("\r", "", "\n", "").Replace(screen)
		for _, text := range contains {
			if !strings.Contains(flat, text) && !northstarContainsRow(screen, text) {
				return false
			}
		}
		return true
	})
}
func (h *northstarPTY) resize(cols, rows int) uint64 {
	h.t.Helper()
	h.mu.Lock()
	rev := h.revision
	h.emu.Resize(cols, rows)
	h.mu.Unlock()
	if err := pty.Setsize(h.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
		h.t.Fatal(err)
	}
	return rev
}
func (h *northstarPTY) exit(input string) {
	h.t.Helper()
	h.send(input)
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Fatalf("PTY child exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		h.t.Fatal("PTY child exit timeout")
	}
	// Darwin revokes the slave when its session leader exits; the master
	// retains the shared terminal attributes for the restoration assertion.
	after, err := term.GetState(int(h.master.Fd()))
	if err != nil || !reflect.DeepEqual(h.before, after) {
		h.t.Fatalf("cooked termios not restored: before=%+v after=%+v err=%v", h.before, after, err)
	}
}

func TestNorthstarPTYSessionNavigator(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	for _, state := range []string{f.local, f.remote} {
		socket, err := controlsocket.Path(state)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, socket)
	}
	if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("CLI did not launch through fixture TMPDIR alias")
	}
	// Current UI changes are channel-independent. Native preview egg identity
	// and upstream provider/SSH canaries retain their separate acceptance gates.
	local := northstarStandaloneEgg(t, f, f.local, "same", "local-parent", "working", "LOCAL_FIXTURE_READY")
	devbox := northstarStandaloneEgg(t, f, f.remote, "same", "devbox-parent", "needs_input", "DEVBOX_FIXTURE_READY")
	disposable := northstarStandaloneEgg(t, f, f.remote, "stop-me", "disposable", "working", "STOP_FIXTURE_READY")
	// Prove root help bytes for the built CLI without a PTY as well as unit routing.
	help := exec.CommandContext(t.Context(), binary)
	help.Env, help.Dir = f.env, f.work
	if output, err := help.Output(); err != nil || !bytes.Equal(output, rootHelpGolden) {
		t.Fatalf("non-TTY root help changed: %v\n%s", err, output)
	}
	started := time.Now()
	h := northstarStartPTY(t, f, binary, 80, 24)
	h.expect("first dashboard frame", 0, "wt   ", "m machines")
	t.Logf("fixture launch-to-first-frame: %s", time.Since(started))
	h.expect("all machines / initial inventory", 0, "local parent", "devbox parent", "disposable", "offline unreachable", "Claude", "2m", "? help")
	t.Logf("fixture launch-to-full-inventory: %s", time.Since(started))
	rev := h.send("?")
	h.expect("help / detach and legend", rev, "Ctrl+B then q", "Move", "Manage", "keeps sessions running")
	h.expect("help close", h.send("\x1b"), "local parent", "devbox parent", "Enter open")
	rev = h.send("!")
	h.waitScreen("attention filter", rev, func(s string) bool {
		return strings.Contains(s, "devbox parent") && !strings.Contains(s, "local parent") && !strings.Contains(s, "disposable")
	})
	h.expect("attention off", h.send("!"), "local parent", "disposable")
	rev = h.send("/DEVBOX\r")
	h.waitScreen("qualified filter / colliding IDs", rev, func(s string) bool {
		return strings.Contains(s, "devbox parent") && !strings.Contains(s, "local parent") && northstarContainsRow(s, "> devbox parent")
	})
	h.expect("full details / exact route", h.send("i"), "Machine: devbox", "Project: work", "Permissions: Not reported")
	h.expect("details close", h.send("\x1b"), "devbox parent", "Enter open")
	h.expect("rename prompt", h.send("r\x15devbox-renamed"), "Name: devbox-renamed")
	h.expect("rename refresh", h.send("\r"), "> devbox renamed")
	if got := eggclient.ReadSessionName(local.dir); got != "local-parent" {
		t.Fatalf("colliding local ID renamed: %s", got)
	}
	if got := eggclient.ReadSessionName(devbox.dir); got != "devbox-renamed" {
		t.Fatalf("remote not renamed: %s", got)
	}
	h.expect("fixture attach output", h.send("\r"), "DEVBOX_FIXTURE_READY")
	select {
	case id := <-devbox.attached:
		if id != "same" {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("remote fixture did not attach")
	}
	if len(local.attached) != 0 {
		t.Fatal("Enter opened the colliding local ID")
	}
	// The split prefix crosses separate PTY writes; no list key intercepts q.
	h.send("\x02")
	h.expect("detached / same qualified selection", h.send("q"), "> devbox renamed", "Enter open")
	if devbox.killed.Load() != 0 {
		t.Fatal("detach stopped the egg")
	}
	northstarWrite(t, filepath.Join(f.home, "devbox-offline"), "offline", 0600)
	h.expect("retained remote rows / stale query failure", h.send("R"), "devbox not searched", "Last seen", "> devbox renamed")
	if err := os.Remove(filepath.Join(f.home, "devbox-offline")); err != nil {
		t.Fatal(err)
	}
	rev = h.send("R")
	h.waitScreen("remote recovery / same qualified selection", rev, func(s string) bool {
		return northstarContainsRow(s, "> devbox renamed") && strings.Contains(s, "! Needs you") && !strings.Contains(s, "devbox unreachable")
	})
	h.expect("clear filter", h.send("/\x15\r"), "local parent", "disposable")
	h.expect("stop filter", h.send("/disposable\r"), "> disposable", "* Working")
	h.expect("captured stop confirmation", h.send("x"), "disposable on devbox", "Enter keep running")
	h.expect("stop cancelled", h.send("n"), "Enter open", "disposable")
	if disposable.killed.Load() != 0 {
		t.Fatal("cancel stopped the fixture")
	}
	h.expect("stop confirmation again", h.send("x"), "disposable on devbox")
	rev = h.send("y")
	h.waitScreen("confirmed stop / no matches", rev, func(s string) bool {
		return strings.Contains(s, "No matching sessions") && strings.Contains(s, "Enter open") && !northstarContainsRow(s, "> disposable")
	})
	if disposable.killed.Load() != 1 || local.killed.Load() != 0 || devbox.killed.Load() != 0 {
		t.Fatal("stop addressed wrong exact target")
	}
	h.expect("clear filter after stop", h.send("/\x15\r"), "local parent", "devbox renamed")
	h.expect("machine picker includes empty/offline", h.send("m"), "All machines", "empty", "offline", "Enter select")
	h.expect("selected empty remote", h.send("jjj\r"), "empty", "No sessions yet on empty")
	h.expect("empty remote new agent", h.send("n"), "Agent (blank = shell)")
	h.expect("empty remote requires remote cwd", h.send("\r"), "Machine: empty", "Working directory", "_")
	h.expect("empty cwd rejected", h.send("\r"), "Working directory cannot be empty")
	h.expect("cancel new / no launch", h.send("\x1b"), "empty", "n new session")
	h.expect("restore all machines", h.send("m\x1b[H\r"), "local parent", "devbox renamed")
	rev = h.resize(40, 12)
	h.expect("40x12 compact navigator", rev, "local parent", "Working", "! attention", "m scope")
	rev = h.send("?")
	h.expect("40x12 help", rev, "Help", "Esc back")
	h.expect("40x12 help scroll / detach", h.send("\x1b[6~"), "Ctrl+B then q")
	h.expect("40x12 return", h.send("\x1b"), "local parent")
	rev = h.resize(120, 40)
	h.expect("120x40 restored", rev, "local parent", "devbox renamed", "offline unreachable", "R reconnect")
	h.exit("q")
	if local.killed.Load() != 0 || devbox.killed.Load() != 0 {
		t.Fatal("quitting stopped surviving sessions")
	}
	h2 := northstarStartPTY(t, f, binary, 40, 12)
	h2.expect("reopen / sessions survive", 0, "local parent", "Working")
	h2.exit("\x03")
	// Test alias was used by every actual application child, not ambient TMPDIR.
	resolved, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || resolved != filepath.Join(f.root, "tmp-real") {
		t.Fatalf("TMPDIR alias: %s %v", resolved, err)
	}
	// No daemon or provider was started by the navigator.
	if _, err := os.Stat(filepath.Join(f.local, "wing.pid")); !os.IsNotExist(err) {
		t.Fatalf("navigator started a wing: %v", err)
	}
}

// Column padding changes with terminal width; preserve each expected row's
// words and unread/state markers while allowing its inter-column spacing.
func northstarContainsRow(screen, text string) bool {
	if strings.HasSuffix(text, " *") {
		return northstarNamedState(screen, text, "")
	}
	// The selection marker sits in a row's first cell, before the state.
	name, selected := strings.CutPrefix(text, "> ")
	for _, line := range strings.Split(screen, "\n") {
		line = strings.Join(strings.Fields(strings.Trim(line, "│| ")), " ")
		if selected && strings.HasPrefix(line, "> ") && strings.Contains(line+" ", " "+name) {
			return true
		}
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}
