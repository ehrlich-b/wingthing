//go:build windows

package config

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockConfigFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}
