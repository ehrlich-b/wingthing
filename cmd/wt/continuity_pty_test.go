package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"golang.org/x/term"
)

// Exercise the built CLI, a real persistent egg and the supervised SSH fixture
// together. The transport dies while the provider and its PTY stay alive.
func TestBuiltWTContinuityReconnectPTY(t *testing.T) {
	binary := fidelityBuild(t)
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("binary=%s sha256=%x", binary, sha256.Sum256(data))
	provider := filepath.Join(t.TempDir(), "provider.py")
	if err := os.WriteFile(provider, []byte(`import os, tty
tty.setraw(0)
def emit(s): os.write(1, s.encode())
emit('SESSION:fidelity PID:%d\r\n' % os.getpid())
while True:
    c = os.read(0, 1)
    if c == b'a': emit('\x1b[?1000h\x1b[?1002l\x1b[?1006h\x1b[?2004h\x1b[?25lBEFORE_DROP:session=fidelity PID:%d\r\n' % os.getpid())
    elif c == b'b': emit('AFTER_RECONNECT:session=fidelity PID:%d\r\n' % os.getpid())
    elif not c: break
`), 0600); err != nil {
		t.Fatal(err)
	}
	f := startFidelityEgg(t, binary, provider)
	var mu sync.Mutex
	var ids []string
	var exact []bool
	server, err := controlsocket.Listen(t.Context(), f.home, "continuity-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID}
		}, nil
	}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
		a := *hello.Attach
		if a.Session != "fidelity" {
			return controlsocket.Welcome{}, nil, fmt.Errorf("unexpected session %q", a.Session)
		}
		mu.Lock()
		ids = append(ids, a.Session)
		exact = append(exact, a.Exact)
		mu.Unlock()
		return controlsocket.Welcome{Capabilities: []string{controlsocket.NativeTerminalCapability}}, func(ctx context.Context, remote *controlsocket.SessionStream) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			defer remote.Close()
			client, err := egg.Dial(f.socket, f.token)
			if err != nil {
				return err
			}
			defer client.Close()
			local, err := client.AttachSessionWithOptions(ctx, "fidelity", egg.AttachOptions{NativeTerminal: a.NativeTerminal, Claim: true, Owner: "cli", Takeover: a.Takeover, Rows: a.Rows, Cols: a.Cols})
			if err != nil {
				return err
			}
			done := make(chan error, 2)
			go func() {
				for {
					msg, err := local.Recv()
					if err == nil {
						err = remote.Send(msg)
					}
					if err != nil {
						done <- err
						return
					}
				}
			}()
			go func() {
				for {
					msg, err := remote.Recv()
					if err == nil {
						err = local.Send(msg)
					}
					if err != nil {
						done <- err
						return
					}
				}
			}()
			return <-done
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ssh := testssh.New(t)
	meta, err := sshcontrol.InspectLocal(t.Context(), f.home, "")
	if err != nil {
		t.Fatal(err)
	}
	ssh.Host(t, "continuity-peer", meta)
	// Resolve ssh through the same PATH used by the ordinary configured CLI.
	if err := os.Symlink(ssh.SSHPath, filepath.Join(f.home, "ssh")); err != nil {
		t.Fatal(err)
	}
	profile := fmt.Sprintf("remotes:\n  fixture:\n    ssh_target: continuity-peer\n    wingthing_dir: %s\n    wing_id: %s\n    control_socket: %s\n    control_version: %s\n", f.home, meta.WingID, meta.ControlSocket, meta.Version)
	if err := os.WriteFile(filepath.Join(f.home, "remotes.yaml"), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	initial := map[int]bool{25: true, 1000: false, 1002: true, 1006: false, 2004: false}
	cmd := exec.CommandContext(t.Context(), binary, "attach", "fixture:fidelity")
	cmd.Env = append(append([]string{}, f.cmd.Env...), "PATH="+f.home+":/usr/bin:/bin", "WT_FAKE_SSH_ROOT="+ssh.Root)
	h := startFidelityTerminal(t, cmd, initial)
	nextForward := func() *testssh.Forward {
		select {
		case forward := <-ssh.Started:
			return forward
		case <-time.After(10 * time.Second):
			t.Fatal("SSH fixture did not start")
			return nil
		}
	}
	first := nextForward()
	h.wait("SESSION:fidelity PID:")
	h.send("a")
	h.wait("BEFORE_DROP:session=fidelity PID:")
	before := h.snapshot()
	t.Logf("session head=%q", before)
	t.Logf("transport kill pid=%d session=fidelity", first.PID)
	if err := (&os.Process{Pid: first.PID}).Kill(); err != nil {
		t.Fatal(err)
	}
	second := nextForward()
	if second.PID == first.PID {
		t.Fatal("transport was not replaced")
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for !bytes.Contains(h.snapshot()[len(before):], []byte("BEFORE_DROP:session=fidelity")) {
		select {
		case <-h.changed:
		case <-timer.C:
			t.Fatalf("reconnect did not restore screen: %q", h.snapshot())
		}
	}
	h.send("b")
	h.wait("AFTER_RECONNECT:session=fidelity PID:")
	recovered := h.snapshot()
	tail := recovered[len(before):]
	pids := regexp.MustCompile(`(?:BEFORE_DROP|AFTER_RECONNECT):session=fidelity PID:(\d+)`).FindAllSubmatch(recovered, -1)
	if len(pids) < 3 {
		t.Fatalf("missing provider identity: %q", recovered)
	}
	for _, pid := range pids {
		if !bytes.Equal(pid[1], pids[0][1]) {
			t.Fatal("provider process changed")
		}
	}
	if !bytes.Contains(tail, []byte("BEFORE_DROP:session=fidelity")) {
		t.Fatalf("screen output was not recovered: %q", tail)
	}
	mu.Lock()
	gotIDs, gotExact := append([]string{}, ids...), append([]bool{}, exact...)
	mu.Unlock()
	if len(gotIDs) != 2 || gotIDs[0] != "fidelity" || gotIDs[1] != "fidelity" || !gotExact[1] {
		t.Fatalf("identity/exact reconnect: %v %v", gotIDs, gotExact)
	}
	t.Logf("reconnect pid=%d sessions=%v exact=%v retained output=%q", second.PID, gotIDs, gotExact, tail)
	t.Log("detach Ctrl+B Q")
	h.send("\x02q")
	h.wait("[detached from fixture:fidelity]")
	select {
	case <-h.processDone:
	case <-time.After(10 * time.Second):
		t.Fatal("detach did not finish")
	}
	if h.err != nil {
		t.Fatal(h.err)
	}
	// The guardian may finish its last terminal write immediately after the CLI.
	h.wait("[detached from fixture:fidelity]")
	h.mu.Lock()
	restored := make(map[int]bool)
	for mode := range initial {
		restored[mode] = h.modes[mode]
	}
	h.mu.Unlock()
	if !reflect.DeepEqual(initial, restored) {
		t.Fatalf("mode restore before=%v after=%v", initial, restored)
	}
	state, err := term.GetState(int(h.slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.before, state) {
		t.Fatal("termios not restored")
	}
	client, err := egg.Dial(f.socket, f.token)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	t.Logf("mode-restore before=%v after=%v termios=equal session=fidelity still alive", initial, restored)
	t.Logf("session tail=%q", h.snapshot()[len(before):])
	t.Log("sessions exercised=1; transport replacements=1; detach=success")
}
