//go:build darwin

package procinfo

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ProcessIdentity binds a PID to one process lifetime, including across reboots.
func ProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process PID %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	start := info.Proc.P_starttime
	return fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec), nil
}
