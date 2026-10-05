//go:build linux || darwin

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseRlimit(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want rlimitPair
	}{
		{"0=60", rlimitPair{0, 60}},
		{"7=32", rlimitPair{7, 32}},
		{"9=4294967296", rlimitPair{9, 4 * 1024 * 1024 * 1024}},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			got, err := parseRlimit(tc.arg)
			if err != nil || got != tc.want {
				t.Fatalf("parseRlimit(%q) = %v, %v, want %v", tc.arg, got, err, tc.want)
			}
		})
	}
	for _, arg := range []string{"", "7", "=32", "-1=32", "fds=32", "7=", "7=-1", "7=32=64", "7=18446744073709551616"} {
		t.Run(arg, func(t *testing.T) {
			if _, err := parseRlimit(arg); err == nil {
				t.Fatalf("parseRlimit(%q) accepted invalid limit", arg)
			}
		})
	}
}

func TestApplyRlimitsBeforeExec(t *testing.T) {
	if mode := os.Getenv("WT_TEST_RLIMIT_MODE"); mode != "" {
		limits := []rlimitPair{{unix.RLIMIT_NOFILE, 32}, {unix.RLIMIT_CPU, 60}}
		if runtime.GOOS == "linux" {
			limits = append(limits, rlimitPair{unix.RLIMIT_AS, 4 * 1024 * 1024 * 1024})
		}
		if mode != "reexec-init" && mode != "reexec-drop" {
			if err := applyRlimits(limits); err != nil {
				t.Fatal(err)
			}
		}
		for _, rl := range limits {
			var got unix.Rlimit
			if err := unix.Getrlimit(rl.resource, &got); err != nil {
				t.Fatal(err)
			}
			if got.Cur != rl.value || got.Max != rl.value {
				t.Fatalf("rlimit %d = %v, want soft and hard %d", rl.resource, got, rl.value)
			}
		}
		args := []string{"/bin/sh", "-c", "ulimit -Sn; ulimit -Hn; ulimit -St; ulimit -Ht"}
		if mode == "exec" || mode == "reexec-drop" {
			if err := syscall.Exec(args[0], args, os.Environ()); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(args[0], args[1:]...)
		if mode == "reexec" || mode == "reexec-init" {
			// Mirror the sealed jail's two Go re-execs without requiring
			// Linux namespaces; neither child reapplies the limits.
			nextMode := "reexec-init"
			if mode == "reexec-init" {
				nextMode = "reexec-drop"
			}
			cmd = exec.Command(os.Args[0], "-test.run=^TestApplyRlimitsBeforeExec$")
			cmd.Env = append(os.Environ(), "WT_TEST_RLIMIT_MODE="+nextMode)
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"spawn", "exec", "reexec"} {
		t.Run(mode, func(t *testing.T) {
			// Lowering hard limits is irreversible, so use a disposable helper.
			cmd := exec.Command(os.Args[0], "-test.run=^TestApplyRlimitsBeforeExec$")
			cmd.Env = append(os.Environ(), "WT_TEST_RLIMIT_MODE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != "32\n32\n60\n60" {
				t.Fatalf("first command's soft/hard FD/CPU limits = %q", got)
			}
		})
	}
}

func TestApplyRlimitsReportsFailure(t *testing.T) {
	err := applyRlimits([]rlimitPair{{resource: -1, value: 32}})
	if !errors.Is(err, unix.EINVAL) {
		t.Fatalf("invalid resource error = %v, want EINVAL", err)
	}
}
