//go:build darwin

package daemonctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const daemonArgvHelperEnv = "WT_TEST_DAEMON_ARGV_HELPER"

func init() {
	if os.Getenv(daemonArgvHelperEnv) == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

func TestInspectDaemonPidPreservesExecutablePathSpacesOnDarwin(t *testing.T) {
	spacedDir := filepath.Join(t.TempDir(), "custom install path")
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

func TestInspectDaemonPidTreatsUnreapedExitAsGoneOnDarwin(t *testing.T) {
	child := exec.Command(os.Args[0], "wing", "start", "--foreground")
	child.Env = append(os.Environ(), daemonArgvHelperEnv+"=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		matches, err := inspectDaemonPid(child.Process.Pid, WingDaemon)
		if err == nil && matches {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture daemon not live: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave this child unreaped. Darwin may still report PID
	// existence, but an exited process cannot own the listener or expose argv.
	for {
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
		if err != nil {
			t.Fatal(err)
		}
		zombie := false
		for _, process := range processes {
			if int(process.Proc.P_pid) == child.Process.Pid {
				zombie = process.Proc.P_stat == 5
			}
		}
		if zombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not enter zombie state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if matches, err := inspectDaemonPid(child.Process.Pid, WingDaemon); err != nil || matches {
		t.Fatalf("unreaped daemon exit was not recognized: matches=%v err=%v", matches, err)
	}
	if err := StopDaemonAndWait(child.Process.Pid, WingDaemon, time.Second); err != nil {
		t.Fatalf("stop rejected an already exited daemon: %v", err)
	}
}
