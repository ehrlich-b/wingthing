//go:build !darwin && !linux

package procinfo

import (
	"fmt"
	"runtime"
)

func ProcessIdentity(pid int) (string, error) {
	return "", fmt.Errorf("process identity inspection is unsupported on %s", runtime.GOOS)
}
