package sandbox

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func sandboxBootID() (string, error) {
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(boot) == "" {
		return "", fmt.Errorf("empty boot id")
	}
	return boot, nil
}

func sandboxProcessStart(pid int) (string, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if len(procs) == 0 {
		return "", os.ErrNotExist
	}
	start := procs[0].Proc.P_starttime
	if start.Sec <= 0 {
		return "", fmt.Errorf("unavailable process start time")
	}
	return fmt.Sprintf("%d.%06d", start.Sec, start.Usec), nil
}

func validSandboxStart(start string) bool {
	sec, usec, ok := strings.Cut(start, ".")
	s, err := strconv.ParseUint(sec, 10, 64)
	u, microErr := strconv.ParseUint(usec, 10, 32)
	return ok && err == nil && microErr == nil && s > 0 && len(usec) == 6 && u < 1000000
}
