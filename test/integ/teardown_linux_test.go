//go:build e2e && linux

package integ

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestSandboxedEggKillWithDirtyPageCache(t *testing.T) {
	if ok, reason := sandbox.CheckCapability(); !ok {
		t.Skipf("Linux sandbox unavailable: %s", reason)
	}
	binary := os.Getenv("WT_TEST_PREVIEW_BINARY")
	if binary == "" {
		t.Fatal("WT_TEST_PREVIEW_BINARY is required; run make gate GATE=integration")
	}
	root, err := os.MkdirTemp("/tmp", "wt-teardown-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, state, work := filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(root, "work")
	for _, path := range []string{home, work} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(work, "fixture.sh")
	// An explicit handler makes this a cooperative exit, independent of the
	// three-second escalation for interactive shells that ignore SIGTERM.
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nprintf persisted > created.tmp && mv created.tmp created || exit 1\nprintf 'teardown-ready\\n'\nwhile IFS= read -r line; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HOME="+home, "WINGTHING_DIR="+state)
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%q: %v: %s", args, err, output)
		}
		return output
	}
	var started struct {
		Session string `json:"session"`
	}
	output := run("terminal", "--cwd", work, "--json", "--", "/bin/sh", script)
	if err := json.Unmarshal(output, &started); err != nil || started.Session == "" {
		t.Fatalf("start fixture: %s: %v", output, err)
	}
	dir := filepath.Join(state, "eggs", started.Session)
	client, err := egg.Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Kill(ctx, started.Session); err != nil {
			t.Errorf("cleanup fixture: %v", err)
		}
		_ = client.Close()
	})
	run("session", "wait", started.Session, "--contains", "teardown-ready", "--timeout", "10s", "--json")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := client.Status(ctx)
	if err != nil || status.ProcessPid == 0 {
		t.Fatalf("wrapper status: %v: %v", status, err)
	}
	// Exercise real workspace creates and atomic replacement, not merely
	// writes through a pre-existing file bind. Missing denies stay private.
	if _, err := os.Stat(filepath.Join(work, "egg.yaml")); !os.IsNotExist(err) {
		t.Fatalf("deny placeholder appeared on the host: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "created")); err != nil || string(data) != "persisted" {
		t.Fatalf("new workspace file did not persist: %q: %v", data, err)
	}
	mounts, err := os.ReadFile(fmt.Sprintf("/proc/%d/mountinfo", status.ProcessPid))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(mounts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] != work || !strings.Contains(line, " - overlay ") {
			continue
		}
		found = true
		// Checking the kernel's active policy makes the regression deterministic
		// even on a fast disk that flushes the dirty cache in under two seconds.
		if !strings.Contains(","+fields[len(fields)-1]+",", ",volatile,") {
			t.Errorf("writable deny overlay still syncs the host filesystem: %s", line)
		}
	}
	if !found {
		t.Fatal("fixture did not exercise a writable private deny overlay")
	}
	// Keep routine CI disk use bounded; relay pressure runs additionally write
	// four GiB. Close without fsync so dirty pages remain during the Kill RPC.
	dirty, err := os.Create(filepath.Join(root, "dirty"))
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1024*1024)
	for i := 0; i < 64; i++ {
		if _, err := dirty.Write(block); err != nil {
			_ = dirty.Close()
			t.Fatal(err)
		}
	}
	if err := dirty.Close(); err != nil {
		t.Fatal(err)
	}
	killCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	begin := time.Now()
	err = client.Kill(killCtx, started.Session)
	elapsed := time.Since(begin)
	t.Logf("sandboxed egg Kill with dirty page cache: %s", elapsed)
	if err != nil || elapsed >= 2*time.Second {
		logLeaseCleanupDiagnostics(t, dir)
		t.Fatalf("Kill waited for host writeback: %s: %v", elapsed, err)
	}
}
