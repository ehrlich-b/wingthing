package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"google.golang.org/grpc"
)

// The owned test executable is a fake SSH client, never a network transport.
// Its -t path allocates a second kernel PTY and propagates window changes.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "northstar-fake-ssh" {
		os.Exit(northstarRemoteSSH(os.Args[2:]))
	}
}

type northstarSSHRecord struct {
	Target  string `json:"target"`
	Mode    string `json:"tty_mode"`
	Batch   bool   `json:"batch_mode"`
	Command string `json:"command"`
}

func northstarRemoteSSH(args []string) int {
	record := northstarSSHRecord{}
	for len(args) > 0 {
		switch args[0] {
		case "-t", "-T":
			record.Mode = args[0]
			args = args[1:]
		case "-o":
			if len(args) < 2 || args[1] != "BatchMode=yes" {
				return 91
			}
			record.Batch = true
			args = args[2:]
		default:
			goto parsed
		}
	}
parsed:
	if len(args) != 2 || record.Mode == "" {
		return 92
	}
	record.Target, record.Command = args[0], args[1]
	switch record.Target {
	case "ns-forge", "ns-mirror", "ns-offline":
	default:
		return 93
	}
	log, err := os.OpenFile(os.Getenv("NORTHSTAR_REMOTE_SSH_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 94
	}
	if err := unix.Flock(int(log.Fd()), unix.LOCK_EX); err != nil {
		_ = log.Close()
		return 95
	}
	data, _ := json.Marshal(record)
	_, err = log.Write(append(data, '\n'))
	_ = log.Close()
	if err != nil {
		return 95
	}
	_, offline := os.Stat(os.Getenv("NORTHSTAR_SSH_OFFLINE_FILE"))
	if record.Target == "ns-offline" || (record.Target == "ns-forge" && offline == nil) {
		fmt.Fprintln(os.Stderr, "fixture offline")
		return 255
	}
	command := exec.Command("/bin/sh", "-c", record.Command)
	if record.Mode == "-T" {
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = command.Run()
	} else {
		if record.Batch {
			return 96
		}
		old, rawErr := term.MakeRaw(int(os.Stdin.Fd()))
		if rawErr != nil {
			fmt.Fprintln(os.Stderr, rawErr)
			return 97
		}
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), old) }()
		size, sizeErr := pty.GetsizeFull(os.Stdin)
		if sizeErr != nil {
			return 98
		}
		master, startErr := pty.StartWithSize(command, size)
		if startErr != nil {
			fmt.Fprintln(os.Stderr, startErr)
			return 99
		}
		signals := make(chan os.Signal, 4)
		signal.Notify(signals, syscall.SIGWINCH)
		resizeDone := make(chan struct{})
		resizeJoined := make(chan struct{})
		go func() {
			defer close(resizeJoined)
			for {
				select {
				case <-signals:
					if size, err := pty.GetsizeFull(os.Stdin); err == nil {
						_ = pty.Setsize(master, size)
					}
				case <-resizeDone:
					return
				}
			}
		}()
		outputDone := make(chan struct{})
		go func() { _, _ = io.Copy(os.Stdout, master); close(outputDone) }()
		go func() { _, _ = io.Copy(master, os.Stdin) }()
		err = command.Wait()
		// Stop and join ioctl users before closing the descriptor. Fd() is
		// unsafe while the output reader releases a concurrently closed file.
		signal.Stop(signals)
		close(resizeDone)
		<-resizeJoined
		_ = master.Close()
		<-outputDone
	}
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, err)
	return 100
}

type northstarRemoteEgg struct {
	*northstarEgg
	resized chan *pb.Resize
}

type northstarRemoteStream struct {
	grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]
	egg *northstarRemoteEgg
}

func (s northstarRemoteStream) Recv() (*pb.SessionMsg, error) {
	msg, err := s.BidiStreamingServer.Recv()
	if err == nil && msg.GetResize() != nil {
		size := msg.GetResize()
		s.egg.resized <- size
		err = s.Send(&pb.SessionMsg{SessionId: msg.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte(fmt.Sprintf("REMOTE_SIZE:%dx%d\r\n", size.Cols, size.Rows))}})
	}
	return msg, err
}
func (f *northstarRemoteEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	return f.northstarEgg.Session(northstarRemoteStream{BidiStreamingServer: stream, egg: f})
}

func northstarRemoteSession(t *testing.T, f *northstarFixture, state, id, marker string) *northstarRemoteEgg {
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
			t.Fatalf("identity readiness: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("identity readiness timeout")
	}
	for file, value := range map[string]string{
		"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "northstar-fixture-token",
		"egg.meta":        "kind=agent\nagent=claude\ncwd=" + f.work + "\nisolation=fixture\n",
		"lifecycle.jsonl": "{\"sequence\":1,\"type\":\"session_ready\",\"source\":\"claude_hook\",\"state\":\"working\"}\n",
	} {
		northstarWrite(t, filepath.Join(dir, file), value, 0600)
	}
	if err := eggclient.WriteSessionName(dir, "shared-name"); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", northstarSocket(t, filepath.Join(dir, "egg.sock")))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &northstarRemoteEgg{northstarEgg: &northstarEgg{dir: dir, marker: marker, attached: make(chan string, 8)}, resized: make(chan *pb.Resize, 16)}
	rpc := grpc.NewServer()
	pb.RegisterEggServer(rpc, fixture)
	served := make(chan struct{})
	go func() { defer close(served); _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close(); <-served })
	return fixture
}

func northstarSSHRecords(t *testing.T, path string) []northstarSSHRecord {
	t.Helper()
	data, err := northstarReadSSHLog(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []northstarSSHRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record northstarSSHRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

// Read a consistent fixture log snapshot while concurrent SSH helpers append.
// The strict JSON assertions still examine every complete recorded operation.
func northstarReadSSHLog(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func TestNorthstarSSHLogSnapshotJoinsInProgressAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`{"target":"fixture",`)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() { data, err := northstarReadSSHLog(path); done <- result{data, err} }()
	select {
	case <-done:
		t.Fatal("snapshot read an in-progress record")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := file.Write([]byte("\"batch_mode\":true}\n")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		var record northstarSSHRecord
		if result.err != nil || json.Unmarshal(result.data, &record) != nil || record.Target != "fixture" || !record.Batch {
			t.Fatalf("snapshot=%q err=%v", result.data, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot did not join append")
	}
}

func TestNorthstarPTYRemoteAttach(t *testing.T) {
	f := northstarNewFixture(t)
	f.work = filepath.Join(f.root, "work space; $(touch injected); it's literal")
	if err := os.Mkdir(f.work, 0700); err != nil {
		t.Fatal(err)
	}
	binary := northstarBuild(t, f, "stable")
	custom := filepath.Join(f.bin, "custom wt")
	if err := os.Symlink(binary, custom); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(f.home, "m")
	if err := os.Mkdir(mirror, 0700); err != nil {
		t.Fatal(err)
	}
	profile := config.Remote{SSHTarget: "ns-forge", WTBinary: custom, WingthingDir: f.remote}
	if err := config.SaveRemotes(f.local, map[string]config.Remote{
		"forge": profile, "mirror": {SSHTarget: "ns-mirror", WingthingDir: mirror}, "offline": {SSHTarget: "ns-offline"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{f.local, f.remote, mirror} {
		socket, err := controlsocket.Path(state)
		if err != nil {
			t.Fatal(err)
		}
		northstarSocket(t, socket)
	}
	log := filepath.Join(f.home, "ssh-routes.jsonl")
	f.env = append(f.env, "NORTHSTAR_REMOTE_SSH_LOG="+log)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nexec "+remotepkg.ShellQuote(exe)+" northstar-fake-ssh \"$@\"\n", 0700)
	t.Cleanup(func() {
		if data, err := os.ReadFile(log); err == nil {
			artifact := filepath.Join(f.evidence, "TestNorthstarPTYRemoteAttach-"+filepath.Base(f.root)+"-ssh.jsonl")
			northstarWrite(t, artifact, string(data), 0600)
			t.Logf("exact SSH/TTY routes: %s", artifact)
		}
	})
	const id = "shared-id"
	local := northstarRemoteSession(t, f, f.local, id, "LOCAL_ONLY")
	forge := northstarRemoteSession(t, f, f.remote, id, "FORGE_ONLY")
	peer := northstarRemoteSession(t, f, mirror, id, "MIRROR_ONLY")
	assertSurvives := func(egg *northstarRemoteEgg) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(egg.dir, "egg.pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || syscall.Kill(pid, 0) != nil || egg.killed.Load() != 0 {
			t.Fatal("detach stopped the exact fixture process")
		}
	}
	started := time.Now()
	h := northstarStartPTY(t, f, binary, 80, 24)
	h.waitScreen("colliding local and two peers / one offline", 0, func(s string) bool {
		return strings.Count(s, "shared-name") == 4 && strings.Contains(s, "offline unreachable") && strings.Contains(s, "mirror") && strings.Contains(s, "forge")
	})
	t.Logf("launch-to-fleet inventory: %s", time.Since(started))
	h.expect("select exact forge", h.send("/forge\r"), "> shared-name")
	h.expect("attach forge only", h.send("\r"), "FORGE_ONLY")
	select {
	case attached := <-forge.attached:
		if attached != id {
			t.Fatal(attached)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forge did not attach")
	}
	if len(local.attached) != 0 || len(peer.attached) != 0 {
		t.Fatal("colliding ID attached on another machine")
	}
	h.expect("resize traverses remote PTY", h.resize(101, 31), "REMOTE_SIZE:101x31")
	select {
	case size := <-forge.resized:
		if size.Cols != 101 || size.Rows != 31 {
			t.Fatalf("remote resize: %+v", size)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote PTY never resized")
	}
	h.send("\x02")
	h.expect("detach returns to exact forge row", h.send("q"), "> shared-name", "Enter open")
	assertSurvives(forge)
	h.expect("rename exact forge", h.send("r\x15forge-renamed\r"), "> forge-renamed")
	if eggclient.ReadSessionName(local.dir) != "shared-name" || eggclient.ReadSessionName(peer.dir) != "shared-name" || eggclient.ReadSessionName(forge.dir) != "forge-renamed" {
		t.Fatal("rename crossed exact machine identity")
	}
	northstarWrite(t, filepath.Join(f.home, "forge-offline"), "fixture offline", 0600)
	h.expect("retain stale forge row", h.send("R"), "Last seen", "forge-renamed")
	h.expect("stale rename refused", h.send("r\x15must-not-rename\r"), "Saved machine changed", "forge")
	if eggclient.ReadSessionName(forge.dir) != "forge-renamed" {
		t.Fatal("unvalidated stale rename dispatched")
	}
	h.expect("stale stop confirmation", h.send("x"), "forge-renamed on forge")
	h.expect("stale stop refused", h.send("y"), "Saved machine changed", "forge")
	assertSurvives(forge)
	// Local work remains reachable while both forge and the offline peer fail.
	h.expect("select healthy local", h.send("/\x15local\r"), "shared-name")
	h.expect("attach local despite offline peers", h.send("\r"), "LOCAL_ONLY")
	h.expect("detach local", h.send("\x02q"), "shared-name", "Enter open")
	assertSurvives(local)
	if err := os.Remove(filepath.Join(f.home, "forge-offline")); err != nil {
		t.Fatal(err)
	}
	h.expect("recover exact forge", h.send("/\x15forge\rR"), "* Working", "forge-renamed")
	h.exit("q")
	// Exercise both receiving CLI paths on real PTYs, including the delimiter.
	for _, test := range []struct {
		name string
		args []string
	}{
		{"named", []string{"attach", "--", "forge:" + id}},
		{"generic", []string{"--remote", profile.SSHTarget, "--remote-binary", profile.Binary(), "--remote-state", profile.WingthingDir, "attach", "--", id}},
	} {
		t.Run(test.name, func(t *testing.T) {
			attached := northstarStartPTY(t, f, binary, 80, 24, test.args...)
			attached.expect("exact configured remote attach", 0, "FORGE_ONLY")
			select {
			case got := <-forge.attached:
				if got != id {
					t.Fatal(got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("exact forge attach missing")
			}
			attached.expect("generic/named resize", attached.resize(94, 28), "REMOTE_SIZE:94x28")
			select {
			case size := <-forge.resized:
				if size.Cols != 94 || size.Rows != 28 {
					t.Fatal(size)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("resize missing")
			}
			attached.exit("\x02q")
			assertSurvives(forge)
		})
	}
	h = northstarStartPTY(t, f, binary, 80, 24)
	h.expect("fresh fleet", 0, "forge-renamed", "shared-name")
	h.expect("stop exact forge selection", h.send("/forge\r"), "forge-renamed")
	h.expect("stop exact forge confirmation", h.send("x"), "forge-renamed on forge")
	h.waitScreen("stop exact forge", h.send("y"), func(s string) bool {
		return strings.Contains(s, "No matching sessions") && strings.Contains(s, "Enter open") && !strings.Contains(s, "forge-renamed") && !strings.Contains(s, "Running")
	})
	h.exit("q")
	if forge.killed.Load() != 1 || local.killed.Load() != 0 || peer.killed.Load() != 0 {
		t.Fatal("stop crossed colliding machine ID")
	}
	assertSurvives(local)
	assertSurvives(peer)
	records := northstarSSHRecords(t, log)
	attaches, renames, stops := 0, 0, 0
	prefix := "WINGTHING_DIR=" + remotepkg.ShellQuote(f.remote) + " WINGTHING_PREVIEW_DIR=" + remotepkg.ShellQuote(f.remote) + " " + remotepkg.ShellQuote(custom) + " '--expected-channel' 'stable' "
	for _, record := range records {
		attach := strings.Contains(record.Command, "'attach'")
		if attach {
			attaches++
			if record.Target != "ns-forge" || record.Mode != "-t" || record.Batch || !strings.Contains(record.Command, "'--' '"+id+"'") {
				t.Fatalf("foreground exact/auth route: %+v", record)
			}
		} else if record.Mode != "-T" || !record.Batch {
			t.Fatalf("background query/action can prompt or allocate TTY: %+v", record)
		}
		if record.Target == "ns-forge" && !strings.HasPrefix(record.Command, prefix) {
			t.Fatalf("custom binary/state/channel route lost: %+v", record)
		}
		if strings.Contains(record.Command, "'session' 'rename'") {
			renames++
		}
		if strings.Contains(record.Command, "'session' 'kill'") {
			stops++
		}
	}
	if attaches != 3 || renames != 1 || stops != 1 {
		t.Fatalf("dispatch counts attach/rename/stop=%d/%d/%d; stale mutation or retry dispatched", attaches, renames, stops)
	}
	canonical, err := filepath.EvalSymlinks(f.tmp)
	if err != nil || canonical != filepath.Join(f.root, "tmp-real") || !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
		t.Fatal("real application did not launch through symlinked TMPDIR parent")
	}
}
