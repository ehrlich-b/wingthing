//go:build !darwin && !linux

package procinfo

import (
	"fmt"
	"runtime"
	"time"
)

func ProcessIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process identity inspection is unsupported on %s", runtime.GOOS)
}

func ProcessStartTime(pid int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("process start time inspection is unsupported on %s", runtime.GOOS)
}
