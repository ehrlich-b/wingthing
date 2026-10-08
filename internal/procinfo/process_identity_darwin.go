//go:build darwin

package procinfo

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// ProcessIdentity binds a PID to one process lifetime, including across reboots.
func ProcessIdentity(pid int) (string, error) {
	start, err := ProcessStartTime(pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("darwin:%d:%d", start.Unix(), start.Nanosecond()/1000), nil
}

// ProcessStartTime returns a conservative upper bound on a process's start time.
func ProcessStartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("invalid process PID %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	start := info.Proc.P_starttime
	if start.Sec <= 0 {
		return time.Time{}, fmt.Errorf("process PID %d has no start time", pid)
	}
	return time.Unix(start.Sec, int64(start.Usec)*1000), nil
}
