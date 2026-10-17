package sandbox

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func sandboxBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	if boot := strings.TrimSpace(string(data)); boot != "" {
		return boot, nil
	}
	return "", fmt.Errorf("empty boot id")
}

func sandboxProcessStart(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// A hidden /proc entry is not proof of death. Confirm ESRCH before
		// treating ENOENT as a missing pid (e.g. procfs hidepid policies).
		if os.IsNotExist(err) && unix.Kill(pid, 0) != unix.ESRCH {
			return "", fmt.Errorf("process identity unavailable")
		}
		return "", err
	}
	// comm may contain spaces and parentheses. Fields after its final closing
	// parenthesis begin with state (field 3); starttime is field 22.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("incomplete process stat")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", err
	}
	return fields[19], nil
}

func validSandboxStart(start string) bool {
	n, err := strconv.ParseUint(start, 10, 64)
	return err == nil && n > 0
}
