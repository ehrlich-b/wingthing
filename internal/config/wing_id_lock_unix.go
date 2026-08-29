//go:build darwin || linux

package config

import (
	"os"
	"syscall"
)

func lockConfigFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
