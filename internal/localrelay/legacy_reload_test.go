package localrelay

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRoostMCPReloadRejectsLegacyDescriptor(t *testing.T) {
	if os.Getenv("WT_TEST_ROOST_RELOAD") == "1" {
		if err := RunRoostForeground("test", "127.0.0.1:0", false, "", "", "", "", false, false, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(filepath.Join(state, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(state, "wing.yaml")
	if err := os.WriteFile(policy, []byte("connection_mode: direct\nmcp:\n  enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(state, "tools", "safe.yaml")
	if err := os.WriteFile(tool, []byte("name: safe\nrun: /bin/true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A legacy process can retain this descriptor even after all aliases vanish.
	writer, err := os.OpenFile(tool, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRoostMCPReloadRejectsLegacyDescriptor$", "-test.v")
	cmd.Env = append(os.Environ(), "WT_TEST_ROOST_RELOAD=1", "HOME="+home, "WINGTHING_DIR="+state, "WT_CHANNEL=stable", "WT_JWT_SECRET=fixture-secret-for-roost-reload-test", "GITHUB_CLIENT_ID=", "GOOGLE_CLIENT_ID=")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	lines := make(chan string, 128)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	await := func(match func(string) bool) string {
		t.Helper()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("fixture roost exited before reload")
				}
				if match(line) {
					return line
				}
			case <-timer.C:
				t.Fatal("fixture roost timed out")
			}
		}
	}
	await(func(line string) bool { return strings.Contains(line, "open http://") })
	legacy := filepath.Join(state, "eggs", "legacy")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	pid := filepath.Join(legacy, "egg.pid")
	if err := os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("name: safe\nrun: injected-host-command\n"), 0); err != nil {
		t.Fatal(err)
	}
	reload := func() string {
		t.Helper()
		if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
		return await(func(line string) bool {
			return strings.Contains(line, "mcp: reload failed") || strings.Contains(line, "mcp: reloaded")
		})
	}
	if line := reload(); !strings.Contains(line, "loader reload refused") {
		t.Fatalf("roost accepted legacy descriptor injection: %s", line)
	}
	if err := os.Remove(pid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("name: safe\nrun: /bin/true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if line := reload(); !strings.Contains(line, "mcp: reloaded") {
		t.Fatalf("roost reload stayed blocked after replacement: %s", line)
	}
}
