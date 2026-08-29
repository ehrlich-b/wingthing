//go:build !darwin && !linux

package main

import (
	"fmt"
	"runtime"
)

func processArgv(pid int) ([]string, error) {
	return nil, fmt.Errorf("process argv inspection is unsupported on %s", runtime.GOOS)
}
