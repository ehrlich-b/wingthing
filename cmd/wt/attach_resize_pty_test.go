package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

// The provider draws a deterministic full-screen image and reports its actual
// PTY dimensions in that image. It has no model, account, or network access.
const resizeCurses = `import curses

def run(s):
 curses.curs_set(0)
 while True:
  rows, cols = s.getmaxyx()
  s.erase()
  for y in range(rows):
   line = ("SIZE:%dx%d ROW:%02d " % (cols, rows, y) + "abcdefghijklmnopqrstuvwxyz" * 16)[:cols-1]
   s.addstr(y, 0, line)
  s.refresh()
  s.getch()
curses.wrapper(run)
`

func TestAttachResizeRealEggPTY(t *testing.T) {
	f := northstarNewFixture(t)
	f.evidence = os.Getenv("WT_RESIZE_EVIDENCE")
	if f.evidence == "" {
		f.evidence = t.TempDir()
	}
	if err := os.MkdirAll(f.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	binary := northstarBuild(t, f, "stable")
	provider := filepath.Join(f.work, "resize.py")
	northstarWrite(t, provider, resizeCurses, 0600)
	for _, tc := range []struct{ name, state, target string }{{"local", f.local, "resize"}, {"remote", f.remote, "devbox:resize"}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "remote" {
				resizeSSH(t, f, binary)
			}
			cmd := exec.Command(binary, "egg", "run", "--session-id", "resize", "--agent=", "--kind", "command", "--command-arg", "/usr/bin/python3", "--command-arg", provider, "--cwd", f.work, "--outer-boundary", "--network", "*", "--agent-domains", "none", "--omit-browser-bridge", "--cols", "80", "--rows", "24")
			cmd.Env = append(f.env, "WINGTHING_DIR="+tc.state)
			cmd.Dir = f.work
			logs := newNativeMouseCapture()
			cmd.Stdout, cmd.Stderr = logs, logs
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					<-done
				}
			})
			logs.wait(t, "serving on ")
			// Serving precedes curses initialization. Observe its first complete
			// frame before sending the initial resize so SIGWINCH cannot race
			// the provider's terminal setup.
			northstarInventoryWait(t, "resize provider initial frame", func() bool {
				read := exec.Command(binary, "session", "read", "resize")
				read.Env = append(f.env, "WINGTHING_DIR="+tc.state)
				read.Dir = f.work
				data, err := read.Output()
				return err == nil && strings.Contains(string(data), "SIZE:80x24 ROW:23")
			})
			h := northstarStartPTY(t, f, binary, 100, 36, "attach", tc.target)
			check := func(cols, rows int, rev uint64) {
				t.Helper()
				marker := fmt.Sprintf("SIZE:%dx%d", cols, rows)
				h.expect(marker, rev, marker, fmt.Sprintf("ROW:%02d", rows-1))
				deadline := time.Now().Add(8 * time.Second)
				for {
					read := exec.Command(binary, "session", "read", "resize")
					read.Env = append(f.env, "WINGTHING_DIR="+tc.state)
					read.Dir = f.work
					data, err := read.Output()
					if err == nil {
						emu := vt.NewEmulator(cols, rows)
						_, _ = emu.Write(data)
						snapshot := emu.String()
						_ = emu.Close()
						screen, _ := h.screen()
						if strings.Contains(snapshot, marker) && screen == snapshot {
							t.Logf("%s client and session read agree at %dx%d", tc.name, cols, rows)
							return
						}
						if time.Now().After(deadline) {
							t.Fatalf("%s %dx%d mismatch\nclient:\n%s\nsnapshot:\n%s", tc.name, cols, rows, screen, snapshot)
						}
					} else if time.Now().After(deadline) {
						t.Fatalf("session read: %v", err)
					}
					time.Sleep(30 * time.Millisecond)
				}
			}
			check(100, 36, 0)
			observer := northstarStartPTY(t, f, binary, 60, 15, "attach", "--read-only", tc.target)
			observer.expect("observer initial screen", 0, "SIZE:100x36")
			observer.resize(50, 14)
			// Detaching the observer orders its resize signal before a fresh
			// snapshot; the writable attachment must retain its dimensions.
			observer.exit("q")
			check(100, 36, 0)
			for _, size := range [][2]int{{40, 12}, {120, 40}, {75, 20}, {142, 47}} {
				check(size[0], size[1], h.resize(size[0], size[1]))
			}
			h.exit("\x02q")
		})
	}
}

// A private loopback SSH daemon exercises OpenSSH's real PTY and window-change
// messages. Keys and configuration are scoped to this test's disposable home.
func resizeSSH(t *testing.T, f *northstarFixture, binary string) {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("loopback sshd unavailable")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	hostkey := filepath.Join(f.root, "hostkey")
	clientkey := filepath.Join(f.root, "clientkey")
	for _, key := range []string{hostkey, clientkey} {
		cmd := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, out)
		}
	}
	pub, err := os.ReadFile(clientkey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(f.root, "authorized_keys")
	northstarWrite(t, auth, string(pub), 0600)
	cfg := filepath.Join(f.root, "sshd_config")
	northstarWrite(t, cfg, fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers %s\nSetEnv HOME=%s\n", port, hostkey, filepath.Join(f.root, "sshd.pid"), auth, current.Username, f.home), 0600)
	logs := newNativeMouseCapture()
	daemon := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", cfg)
	daemon.Stdout, daemon.Stderr = logs, logs
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	t.Cleanup(func() { _ = daemon.Process.Kill(); <-done })
	logs.wait(t, "Server listening on")
	clientcfg := filepath.Join(f.root, "ssh_config")
	northstarWrite(t, clientcfg, fmt.Sprintf("Host ns-devbox\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n BatchMode yes\n StrictHostKeyChecking no\n UserKnownHostsFile /dev/null\n LogLevel ERROR\n", port, current.Username, clientkey), 0600)
	northstarWrite(t, filepath.Join(f.bin, "ssh"), "#!/bin/sh\nexec /usr/bin/ssh -F "+remotepkg.ShellQuote(clientcfg)+" \"$@\"\n", 0700)
	if err := config.SaveRemotes(f.local, map[string]config.Remote{"devbox": {SSHTarget: "ns-devbox", WTBinary: binary, WingthingDir: f.remote}}); err != nil {
		t.Fatal(err)
	}
}
