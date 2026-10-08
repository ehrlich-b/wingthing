//go:build linux

package procinfo

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
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

func ProcessStartTime(pid int) (time.Time, error) {
	identity, err := ProcessIdentity(pid)
	if err != nil {
		return time.Time{}, err
	}
	ticks, err := strconv.ParseInt(identity[strings.LastIndexByte(identity, ':')+1:], 10, 64)
	if err != nil || ticks < 0 {
		return time.Time{}, fmt.Errorf("process PID %d has invalid start ticks", pid)
	}
	auxv, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return time.Time{}, err
	}
	hz, err := linuxClockTicks(auxv)
	if err != nil {
		return time.Time{}, err
	}
	var uptime, realtime unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &uptime); err != nil {
		return time.Time{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &realtime); err != nil {
		return time.Time{}, err
	}
	boot := time.Unix(realtime.Sec-uptime.Sec, realtime.Nsec-uptime.Nsec)
	// Start ticks are rounded down. Use the end of that tick so a newer host
	// cannot appear to precede admission because of the kernel's precision.
	return boot.Add(time.Duration(ticks/hz)*time.Second + time.Duration(ticks%hz+1)*time.Second/time.Duration(hz)), nil
}

func linuxClockTicks(auxv []byte) (int64, error) {
	word := strconv.IntSize / 8
	read := func(data []byte) uint64 {
		if word == 4 {
			return uint64(binary.NativeEndian.Uint32(data))
		}
		return binary.NativeEndian.Uint64(data)
	}
	for len(auxv) >= 2*word {
		key, value := read(auxv), read(auxv[word:])
		if key == 0 {
			break
		}
		if key == 17 { // AT_CLKTCK is the native clock ticks per second.
			if value > 0 && value <= uint64(time.Second) {
				return int64(value), nil
			}
			break
		}
		auxv = auxv[2*word:]
	}
	return 0, fmt.Errorf("process auxiliary vector has no valid clock tick rate")
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
