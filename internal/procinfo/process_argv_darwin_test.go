//go:build darwin

package procinfo

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProcessArgvMissingPIDOnDarwin(t *testing.T) {
	child := exec.Command("/usr/bin/true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		pid  int
	}{
		{"just_reaped", child.Process.Pid},
		{"never_existed", 1 << 30}, // Beyond Darwin's PID allocation range.
	} {
		t.Run(test.name, func(t *testing.T) {
			_, rawErr := unix.SysctlRaw("kern.procargs2", test.pid)
			members, inventoryErr := unix.SysctlKinfoProcSlice("kern.proc.pid", test.pid)
			t.Logf("pid=%d procargs2=%v proc.pid members=%d error=%v", test.pid, rawErr, len(members), inventoryErr)
			if inventoryErr != nil || len(members) != 0 {
				t.Fatalf("missing PID inventory: members=%v error=%v", members, inventoryErr)
			}
			if argv, err := ProcessArgv(test.pid); !errors.Is(err, unix.ESRCH) || len(argv) != 0 {
				t.Fatalf("missing PID argv=%v error=%v, want ESRCH", argv, err)
			}
		})
	}
}

func TestProcessArgvZombieOnDarwin(t *testing.T) {
	child := exec.Command("/usr/bin/true")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	// Keep the zombie until inspection finishes: signal(0) still succeeds,
	// while argv is already unavailable. This is the daemon stop race.
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		members, err := unix.SysctlKinfoProcSlice("kern.proc.pid", child.Process.Pid)
		if err != nil || len(members) != 1 {
			t.Fatalf("child inventory: members=%v error=%v", members, err)
		}
		if members[0].Proc.P_stat == 5 { // SZOMB in sys/proc.h.
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("child did not become a zombie")
		}
	}
	if err := unix.Kill(child.Process.Pid, 0); err != nil {
		t.Fatalf("unreaped child signal(0): %v", err)
	}
	if argv, err := ProcessArgv(child.Process.Pid); !errors.Is(err, unix.ESRCH) || len(argv) != 0 {
		t.Fatalf("zombie argv=%v error=%v, want ESRCH", argv, err)
	}
}

func TestProcessArgvLivePIDOnDarwin(t *testing.T) {
	argv, err := ProcessArgv(os.Getpid())
	if err != nil || len(argv) == 0 || argv[0] != os.Args[0] {
		t.Fatalf("live process argv=%v error=%v", argv, err)
	}
}

func TestProcessArgvInvalidPIDOnDarwin(t *testing.T) {
	for _, pid := range []int{-1, 0, math.MaxInt32 + 1, (1 << 32) + os.Getpid()} {
		if argv, err := ProcessArgv(pid); !errors.Is(err, unix.EINVAL) || len(argv) != 0 {
			t.Fatalf("invalid PID %d argv=%v error=%v, want EINVAL", pid, argv, err)
		}
	}
}

func TestDarwinProcArgsErrorRequiresVerifiedExit(t *testing.T) {
	for _, test := range []struct {
		name         string
		process      *unix.ExternProc
		inventoryErr error
		want         error
	}{
		{name: "missing", want: unix.ESRCH},
		{name: "zombie", process: &unix.ExternProc{P_stat: 5}, want: unix.ESRCH},
		{name: "exiting_runnable", process: &unix.ExternProc{P_stat: 2, P_flag: 0x6006}, want: unix.ESRCH},
		{name: "runnable", process: &unix.ExternProc{P_stat: 2}, want: unix.EINVAL},
		{name: "sleeping", process: &unix.ExternProc{P_stat: 3}, want: unix.EINVAL},
		{name: "stopped", process: &unix.ExternProc{P_stat: 4}, want: unix.EINVAL},
		{name: "unknown_state", process: &unix.ExternProc{}, want: unix.EINVAL},
		{name: "exec_without_exit", process: &unix.ExternProc{P_stat: 2, P_flag: 0x4000}, want: unix.EINVAL},
		{name: "inventory_denied", inventoryErr: unix.EPERM, want: unix.EINVAL},
		{name: "inventory_invalid", inventoryErr: unix.EINVAL, want: unix.EINVAL},
		{name: "inventory_io_error", inventoryErr: unix.EIO, want: unix.EINVAL},
		{name: "sysctl_unavailable", inventoryErr: unix.ENOENT, want: unix.EINVAL},
		{name: "inventory_esrch", inventoryErr: unix.ESRCH, want: unix.EINVAL},
	} {
		t.Run(test.name, func(t *testing.T) {
			var members []unix.KinfoProc
			if test.process != nil {
				members = []unix.KinfoProc{{Proc: *test.process}}
			}
			got := darwinProcArgsError(unix.EINVAL, members, test.inventoryErr)
			if !errors.Is(got, test.want) {
				t.Fatalf("argv error with process=%v inventory error=%v: %v, want %v", test.process, test.inventoryErr, got, test.want)
			}
			if test.want == unix.EINVAL && (errors.Is(got, unix.ESRCH) || errors.Is(got, os.ErrNotExist)) {
				t.Fatalf("unverified process absence would be hidden by daemon inspection: %v", got)
			}
		})
	}
}
