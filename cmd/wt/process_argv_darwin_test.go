//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
		matches, err := inspectDaemonPid(child.Process.Pid, wingDaemon)
		if err == nil && matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("spaced-path daemon was not recognized: matches=%v err=%v", matches, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
