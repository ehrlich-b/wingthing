//go:build darwin

package daemonctl

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

const daemonArgvHelperEnv = "WT_TEST_DAEMON_ARGV_HELPER"

func init() {
	if os.Getenv(daemonArgvHelperEnv) == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

func TestInspectDaemonPidPreservesExecutablePathSpacesOnDarwin(t *testing.T) {
	spacedDir := filepath.Join(testsocket.Dir(t), "custom install path")
	if err := os.Mkdir(spacedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(spacedDir, "wt")
	if err := os.Symlink(os.Args[0], link); err != nil {
		t.Fatal(err)
	}

	child := exec.Command(link, "wing", "start", "--foreground")
	child.Env = append(os.Environ(), daemonArgvHelperEnv+"=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		matches, err := inspectDaemonPid(child.Process.Pid, WingDaemon)
		if err == nil && matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spaced-path daemon was not recognized: matches=%v err=%v", matches, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStopDaemonAndWaitHandlesUnreapedExitOnDarwin(t *testing.T) {
	child := exec.Command(os.Args[0], "wing", "start", "--foreground")
	child.Env = append(os.Environ(), daemonArgvHelperEnv+"=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	// Do not reap until StopDaemonAndWait and ReadPidFrom have inspected
	// the terminated daemon. Native signal(0) still reports its PID alive.
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	if matches, err := inspectDaemonPid(pid, WingDaemon); err != nil || !matches {
		t.Fatalf("fixture daemon not recognized: matches=%v error=%v", matches, err)
	}
	pidPath := filepath.Join(testsocket.Dir(t), "wing.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopDaemonAndWait(pid, WingDaemon, 3*time.Second); err != nil {
		t.Fatalf("stop unreaped daemon: %v", err)
	}
	if matches, err := inspectDaemonPid(pid, WingDaemon); err != nil || matches {
		t.Fatalf("terminated daemon inspection: matches=%v error=%v", matches, err)
	}
	if _, err := ReadPidFrom(pidPath, WingDaemon); !errors.Is(err, errStaleDaemonPID) {
		t.Fatalf("terminated daemon PID metadata: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("terminated daemon PID file was not removed: %v", err)
	}
}
