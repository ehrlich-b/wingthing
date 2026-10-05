//go:build !darwin && !linux

package procinfo

import (
	"fmt"
	"runtime"
)

func ProcessArgv(pid int) ([]string, error) {
	return nil, fmt.Errorf("process argv inspection is unsupported on %s", runtime.GOOS)
}
