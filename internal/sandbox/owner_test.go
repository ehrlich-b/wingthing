package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSandboxOwnerChild(t *testing.T) {
	if os.Getenv("WT_SANDBOX_OWNER_CHILD") != "1" {
		return
	}
	sb, err := newFallback(Config{})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(sb.(*fallbackSandbox).tmpDir)
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0) // Deliberately bypass Destroy, as a crashed owner would.
}

func sandboxOwnerChild(t *testing.T, root string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSandboxOwnerChild$")
	cmd.Env = append(os.Environ(), "WT_SANDBOX_OWNER_CHILD=1", "TMPDIR="+root)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(out)
		if scan.Scan() {
			ready <- scan.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case path := <-ready:
		if filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), "wt-sandbox-") {
			t.Fatalf("child did not create sandbox: %q", path)
		}
		return cmd, path
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox owner did not become ready")
		return nil, ""
	}
}

func TestSandboxCrashSweepOnNextStart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	child, path := sandboxOwnerChild(t, root)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("expected SIGKILL exit")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("crash should leave sandbox behind: %v", err)
	}
	next, err := newFallback(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Destroy()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dead child's sandbox was not swept: %v", err)
	}
}

func TestSandboxSweepLiveOwner(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	_, path := sandboxOwnerChild(t, root)
	SweepStaleDirectories()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live child's sandbox removed: %v", err)
	}
}

func currentSandboxOwner(t *testing.T) sandboxOwner {
	t.Helper()
	boot, err := sandboxBootID()
	if err != nil {
		t.Fatal(err)
	}
	start, err := sandboxProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return sandboxOwner{PID: os.Getpid(), Start: start, Boot: boot}
}

func writeSandboxOwner(t *testing.T, root string, owner sandboxOwner) string {
	t.Helper()
	path, err := os.MkdirTemp(root, "wt-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, sandboxOwnerFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureSandboxSweep(root string, budget *sweepBudget) string {
	var logs strings.Builder
	sweepSandboxDirectories(root, budget, func(format string, args ...any) {
		fmt.Fprintf(&logs, format+"\n", args...)
	})
	return logs.String()
}

func testSweepBudget() *sweepBudget {
	return &sweepBudget{remaining: 4096, deadline: time.Now().Add(5 * time.Second)}
}

func TestSandboxSweepOwnership(t *testing.T) {
	for _, scenario := range []string{"pid reuse", "different boot", "missing pid", "unlocked live pid", "held lock overrides dead identity"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			owner := currentSandboxOwner(t)
			keep := false
			switch scenario {
			case "pid reuse":
				parts := strings.Split(owner.Start, ".")
				parts[0] += "0"
				owner.Start = strings.Join(parts, ".")
			case "different boot", "held lock overrides dead identity":
				owner.Boot = "00000000-0000-0000-0000-000000000000"
			case "missing pid":
				// A child which has been waited for cannot still own this pid.
				cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
				owner.PID = cmd.Process.Pid
			case "unlocked live pid":
				keep = true
			}
			path := writeSandboxOwner(t, root, owner)
			if scenario == "held lock overrides dead identity" {
				keep = true
				f, err := os.OpenFile(filepath.Join(path, sandboxOwnerFile), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureSandboxSweep(root, testSweepBudget())
			_, err := os.Stat(path)
			if keep && err != nil || !keep && !os.IsNotExist(err) {
				t.Fatalf("keep=%v, stat=%v, logs=%s", keep, err, logs)
			}
			if !strings.Contains(logs, filepath.Base(path)) {
				t.Fatalf("missing decision log: %s", logs)
			}
		})
	}
}

func TestSandboxSweepUnknownOwnerKeptAndLogged(t *testing.T) {
	for _, scenario := range []string{"unreadable", "missing", "malformed", "invalid boot", "invalid start", "oversized pid", "oversized", "fifo"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			owner := currentSandboxOwner(t)
			owner.Boot = "00000000-0000-0000-0000-000000000000"
			switch scenario {
			case "invalid boot":
				owner.Boot = "unknown"
			case "invalid start":
				owner.Start = "unknown"
			case "oversized pid":
				owner.PID = 1 << 32
			}
			path := writeSandboxOwner(t, root, owner)
			lock := filepath.Join(path, sandboxOwnerFile)
			switch scenario {
			case "unreadable":
				if err := os.Chmod(lock, 0000); err != nil {
					t.Fatal(err)
				}
			case "missing", "fifo":
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if scenario == "fifo" {
					if err := unix.Mkfifo(lock, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "malformed", "oversized":
				data := "{}"
				if scenario == "oversized" {
					data = strings.Repeat("x", 8192)
				}
				if err := os.WriteFile(lock, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureSandboxSweep(root, testSweepBudget())
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("unknown owner was removed: %v", err)
			}
			if !strings.Contains(logs, "kept "+filepath.Base(path)) {
				t.Fatalf("retention was not logged: %s", logs)
			}
		})
	}
}

func TestSandboxSweepSymlinkEscape(t *testing.T) {
	for _, scenario := range []string{"directory", "lock", "nested"} {
		t.Run(scenario, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			owner := currentSandboxOwner(t)
			owner.Boot = "00000000-0000-0000-0000-000000000000"
			target := writeSandboxOwner(t, outside, owner)
			sentinel := filepath.Join(target, "keep")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "wt-sandbox-escape")
			switch scenario {
			case "directory":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "lock":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(target, sandboxOwnerFile), filepath.Join(path, sandboxOwnerFile)); err != nil {
					t.Fatal(err)
				}
			case "nested":
				path = writeSandboxOwner(t, root, owner)
				if err := os.Symlink(target, filepath.Join(path, "escape")); err != nil {
					t.Fatal(err)
				}
			}
			logs := captureSandboxSweep(root, testSweepBudget())
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "untouched" {
				t.Fatalf("followed symlink outside root: %q, %v", data, err)
			}
			if scenario != "nested" && !strings.Contains(logs, "kept "+filepath.Base(path)) {
				t.Fatalf("escape was not refused and logged: %s", logs)
			}
		})
	}
}

func TestSandboxSweepBoundsRetainOwnerForRetry(t *testing.T) {
	for _, scenario := range []string{"count", "time", "contents"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			owner := currentSandboxOwner(t)
			owner.Boot = "00000000-0000-0000-0000-000000000000"
			path := writeSandboxOwner(t, root, owner)
			budget := testSweepBudget()
			switch scenario {
			case "count":
				budget.remaining = 0
			case "time":
				budget.deadline = time.Now().Add(-time.Second)
			case "contents":
				budget.remaining = 8
				for i := range 20 {
					if err := os.WriteFile(filepath.Join(path, fmt.Sprint(i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			logs := captureSandboxSweep(root, budget)
			if !strings.Contains(logs, errSweepBound.Error()) {
				t.Fatalf("missing bound log: %s", logs)
			}
			if _, err := os.Stat(filepath.Join(path, sandboxOwnerFile)); err != nil {
				t.Fatalf("lost owner needed for retry: %v", err)
			}
			captureSandboxSweep(root, testSweepBudget())
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("retry did not finish cleanup: %v", err)
			}
		})
	}
}
