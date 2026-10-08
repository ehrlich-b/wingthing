//go:build linux

package procinfo

import (
	"fmt"
	"os"
	"strings"
)

func ProcessIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	start, err := linuxProcessStart(string(data))
	if err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return "linux:" + strings.TrimSpace(string(boot)) + ":" + start, nil
}

func linuxProcessStart(stat string) (string, error) {
	// comm is parenthesized and may itself contain spaces or ')' characters.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return "", fmt.Errorf("process stat has no command terminator")
	}
	fields := strings.Fields(stat[end+1:]) // Starts at field 3 (state).
	if len(fields) < 20 || fields[0] == "Z" {
		return "", fmt.Errorf("process stat is truncated or process is a zombie")
	}
	return fields[19], nil // Field 22 is the start time in ticks since boot.
}
